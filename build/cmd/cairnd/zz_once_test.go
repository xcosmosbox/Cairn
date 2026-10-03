package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/controller/fakeforge"
	"github.com/xcosmosbox/cairn/build/internal/controller/githubapp"
	"github.com/xcosmosbox/cairn/build/internal/controller/model"
	"github.com/xcosmosbox/cairn/build/internal/controller/publisher"
	"github.com/xcosmosbox/cairn/build/internal/controller/reconcile"
	"github.com/xcosmosbox/cairn/build/internal/controller/store"
	"github.com/xcosmosbox/cairn/build/internal/controller/workspace"
	"github.com/xcosmosbox/cairn/core/dkconfig"
)

// 原 one-shot 只比较 LastSeenSourceSHA，成功后同 SHA 的配置漂移被静默跳过。
// Exercise the real isolated lifecycle so a scheduled rebuild must finish and publish.
func TestOnceSameSHAConfigurationDriftRebuilds(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
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
	if err := st.UpsertRepo(ctx, toManagedRepo(cfg.Repos[0])); err != nil {
		t.Fatal(err)
	}
	ws := workspace.New(cfg.Service.WorkspacesDir)
	pub := publisher.New(forge, ws, githubapp.RepoRef{Owner: "synthetic", Name: "catalog"})
	recon := reconcile.New(reconcile.Options{Config: &cfg, Store: st, Forge: forge, WS: ws, Runner: fakePipelineRunner(), Publisher: pub})
	if err := runOnce(ctx, &cfg, st, forge, recon, true); err != nil {
		t.Fatal(err)
	}
	before, err := st.GetRepo(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := runOnce(ctx, &cfg, st, forge, recon, true); err != nil {
		t.Fatal(err)
	}
	runs, err := st.ListRuns(ctx, "demo", 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("unchanged stable rebuilt: %+v %v", runs, err)
	}
	cfg.LLM.Model = "changed-model"
	if err := runOnce(ctx, &cfg, st, forge, recon, true); err != nil {
		t.Fatal(err)
	}
	after, err := st.GetRepo(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	runs, err = st.ListRuns(ctx, "demo", 10)
	if err != nil || len(runs) != 2 || runs[0].State != model.StateStable {
		t.Fatalf("configuration drift did not publish a new stable: %+v %v", runs, err)
	}
	if after.LastStableSourceSHA != before.LastStableSourceSHA || after.LastStableFingerprint == before.LastStableFingerprint || after.LastStableBundleDigest == before.LastStableBundleDigest {
		t.Fatalf("wrong same-SHA rebuild identity: before=%+v after=%+v", before, after)
	}
}

type onceBranchForge struct {
	*fakeforge.FakeForge
	sha string
}

func (f *onceBranchForge) GetBranchSHA(context.Context, string, string, string) (string, error) {
	return f.sha, nil
}

func onceSchedulingFixture(t *testing.T, sha string) (*dkconfig.Config, *store.Store, *onceBranchForge, *reconcile.Reconciler) {
	t.Helper()
	ctx := context.Background()
	cfg := fakeTestConfig(t.TempDir())
	st, err := store.Open(filepath.Join(t.TempDir(), "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.UpsertRepo(ctx, toManagedRepo(cfg.Repos[0])); err != nil {
		t.Fatal(err)
	}
	forge := &onceBranchForge{FakeForge: fakeforge.New(), sha: sha}
	recon := reconcile.New(reconcile.Options{Config: &cfg, Store: st, Forge: forge})
	return &cfg, st, forge, recon
}

// 改用构建身份判定后也不能每次 --once 另建 run 来绕过持久化的失败上限。
// A terminal source/config identity stays suppressed until explicit recovery or drift.
func TestOnceTerminalFailureDoesNotStartAnotherRun(t *testing.T) {
	t.Parallel()
	for _, state := range []model.RunState{model.StateFailedPermanent, model.StateBlocked, model.StateStale} {
		t.Run(string(state), func(t *testing.T) {
			ctx := context.Background()
			sha := strings.Repeat("a", 40)
			cfg, st, forge, recon := onceSchedulingFixture(t, sha)
			if err := st.CreateRun(ctx, model.Run{RunID: "exhausted", RepoID: "demo", State: state, DesiredSourceSHA: sha, BuilderFingerprint: recon.CurrentFingerprintHex()}); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if err := runOnce(ctx, cfg, st, forge, recon, false); err != nil {
					t.Fatal(err)
				}
			}
			runs, err := st.ListRuns(ctx, "demo", 10)
			if err != nil || len(runs) != 1 || runs[0].State != state {
				t.Fatalf("terminal identity rebuilt automatically: %+v %v", runs, err)
			}
		})
	}
}

// 空远端读取以前与空 LastSeen 相等而退出 0；不能当作 no-op 或真实来源。
// Empty source reads fail before creating or advancing a run.
func TestOnceEmptyBranchSHAFailsWithoutStateMutation(t *testing.T) {
	t.Parallel()
	for name, sha := range map[string]string{"empty": "", "whitespace": " \t\n"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			cfg, st, forge, recon := onceSchedulingFixture(t, sha)
			if err := runOnce(ctx, cfg, st, forge, recon, false); err == nil {
				t.Fatal("missing source fact succeeded")
			}
			runs, err := st.ListRuns(ctx, "demo", 10)
			if err != nil || len(runs) != 0 {
				t.Fatalf("invalid source created a run: %+v %v", runs, err)
			}
			repo, err := st.GetRepo(ctx, "demo")
			if err != nil || repo.LastSeenSourceSHA != "" || repo.LastStableSourceSHA != "" {
				t.Fatalf("invalid source changed source state: %+v %v", repo, err)
			}
		})
	}
}
