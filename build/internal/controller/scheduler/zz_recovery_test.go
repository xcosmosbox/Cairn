package scheduler

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/controller/fakeforge"
	"github.com/xcosmosbox/cairn/build/internal/controller/model"
	"github.com/xcosmosbox/cairn/build/internal/controller/reconcile"
	"github.com/xcosmosbox/cairn/build/internal/controller/store"
	"github.com/xcosmosbox/cairn/core/dkconfig"
)

type schedulerForge struct {
	*fakeforge.FakeForge
	sha string
}

func (f *schedulerForge) GetBranchSHA(context.Context, string, string, string) (string, error) {
	return f.sha, nil
}

func schedulingFixture(t *testing.T, state model.RunState, stable bool) (*Scheduler, *store.Store, *dkconfig.Config) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	cfg := &dkconfig.Config{Repos: []dkconfig.RepoConfig{{ID: "repo", Owner: "owner", Name: "repo", Branch: "main", KGGroup: "repo"}}}
	f := &schedulerForge{FakeForge: fakeforge.New(), sha: strings.Repeat("a", 40)}
	r := reconcile.New(reconcile.Options{Config: cfg, Store: s, Forge: f})
	repo := model.ManagedRepo{ID: "repo", GitHubOwner: "owner", GitHubName: "repo", Branch: "main", KGGroup: "repo", Enabled: true, LastSeenSourceSHA: f.sha}
	if stable {
		repo.LastStableSourceSHA = f.sha
		repo.LastStableBundleDigest = "bundle"
		repo.LastStableFingerprint = r.CurrentFingerprintHex()
	}
	if err := s.UpsertRepo(ctx, repo); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRun(ctx, model.Run{RunID: "old", RepoID: "repo", State: state, DesiredSourceSHA: f.sha, BuilderFingerprint: r.CurrentFingerprintHex()}); err != nil {
		t.Fatal(err)
	}
	return New(cfg, s, f, r), s, cfg
}

func TestAutomaticReconcileHonorsRetryExhaustion(t *testing.T) {
	t.Parallel()
	for _, state := range []model.RunState{model.StateFailedPermanent, model.StateBlocked, model.StateStale} {
		t.Run(string(state), func(t *testing.T) {
			scheduler, s, _ := schedulingFixture(t, state, false)
			// 无 stable 的永久失败不能每轮另建任务绕过持久化的重试上限。
			// Failure bounds apply across scheduler polls, not only inside one run.
			scheduler.tick(context.Background())
			scheduler.tick(context.Background())
			runs, err := s.ListRuns(context.Background(), "repo", 10)
			if err != nil || len(runs) != 1 {
				t.Fatalf("terminal failure rebuilt automatically: runs=%+v err=%v", runs, err)
			}
		})
	}
}

func TestConfigurationDriftWithSameSHAIsScheduled(t *testing.T) {
	t.Parallel()
	scheduler, s, cfg := schedulingFixture(t, model.StateStable, true)
	scheduler.tick(context.Background())
	runs, err := s.ListRuns(context.Background(), "repo", 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("unchanged stable rebuilt: %+v %v", runs, err)
	}
	cfg.LLM.Model = "changed-model"
	scheduler.tick(context.Background())
	runs, err = s.ListRuns(context.Background(), "repo", 10)
	if err != nil || len(runs) != 2 {
		t.Fatalf("same SHA config drift ignored: %+v %v", runs, err)
	}
}

func TestSchedulerMissingSourceFactDoesNotCreateTask(t *testing.T) {
	t.Parallel()
	scheduler, s, _ := schedulingFixture(t, model.StateStable, true)
	scheduler.forge.(*schedulerForge).sha = ""
	scheduler.tick(context.Background())
	runs, err := s.ListRuns(context.Background(), "repo", 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("missing source created task: runs=%+v err=%v", runs, err)
	}
}
