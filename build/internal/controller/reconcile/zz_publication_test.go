package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/controller/fakeforge"
	"github.com/xcosmosbox/cairn/build/internal/controller/githubapp"
	"github.com/xcosmosbox/cairn/build/internal/controller/model"
	"github.com/xcosmosbox/cairn/build/internal/controller/publisher"
	"github.com/xcosmosbox/cairn/build/internal/controller/runner"
	"github.com/xcosmosbox/cairn/build/internal/controller/store"
	"github.com/xcosmosbox/cairn/build/internal/controller/workspace"
	"github.com/xcosmosbox/cairn/core/dkconfig"
	"github.com/xcosmosbox/cairn/core/kbbundle"
	"github.com/xcosmosbox/cairn/core/storage"
)

type publicationForge struct {
	*fakeforge.FakeForge
	releaseCalls, prCalls int
}

func (f *publicationForge) EnsureRelease(ctx context.Context, spec githubapp.ReleaseSpec) (githubapp.Release, error) {
	f.releaseCalls++
	return f.FakeForge.EnsureRelease(ctx, spec)
}
func (f *publicationForge) EnsurePullRequest(ctx context.Context, spec githubapp.PullRequestSpec) (githubapp.PullRequest, error) {
	f.prCalls++
	return f.FakeForge.EnsurePullRequest(ctx, spec)
}

