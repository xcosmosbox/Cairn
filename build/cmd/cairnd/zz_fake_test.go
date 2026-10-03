package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/controller/githubapp"
	"github.com/xcosmosbox/cairn/build/internal/controller/model"
	"github.com/xcosmosbox/cairn/build/internal/controller/publisher"
	"github.com/xcosmosbox/cairn/build/internal/controller/reconcile"
	"github.com/xcosmosbox/cairn/build/internal/controller/runner"
	"github.com/xcosmosbox/cairn/build/internal/controller/store"
	"github.com/xcosmosbox/cairn/build/internal/controller/workspace"
	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/writeback"
	"github.com/xcosmosbox/cairn/core/dkconfig"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/kbbundle"
	"github.com/xcosmosbox/cairn/core/repoidentity"
	"github.com/xcosmosbox/cairn/core/storage"
)

// 原 fake quickstart 未注册远端、--once 不存在，还在 Git 失败时退出 0。
func TestFakeOncePublishesAndMergesInIsolatedSession(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cfg := fakeTestConfig(t.TempDir())
	stateBefore := cfg.Service.StateDB
	if err := os.WriteFile(stateBefore, []byte("existing state must remain untouched"), 0o644); err != nil {
		t.Fatal(err)
	}
	forge, err := bootstrapFake(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Service.StateDB == stateBefore || cfg.Repos[0].URL == "https://github.invalid/synthetic/source.git" {
		t.Fatal("fake environment did not isolate real paths")
	}
	st, err := store.Open(cfg.Service.StateDB)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.UpsertRepo(ctx, toManagedRepo(cfg.Repos[0])); err != nil {
		t.Fatal(err)
	}
	ws := workspace.New(cfg.Service.WorkspacesDir)
	pub := publisher.New(forge, ws, githubapp.RepoRef{Owner: "synthetic", Name: "catalog"})
	recon := reconcile.New(reconcile.Options{Config: &cfg, Store: st, Forge: forge, WS: ws, Runner: fakePipelineRunner(), Publisher: pub})
	if err := runOnce(ctx, &cfg, st, forge, recon, true); err != nil {
		t.Fatal(err)
	}
	runs, err := st.ListRuns(ctx, "demo", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].State != model.StateStable {
		t.Fatalf("not Stable: %#v", runs)
	}
	for _, kind := range []model.PRKind{model.PRKindSourceWriteback, model.PRKindCatalogPublish} {
		pr, err := st.GetPR(ctx, runs[0].RunID, kind)
		if err != nil || pr == nil {
			t.Fatalf("missing %s PR: %v", kind, err)
		}
		remote, err := forge.GetPullRequest(ctx, pr.Owner, pr.Repo, pr.Number)
		if err != nil || !remote.Merged {
			t.Fatalf("%s not merged: %v", kind, err)
		}
	}
	cand, err := st.GetCandidate(ctx, runs[0].RunID+"-cand")
	if err != nil || cand == nil {
		t.Fatalf("candidate: %v", err)
	}
	if cand.Status != model.CandidateStable || cand.ReleaseID == 0 {
		t.Fatalf("not published: %#v", cand)
	}
	if _, err := kbbundle.Verify(cand.BundlePath); err != nil {
		t.Fatal(err)
	}
	catalog := forge.RemoteURL("synthetic", "catalog")
	if _, err := fakeGit(ctx, catalog, "show", "main:knowledge-bases/demo/stable.json"); err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile(stateBefore)
	if err != nil || string(old) != "existing state must remain untouched" {
		t.Fatal("existing state changed")
	}
}

func TestOnceSourceFailureIsReturned(t *testing.T) {
	t.Parallel()
	cfg := fakeTestConfig(t.TempDir())
	forge, err := bootstrapFake(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.Service.StateDB)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.UpsertRepo(context.Background(), toManagedRepo(cfg.Repos[0])); err != nil {
		t.Fatal(err)
	}
	cfg.Repos[0].Branch = "missing"
	if err := runOnce(context.Background(), &cfg, st, forge, nil, false); err == nil {
		t.Fatal("missing source branch failure swallowed")
	}
}

func TestDocumentedOnceFlagAccepted(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "cairnd")
	cmd := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-o", binary, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, out)
	}
	// 配置错误发生在 flag 解析之后，因此证明 --once 已被接受，且错误返回非零。
	cmd = exec.CommandContext(ctx, binary, "run", "--once", "--fake", "--config", filepath.Join(t.TempDir(), "missing.yaml"))
	out, err := cmd.CombinedOutput()
	if err == nil || strings.Contains(string(out), "flag provided but not defined") || !strings.Contains(string(out), "missing.yaml") {
		t.Fatalf("unexpected once behavior: %v: %s", err, out)
	}
	root := t.TempDir()
	config := filepath.Join(root, "fake.yaml")
	data := fmt.Sprintf("service:\n  mode: once\n  state_db: %q\n  workspaces_dir: %q\n  cache_dir: %q\n  bundle_dir: %q\ncatalog:\n  repo: synthetic/catalog\n  branch: main\n  manifest_dir: knowledge-bases\nrepos:\n  - id: demo\n    owner: synthetic\n    name: source\n    url: https://github.invalid/synthetic/source.git\n    branch: main\n    kg_group: demo\n", filepath.Join(root, "state.db"), filepath.Join(root, "fake"), filepath.Join(root, "cache"), filepath.Join(root, "bundles"))
	if err := os.WriteFile(config, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd = exec.CommandContext(ctx, binary, "run", "--once", "--fake", "--config", config)
	out, err = cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "最终状态: Stable") || !strings.Contains(string(out), "source_writeback PR") || !strings.Contains(string(out), "catalog_publish PR") {
		t.Fatalf("documented CLI flow did not complete: %v: %s", err, out)
	}
}

