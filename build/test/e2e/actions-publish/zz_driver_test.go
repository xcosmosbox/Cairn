package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/controller/githubapp"
	"github.com/xcosmosbox/cairn/build/internal/controller/publisher"
	"github.com/xcosmosbox/cairn/build/internal/controller/runner"
	"github.com/xcosmosbox/cairn/build/internal/writeback"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/kbbundle"
	"github.com/xcosmosbox/cairn/core/storage"
)

func TestJobTokenSourceRotationCancellationAndEmpty(t *testing.T) {
	source := envTokenSource{baseURL: "https://api.github.com", envName: "CAIRN_TEST_JOB_TOKEN"}
	for _, current := range []string{"synthetic-first", "synthetic-second"} {
		t.Setenv(source.envName, current)
		got, expiry, err := source.InstallToken(context.Background(), 7)
		if err != nil || got != current || !expiry.IsZero() {
			t.Fatalf("rotation/expiry contract failed: %v", err)
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := source.InstallToken(canceled, 7); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation not preserved: %v", err)
	}
	t.Setenv(source.envName, "")
	if _, _, err := source.InstallToken(context.Background(), 7); err == nil {
		t.Fatal("empty job token accepted")
	}
}

func driverFixture(t *testing.T) (publishSpec, string, string, string) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(root, "knowledge.db")
	db, err := storage.NewDB(storage.DBOptions{Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	node := &dktypes.Node{ID: "node", Label: dktypes.LabelConcept, Name: "concept", Domain: "domain", Subdomain: "subdomain", Summary: "summary", Description: "description", Confidence: 1, Provenance: dktypes.ProvenanceLLMInferred}
	if err := storage.NewNodeRepo(db).Insert(ctx, node); err != nil {
		t.Fatal(err)
	}
	if err := storage.NewNodeSourceRepo(db).InsertBatch(ctx, []storage.NodeSource{{NodeUUID: "node", MemberID: "member", Skill: "skill", FilePath: "references/doc.md"}}); err != nil {
		t.Fatal(err)
	}
	if err := storage.NewRepositoryIdentityRepo(db).Set(ctx, "git:github.com/owner/source"); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.NewKBVersionRepo(db).Bump(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	update := writeback.NodeUpdate{UUID: node.ID, Tag: string(node.Label), Name: node.Name, Domain: "domain", Subdomain: "subdomain", DomainSlug: "domain", SubdomainSlug: "subdomain", Summary: node.Summary, Description: node.Description, Members: []string{"member"}, SourceFiles: []string{"references/doc.md"}, Provenance: string(node.Provenance)}
	if err := writeback.RewriteDoc(source, "references/doc.md", []writeback.NodeUpdate{update}, time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		data, err := exec.Command("git", append([]string{"-c", "core.hooksPath=" + os.DevNull, "-C", source}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("fixture Git command failed: %v %s", err, data)
		}
		return strings.TrimSpace(string(data))
	}
	git("init", "-b", "main")
	git("config", "user.name", "fixture")
	git("config", "user.email", "fixture@example.invalid")
	git("remote", "add", "origin", "https://github.com/owner/source.git")
	git("add", ".")
	git("commit", "-m", "reviewed synthetic source")
	sha := git("rev-parse", "HEAD")
	digest := "sha256:" + strings.Repeat("1", 64)
	spec := publishSpec{Repo: sourceSpec{ID: "fixture", URL: "https://github.com/owner/source.git", Owner: "owner", Name: "source", Branch: "main", KGGroup: "fixture", MinConfidence: 0.7}, Catalog: catalogSpec{Repo: "owner/catalog", Branch: "main", ManifestDir: "knowledge-bases", ReleaseTag: "kb-{group}-{source_sha}", ReleasePrerelease: true}, SourceCommit: sha, CreatedAt: "2026-10-03T00:00:00Z", ConfigDigest: digest, Fingerprint: publisher.Fingerprint{BuilderVersion: "fixture", BuilderCommit: digest, ControllerSchemaVer: 3, DBSchemaVersion: storage.LatestSchemaVersion(), PromptSetVersion: digest, IdentityAlgorithmVer: digest, DiscoveryRulesDigest: digest, Model: "fixture-model", Provider: "openai_compatible", AnnotationSchemaVer: 1, ConfigDigest: digest}}
	report := filepath.Join(root, "build-report.json")
	if err := os.WriteFile(report, []byte(`{"fixture":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return spec, source, dbPath, report
}

// 用真实 Git/SQLite/HTTP 走驱动全链路；只有远端服务响应是明确的测试 fixture。
// Exercise publication replay and remote consumption without a fake Forge or LLM.
func TestDriverPublicationReplayAndRemoteConsumer(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "synthetic-job-token")
	spec, source, dbPath, reportPath := driverFixture(t)
	root := t.TempDir()
	specPath := filepath.Join(root, "spec.json")
	if err := writeJSON(specPath, spec); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var rel githubapp.Release
	var asset []byte
	var cm kbbundle.CatalogManifest
	var creates, uploads, replays atomic.Int32
	var base string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Authorization"), "synthetic-job-token") {
			t.Error("missing job authentication")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.Contains(r.URL.Path, "/branches/"):
			json.NewEncoder(w).Encode(map[string]any{"commit": map[string]string{"sha": spec.SourceCommit}})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contents/"):
			json.NewEncoder(w).Encode(cm)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/releases/tags/"):
			if rel.ID == 0 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			replays.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"id": rel.ID, "tag_name": rel.Tag, "html_url": rel.HTMLURL, "prerelease": rel.Prelease, "assets_url": rel.AssetURL, "assets": []any{map[string]any{"id": 9, "name": "bundle.tar.gz", "browser_download_url": base + "/asset"}}})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/releases"):
			creates.Add(1)
			var body struct {
				Tag string `json:"tag_name"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				return
			}
			rel = githubapp.Release{ID: 7, Tag: body.Tag, HTMLURL: base + "/release", AssetURL: base + "/assets", Prelease: true}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(rel)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/assets"):
			uploads.Add(1)
			var err error
			asset, err = io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"id": 9, "name": "bundle.tar.gz"})
		case r.Method == http.MethodGet && r.URL.Path == "/asset":
			w.Write(asset)
		default:
			t.Errorf("unexpected remote effect: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	base = server.URL
	defer server.Close()
	publishedPath := filepath.Join(root, "publish.json")
	builderRecordPath := filepath.Join(root, "frozen-builder-fingerprint.json")
	if err := writeJSON(builderRecordPath, spec.Fingerprint); err != nil {
		t.Fatal(err)
	}
	args := []string{"--spec", specPath, "--source-dir", source, "--kg-db", dbPath, "--build-report", reportPath, "--builder-record", builderRecordPath, "--output-dir", filepath.Join(root, "stage"), "--receipt", publishedPath, "--api-base", base}
	if err := run(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	var published receipt
	if err := readJSON(publishedPath, &published); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	cm = published.Catalog
	mu.Unlock()
	if creates.Load() != 1 || uploads.Load() != 1 || replays.Load() != 1 || !published.PublicationReplay || !published.Validation.OK {
		t.Fatalf("remote counts=%d/%d/%d receipt=%+v", creates.Load(), uploads.Load(), replays.Load(), published)
	}
	consumedPath := filepath.Join(root, "consume.json")
	if err := run(context.Background(), []string{"--phase", "consume", "--spec", specPath, "--expected-receipt", publishedPath, "--install-dir", filepath.Join(root, "install"), "--receipt", consumedPath, "--api-base", base}); err != nil {
		t.Fatal(err)
	}
	var consumed receipt
	if err := readJSON(consumedPath, &consumed); err != nil {
		t.Fatal(err)
	}
	if !consumed.RepeatInstallVerified || consumed.Catalog != published.Catalog || !filepath.IsAbs(consumed.InstalledDB) {
		t.Fatalf("consumer identity/reinstall failed: %+v", consumed)
	}
	for _, path := range []string{publishedPath, consumedPath, filepath.Join(root, "stage", "candidate.json")} {
		data, err := os.ReadFile(path)
		if err != nil || strings.Contains(string(data), "synthetic-job-token") {
			t.Fatalf("receipt contained credential or could not be read: %v", err)
		}
	}
}

// 手工改过或未审查的源不能触发发布，甚至不能先读取远端 branch。
// Local ownership and reviewed-tree failures must stop before the first remote request.
func TestDriverRejectsUnreviewedSourceBeforeRemoteRequest(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"wrong-commit", "dirty-source", "wrong-owner", "invalid-closure"} {
		t.Run(mode, func(t *testing.T) {
			spec, source, dbPath, reportPath := driverFixture(t)
			builderRecordPath := filepath.Join(t.TempDir(), "frozen-builder-fingerprint.json")
			if err := writeJSON(builderRecordPath, spec.Fingerprint); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "wrong-commit":
				spec.SourceCommit = strings.Repeat("a", 40)
			case "dirty-source":
				if err := os.WriteFile(filepath.Join(source, "unreviewed.md"), []byte("unreviewed"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "wrong-owner":
				spec.Repo.Owner = "different"
				spec.Repo.URL = "https://github.com/different/source.git"
			case "invalid-closure":
				db, err := storage.OpenExisting(dbPath)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.Conn().Exec("DELETE FROM node_sources"); err != nil {
					t.Fatal(err)
				}
				db.Close()
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer server.Close()
			forge := githubapp.NewHTTPForge(envTokenSource{baseURL: server.URL, envName: "UNUSED_SYNTHETIC_TOKEN"}, 0)
			repo, catalog, created, err := spec.validate()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := publish(context.Background(), forge, spec, repo, catalog, created, source, dbPath, reportPath, builderRecordPath, "", t.TempDir()); err == nil {
				t.Fatal("invalid source was published")
			}
			if calls.Load() != 0 {
				t.Fatalf("invalid source reached remote: %d requests", calls.Load())
			}
		})
	}
}

func TestDriverRejectsMissingOrMismatchedBuilderRecordBeforeRemoteRequest(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"missing", "malformed", "unknown-field", "changed-builder", "changed-config"} {
		t.Run(mode, func(t *testing.T) {
			spec, source, dbPath, reportPath := driverFixture(t)
			repo, catalog, created, err := spec.validate()
			if err != nil {
				t.Fatal(err)
			}
			builderRecordPath := filepath.Join(t.TempDir(), "frozen-builder-fingerprint.json")
			recorded := spec.Fingerprint
			switch mode {
			case "malformed":
				err = os.WriteFile(builderRecordPath, []byte("{"), 0o600)
			case "unknown-field":
				data, _ := json.Marshal(recorded)
				data = append(data[:len(data)-1], []byte(`,"unreviewed":true}`)...)
				err = os.WriteFile(builderRecordPath, data, 0o600)
			case "changed-builder":
				recorded.BuilderCommit = "different-builder"
				err = writeJSON(builderRecordPath, recorded)
			case "changed-config":
				recorded.ConfigDigest = "sha256:" + strings.Repeat("2", 64)
				err = writeJSON(builderRecordPath, recorded)
			}
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer server.Close()
			forge := githubapp.NewHTTPForge(envTokenSource{baseURL: server.URL, envName: "UNUSED_SYNTHETIC_TOKEN"}, 0)
			_, err = publish(context.Background(), forge, spec, repo, catalog, created, source, dbPath, reportPath, builderRecordPath, "", t.TempDir())
			if err == nil || !strings.Contains(err.Error(), "builder record") {
				t.Fatalf("invalid builder record was not rejected locally: %v", err)
			}
			if calls.Load() != 0 {
				t.Fatalf("invalid builder record reached remote: %d requests", calls.Load())
			}
		})
	}
}

func TestDriverRejectsConsumerProvenanceDriftBeforeRemoteRequest(t *testing.T) {
	t.Parallel()
	spec, _, _, _ := driverFixture(t)
	repo, catalog, _, err := spec.validate()
	if err != nil {
		t.Fatal(err)
	}
	expected := receipt{
		Phase: "publish", PublicationReplay: true,
		Validation: runner.ValidationReport{OK: true},
		Release:    githubapp.Release{ID: 7, Tag: "fixed-tag"},
		Catalog: kbbundle.CatalogManifest{Manifest: kbbundle.Manifest{
			KG: repo.KGGroup, SourceRepo: repo.Owner + "/" + repo.Name, SourceRef: repo.Branch,
			SourceCommit: spec.SourceCommit, Model: spec.Fingerprint.Model,
			BuilderVersion: spec.Fingerprint.BuilderVersion, BuilderCommit: spec.Fingerprint.BuilderCommit,
			PromptSetVersion: spec.Fingerprint.PromptSetVersion, SchemaVersion: spec.Fingerprint.DBSchemaVersion,
			ConfigDigest: spec.ConfigDigest, CreatedAt: spec.CreatedAt,
		}, ReleaseTag: "fixed-tag"},
	}
	for _, test := range []struct {
		name   string
		change func(*kbbundle.CatalogManifest)
	}{
		{"source-ref", func(m *kbbundle.CatalogManifest) { m.SourceRef = "unreviewed-branch" }},
		{"builder-version", func(m *kbbundle.CatalogManifest) { m.BuilderVersion = "different-version" }},
		{"builder-commit", func(m *kbbundle.CatalogManifest) { m.BuilderCommit = "different-builder" }},
		{"prompt-set", func(m *kbbundle.CatalogManifest) { m.PromptSetVersion = "different-prompts" }},
		{"schema-version", func(m *kbbundle.CatalogManifest) { m.SchemaVersion++ }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := expected
			test.change(&changed.Catalog)
			// No token is configured: reject the receipt before authentication or remote access.
			_, err := consume(context.Background(), envTokenSource{envName: "UNUSED_SYNTHETIC_TOKEN"}, spec, repo, catalog, changed, t.TempDir())
			if err == nil || !strings.Contains(err.Error(), "receipt does not match specification") {
				t.Fatalf("consumer provenance drift was not rejected locally: %v", err)
			}
		})
	}
}