func publicationSetup(t *testing.T, state model.RunState) (*Reconciler, *store.Store, *publicationForge, string) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	remotes := filepath.Join(root, "remotes")
	os.MkdirAll(remotes, 0o755)
	forge := &publicationForge{FakeForge: fakeforge.New()}
	source, err := forge.CreateRepo(remotes, "owner", "source", "main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := forge.CreateRepo(remotes, "owner", "catalog", "main"); err != nil {
		t.Fatal(err)
	}
	sha, err := forge.GetBranchSHA(ctx, "owner", "source", "main")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &dkconfig.Config{Service: dkconfig.ServiceConfig{CacheDir: filepath.Join(root, "cache"), BundleDir: filepath.Join(root, "bundles")}, LLM: dkconfig.LLMConfig{Model: "test-model", Provider: "test-provider", MaxTokens: 100}, Catalog: dkconfig.CatalogConfig{Repo: "owner/catalog", Branch: "main", ManifestDir: "manifests", ReleaseTagTemplate: "kb-{group}-{source_sha}"}, Repos: []dkconfig.RepoConfig{{ID: "repo", Owner: "owner", Name: "source", URL: source, Branch: "main", KGGroup: "kg", Rewrite: dkconfig.RewriteConfig{Mode: "pr", MinConfidence: 0.8}}}}
	st, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.UpsertRepo(ctx, model.ManagedRepo{ID: "repo", GitHubOwner: "owner", GitHubName: "source", SourceURL: source, Branch: "main", KGGroup: "kg", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(ctx, model.Run{RunID: "run", RepoID: "repo", State: state, DesiredSourceSHA: sha, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	ws := workspace.New(filepath.Join(root, "workspaces"))
	if err := ws.EnsureMirror(ctx, "repo", source, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.CheckoutWorktree(ctx, "repo", "run", sha, ""); err != nil {
		t.Fatal(err)
	}
	pub := publisher.New(forge, ws, githubapp.RepoRef{Owner: "owner", Name: "catalog"})
	recon := New(Options{Config: cfg, Store: st, Forge: forge, WS: ws, Runner: &runner.FakeRunner{}, Publisher: pub})
	db, err := storage.NewDB(storage.DBOptions{Path: recon.candidateDBPath("run")})
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	return recon, st, forge, sha
}

// 人工调整分支原先早退，保留旧 SHA，并且再次回写无需审查即可上传。
func TestAdjustedMergePersistsSourceAndReviewsAdditionalWriteback(t *testing.T) {
	for _, rewrite := range []bool{false, true} {
		t.Run(fmt.Sprint(rewrite), func(t *testing.T) {
			ctx := context.Background()
			recon, st, _, sha := publicationSetup(t, model.StateReconcileMergedSource)
			if err := st.UpdateRunDesiredSHA(ctx, "run", strings.Repeat("a", 40)); err != nil {
				t.Fatal(err)
			}
			if err := st.UpsertCandidate(ctx, model.Candidate{CandidateID: "run-cand", RunID: "run", SourceSHA: strings.Repeat("a", 40), ExpectedWorktreeDigest: "sha256:different", Status: model.CandidateBuilding}); err != nil {
				t.Fatal(err)
			}
			called := false
			recon.runner = &runner.FakeRunner{OnIncremental: func(ctx context.Context, req runner.IncrementalRequest) (runner.BuildResult, error) {
				called = true
				if req.RepositoryIdentity == "" {
					t.Fatal("missing repository identity")
				}
				if rewrite {
					if err := os.WriteFile(filepath.Join(req.RepoPath, "README.md"), []byte("human-adjusted convergence\n"), 0o644); err != nil {
						return runner.BuildResult{}, err
					}
				}
				return runner.BuildResult{HasWriteback: rewrite}, nil
			}}
			result, err := recon.Step(ctx, "run")
			if err != nil {
				t.Fatal(err)
			}
			want := model.StateUploadCandidate
			if rewrite {
				want = model.StateCreateSourcePR
			}
			if !called || result.To != want {
				t.Fatalf("called=%v result=%+v want=%s", called, result, want)
			}
			run, err := st.GetRun(ctx, "run")
			if err != nil {
				t.Fatal(err)
			}
			if run.DesiredSourceSHA != sha {
				t.Fatalf("wrong source: %s", run.DesiredSourceSHA)
			}
			if rewrite {
				if _, err := recon.Step(ctx, "run"); err != nil {
					t.Fatal(err)
				}
				pr, err := st.GetPR(ctx, "run", model.PRKindSourceWriteback)
				if err != nil || pr == nil {
					t.Fatalf("new review missing: %v", err)
				}
				if !strings.HasPrefix(pr.Branch, "cairnd/repo/run-") {
					t.Fatalf("round identity missing: %s", pr.Branch)
				}
			}
		})
	}
}

// Release 已成功但 candidate 落盘失败时不能报告推进；重入复用 outbox 的相同 Release。
func TestPublicationPersistenceFailureDoesNotAdvanceAndReplaysEffect(t *testing.T) {
	ctx := context.Background()
	recon, st, forge, _ := publicationSetup(t, model.StateUploadCandidate)
	if _, err := st.DB().Exec(`CREATE TRIGGER candidate_fault BEFORE INSERT ON candidates BEGIN SELECT RAISE(ABORT,'candidate checkpoint failure'); END`); err != nil {
		t.Fatal(err)
	}
	result, err := recon.Step(ctx, "run")
	if err == nil || result.Advanced {
		t.Fatalf("false advancement: %+v %v", result, err)
	}
	if forge.releaseCalls != 1 {
		t.Fatalf("remote calls=%d", forge.releaseCalls)
	}
	if _, err := st.DB().Exec("DROP TRIGGER candidate_fault"); err != nil {
		t.Fatal(err)
	}
	result, err = recon.Step(ctx, "run")
	if err != nil || result.To != model.StateCreateCatalogPR {
		t.Fatalf("replay: %+v %v", result, err)
	}
	if forge.releaseCalls != 1 {
		t.Fatal("replay duplicated remote Release")
	}
	if _, err := st.DB().Exec(`CREATE TRIGGER pr_fault BEFORE INSERT ON pull_requests BEGIN SELECT RAISE(ABORT,'PR checkpoint failure'); END`); err != nil {
		t.Fatal(err)
	}
	result, err = recon.Step(ctx, "run")
	if err == nil || result.Advanced {
		t.Fatalf("false PR advancement: %+v %v", result, err)
	}
	if forge.prCalls != 1 {
		t.Fatalf("PR remote calls=%d", forge.prCalls)
	}
	if _, err := st.DB().Exec("DROP TRIGGER pr_fault"); err != nil {
		t.Fatal(err)
	}
	result, err = recon.Step(ctx, "run")
	if err != nil || result.To != model.StateAwaitCatalogPR {
		t.Fatalf("PR replay: %+v %v", result, err)
	}
	if forge.prCalls != 1 {
		t.Fatal("replay duplicated remote PR")
	}
}

func TestHumanModifiedMergedCatalogDoesNotPromoteWrongCandidate(t *testing.T) {
	for _, modify := range []bool{false, true} {
		t.Run(fmt.Sprint(modify), func(t *testing.T) {
			ctx := context.Background()
			recon, st, forge, _ := publicationSetup(t, model.StateUploadCandidate)
			for i := 0; i < 2; i++ {
				result, err := recon.Step(ctx, "run")
				if err != nil || !result.Advanced {
					t.Fatalf("publish setup: %+v %v", result, err)
				}
			}
			pr, err := st.GetPR(ctx, "run", model.PRKindCatalogPublish)
			if err != nil || pr == nil {
				t.Fatalf("PR missing: %v", err)
			}
			if modify {
				wt := recon.ws.SourceWorktree("run-cat")
				path := filepath.Join(wt, "manifests", "kg", "stable.json")
				data, _ := os.ReadFile(path)
				var manifest kbbundle.CatalogManifest
				json.Unmarshal(data, &manifest)
				manifest.SourceCommit = strings.Repeat("f", 40)
				data, _ = json.Marshal(manifest)
				os.WriteFile(path, data, 0o644)
				if err := recon.ws.CommitAll(ctx, wt, "human modified pointer", "human", "human@example.test"); err != nil {
					t.Fatal(err)
				}
				if err := recon.ws.PushBranch(ctx, "catalog", forge.RemoteURL("owner", "catalog"), pr.Branch, ""); err != nil {
					t.Fatal(err)
				}
				// Refresh fake's observed head as a real forge does after a human push.
				if _, err := forge.EnsurePullRequest(ctx, githubapp.PullRequestSpec{Owner: "owner", Repo: "catalog", HeadBranch: pr.Branch, BaseBranch: "main", RunMarker: "test"}); err != nil {
					t.Fatal(err)
				}
			}
			if err := forge.MergePullRequest(ctx, "owner", "catalog", pr.Number, "merge"); err != nil {
				t.Fatal(err)
			}
			if modify {
				humanHead, err := recon.ws.HeadSHA(ctx, recon.ws.SourceWorktree("run-cat"))
				if err != nil {
					t.Fatal(err)
				}
				// FakeForge's merge stores the initial PR head; make the bare Git tree match the real human-adjusted merge.
				if output, err := exec.Command("git", "--git-dir", forge.RemoteURL("owner", "catalog"), "update-ref", "refs/heads/main", humanHead).CombinedOutput(); err != nil {
					t.Fatalf("human merge fixture: %s %v", output, err)
				}
			}
			result, err := recon.Step(ctx, "run")
			if err != nil {
				t.Fatal(err)
			}
			want := model.StateStable
			if modify {
				want = model.StateBlocked
			}
			if result.To != want {
				t.Fatalf("promotion=%+v want=%s", result, want)
			}
			repo, err := st.GetRepo(ctx, "repo")
			if err != nil {
				t.Fatal(err)
			}
			if modify && repo.LastStableBundleDigest != "" {
				t.Fatal("wrong catalog promoted local stable")
			}
		})
	}
}

func TestFingerprintTracksActualSchemaPromptsAndConfig(t *testing.T) {
	recon, _, _, sha := publicationSetup(t, model.StateUploadCandidate)
	first := recon.currentFingerprint()
	if first.DBSchemaVersion != storage.LatestSchemaVersion() || first.BuilderCommit == "" || first.PromptSetVersion == "" || first.IdentityAlgorithmVer == "" || first.DiscoveryRulesDigest == "" {
		t.Fatalf("incomplete actual identity: %+v", first)
	}
	if first.Digest() != recon.currentFingerprint().Digest() {
		t.Fatal("fingerprint is unstable")
	}
	recon.cfg.Repos[0].Rewrite.MinConfidence += 0.01
	if first.ConfigDigest == recon.currentFingerprint().ConfigDigest || first.Digest() == recon.currentFingerprint().Digest() {
		t.Fatal("confidence did not change build identity")
	}
	first = recon.currentFingerprint()
	recon.cfg.LLM.Model = "new-model"
	if first.Digest() == recon.currentFingerprint().Digest() {
		t.Fatal("model did not change identity")
	}
	repo := &model.ManagedRepo{LastStableSourceSHA: sha, LastStableBundleDigest: "sha256:stable", LastStableFingerprint: strings.TrimPrefix(first.Digest(), "sha256:")}
	if !recon.NeedsReconcile(repo, sha) {
		t.Fatal("same SHA semantics change not scheduled")
	}
}