func fakeTestConfig(root string) dkconfig.Config {
	cfg := dkconfig.Defaults()
	cfg.Service.Mode = dkconfig.ModeOnce
	cfg.Service.StateDB = filepath.Join(root, "existing.db")
	cfg.Service.WorkspacesDir = filepath.Join(root, "fake")
	cfg.Service.CacheDir = filepath.Join(root, "existing-cache")
	cfg.Service.BundleDir = filepath.Join(root, "existing-bundles")
	cfg.Catalog = dkconfig.CatalogConfig{Repo: "synthetic/catalog", Branch: "main", ManifestDir: "knowledge-bases"}
	cfg.Repos = []dkconfig.RepoConfig{{ID: "demo", Owner: "synthetic", Name: "source", URL: "https://github.invalid/synthetic/source.git", Branch: "main", KGGroup: "demo", Rewrite: dkconfig.RewriteConfig{Mode: "pr"}}}
	return cfg
}

func fakeGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("fake git %v: %w: %s", args, err, out)
	}
	return string(out), nil
}

func TestFakeFixtureBindsIdentityAndCanRetryFullBuild(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfg := fakeTestConfig(t.TempDir())
	forge, err := bootstrapFake(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	ws := workspace.New(cfg.Service.WorkspacesDir)
	if err := ws.EnsureMirror(ctx, "demo", cfg.Repos[0].URL, ""); err != nil {
		t.Fatal(err)
	}
	sha, err := forge.GetBranchSHA(ctx, "synthetic", "source", "main")
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := ws.CheckoutWorktree(ctx, "demo", "fixture", sha, "")
	if err != nil {
		t.Fatal(err)
	}
	request := runner.FullRequest{RepoPath: worktree, DBPath: filepath.Join(t.TempDir(), "candidate.db")}
	builder := fakePipelineRunner()
	var materialized [2][]byte
	for i := 0; i < 2; i++ {
		if _, err := builder.Full(ctx, request); err != nil {
			t.Fatalf("full retry %d: %v", i, err)
		}
		for j, name := range []string{"SKILL.md", "SKILL.md.kg.yaml"} {
			data, err := os.ReadFile(filepath.Join(worktree, name))
			if err != nil {
				t.Fatal(err)
			}
			if i > 0 && !bytes.Equal(data, materialized[j]) {
				t.Fatalf("fixed source %s drifted on repeated full build", name)
			}
			materialized[j] = data
		}
	}
	db, err := storage.OpenReadOnly(request.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := storage.NewRepositoryIdentityRepo(db).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := repoidentity.CanonicalGitURL(cfg.Repos[0].URL)
	if err != nil {
		t.Fatal(err)
	}
	if identity != expected {
		t.Fatalf("fixture identity %q, want %q", identity, expected)
	}
	var count int
	if err := db.Conn().QueryRowContext(ctx, "SELECT count(*) FROM nodes").Scan(&count); err != nil || count != 4 {
		t.Fatalf("retried fixture node count: %d: %v", count, err)
	}
	assertFakeSourceClosure(t, ctx, db, request)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(request.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(worktree, "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(worktree, "SKILL.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := builder.Full(ctx, request); err == nil {
		t.Fatal("writeback error hidden")
	}
	after, err := os.ReadFile(request.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed fixture writeback modified existing candidate DB")
	}
}

// 原 fixture 的 Concept 没有 node_sources 和 sidecar，完整发布门禁必然拒绝。
// Check the production validator and the exact UUID/document/sidecar/source graph.
func assertFakeSourceClosure(t *testing.T, ctx context.Context, db *storage.DB, request runner.FullRequest) {
	t.Helper()
	validation, err := (&runner.Runner{}).Validate(ctx, runner.ValidateRequest{DBPath: request.DBPath, RepoPath: request.RepoPath})
	if err != nil || !validation.OK || validation.NodeCount != 4 || validation.EdgeCount != 3 {
		t.Fatalf("real runner rejected fake source materialization: %+v %v", validation, err)
	}
	const member = "fake-concept-blocking-queue"
	id := extract.NodeUUID([]string{member})
	sources, err := storage.NewNodeSourceRepo(db).ListAll(ctx)
	want := storage.NodeSource{NodeUUID: id, MemberID: member, Skill: "fake", FilePath: "SKILL.md", StartLine: 6, EndLine: 8}
	if err != nil || len(sources) != 1 || sources[0] != want {
		t.Fatalf("wrong source ledger: %+v %v", sources, err)
	}
	node, err := storage.NewNodeRepo(db).GetByID(ctx, id)
	if err != nil || node == nil || node.Label != dktypes.LabelConcept {
		t.Fatalf("missing deterministic concept: %+v %v", node, err)
	}
	path := filepath.Join(request.RepoPath, "SKILL.md")
	md, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sf, err := writeback.ReadSidecar(path)
	if err != nil || sf.Doc != "SKILL.md" || sf.DocHash != writeback.HashEditable(string(md)) || len(sf.Nodes) != 1 {
		t.Fatalf("wrong sidecar baseline: %+v %v", sf, err)
	}
	sn := sf.Nodes[0]
	if sn.UUID != id || sn.Tag != "concept" || sn.Name != node.Name || sn.DomainSlug != node.Domain || sn.SubdomainSlug != node.Subdomain || sn.Shared || len(sn.Members) != 1 || sn.Members[0] != member || sn.Span == nil || sn.Span.StartLine != want.StartLine || sn.Span.EndLine != want.EndLine || sn.SummaryHash != writeback.HashEditable(node.Summary) || sn.DescriptionHash != writeback.HashEditable(node.Description) || sn.Provenance != string(node.Provenance) {
		t.Fatalf("sidecar does not match graph/source ledger: %+v %+v", sn, node)
	}
	if !strings.Contains(string(md), writeback.CanonicalFullOpenComment(id, "concept", false)) || !strings.Contains(string(md), writeback.CanonicalFullCloseComment(id)) {
		t.Fatal("document does not contain the matching UUID anchor pair")
	}
	edges, err := storage.NewEdgeRepo(db).ListAll(ctx)
	if err != nil || len(edges) != 3 {
		t.Fatalf("wrong hierarchy edge count: %+v %v", edges, err)
	}
	expected := map[string]dktypes.RelationKind{
		"skill::fake->domain::demo":            dktypes.KindProvides,
		"domain::demo->subdomain::demo::queue": dktypes.KindComposes,
		"subdomain::demo::queue->" + id:        dktypes.KindComposes,
	}
	for _, edge := range edges {
		key := edge.SourceID + "->" + edge.TargetID
		kind, ok := expected[key]
		if !ok || edge.Kind != kind {
			t.Fatalf("unexpected hierarchy edge: %+v", edge)
		}
		delete(expected, key)
	}
	if len(expected) != 0 {
		t.Fatalf("missing hierarchy edges: %+v", expected)
	}
}
