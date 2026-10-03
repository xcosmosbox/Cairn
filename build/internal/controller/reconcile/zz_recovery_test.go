package reconcile

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/controller/fakeforge"
	"github.com/xcosmosbox/cairn/build/internal/controller/githubapp"
	"github.com/xcosmosbox/cairn/build/internal/controller/model"
	"github.com/xcosmosbox/cairn/build/internal/controller/store"
	"github.com/xcosmosbox/cairn/core/dkconfig"
)

type recoveryForge struct {
	*fakeforge.FakeForge
	branch      func(context.Context) (string, error)
	prErr       error
	prCalls     atomic.Int32
	branchCalls atomic.Int32
}

func (f *recoveryForge) GetBranchSHA(ctx context.Context, owner, repo, branch string) (string, error) {
	f.branchCalls.Add(1)
	if f.branch != nil {
		return f.branch(ctx)
	}
	return strings.Repeat("a", 40), nil
}
func (f *recoveryForge) GetPullRequest(context.Context, string, string, int) (githubapp.PullRequest, error) {
	f.prCalls.Add(1)
	return githubapp.PullRequest{State: "open", HeadSHA: strings.Repeat("b", 40)}, f.prErr
}

func recoverySetup(t *testing.T, state model.RunState, f githubapp.Forge) (*Reconciler, *store.Store) {
	t.Helper()
	root := t.TempDir()
	s, err := store.Open(filepath.Join(root, "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	cfg := &dkconfig.Config{Service: dkconfig.ServiceConfig{CacheDir: filepath.Join(root, "cache")}, Repos: []dkconfig.RepoConfig{{ID: "repo", Owner: "owner", Name: "repo", Branch: "main", KGGroup: "repo"}}}
	if err := s.UpsertRepo(context.Background(), model.ManagedRepo{ID: "repo", GitHubOwner: "owner", GitHubName: "repo", Branch: "main", KGGroup: "repo", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRun(context.Background(), model.Run{RunID: "run", RepoID: "repo", State: state, DesiredSourceSHA: strings.Repeat("a", 40)}); err != nil {
		t.Fatal(err)
	}
	return New(Options{Config: cfg, Store: s, Forge: f}), s
}
func recoveryPR(t *testing.T, s *store.Store, kind model.PRKind) {
	t.Helper()
	if err := s.UpsertPR(context.Background(), model.PullRequest{RunID: "run", Kind: kind, Owner: "owner", Repo: "repo", Number: 1, HeadSHA: strings.Repeat("b", 40), Status: model.PRStatusOpen}); err != nil {
		t.Fatal(err)
	}
}

func TestPRNetworkFailuresResumeWaitingStage(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		state model.RunState
		kind  model.PRKind
	}{{model.StateAwaitSourcePR, model.PRKindSourceWriteback}, {model.StateAwaitCatalogPR, model.PRKindCatalogPublish}} {
		t.Run(string(test.state), func(t *testing.T) {
			f := &recoveryForge{FakeForge: fakeforge.New(), prErr: errors.New("network outage")}
			r, s := recoverySetup(t, test.state, f)
			recoveryPR(t, s, test.kind)
			result, err := r.Step(context.Background(), "run")
			if err != nil {
				t.Fatal(err)
			}
			saved, err := s.GetRun(context.Background(), "run")
			if err != nil {
				t.Fatal(err)
			}
			if !result.Advanced || saved.State != model.StateFailedRetryable || saved.RetryState != test.state {
				t.Fatalf("result=%+v checkpoint=%+v", result, saved)
			}
			if result, err := r.Step(context.Background(), "run"); err != nil || result.Advanced || f.prCalls.Load() != 1 {
				t.Fatalf("backoff bypassed: %+v err=%v calls=%d", result, err, f.prCalls.Load())
			}
			if err := s.SetRunError(context.Background(), "run", "network", "outage", time.Now().Add(-time.Second)); err != nil {
				t.Fatal(err)
			}
			result, err = r.Step(context.Background(), "run")
			if err != nil || result.To != test.state || f.branchCalls.Load() != 0 {
				t.Fatalf("retry rebuilt instead of resumed: %+v err=%v branchcalls=%d", result, err, f.branchCalls.Load())
			}
		})
	}
}

func TestBranchReadFailureDoesNotBecomeStale(t *testing.T) {
	t.Parallel()
	f := &recoveryForge{FakeForge: fakeforge.New(), branch: func(context.Context) (string, error) { return "", errors.New("branch outage") }}
	r, s := recoverySetup(t, model.StateAwaitSourcePR, f)
	recoveryPR(t, s, model.PRKindSourceWriteback)
	result, err := r.Step(context.Background(), "run")
	if err != nil {
		t.Fatal(err)
	}
	saved, err := s.GetRun(context.Background(), "run")
	if err != nil {
		t.Fatal(err)
	}
	if result.To != model.StateFailedRetryable || saved.State != result.To || saved.RetryState != model.StateAwaitSourcePR {
		t.Fatalf("branch outage mishandled: result=%+v run=%+v", result, saved)
	}
}

func TestFailedTransitionDoesNotClaimProgress(t *testing.T) {
	t.Parallel()
	r, s := recoverySetup(t, model.StateIdle, fakeforge.New())
	if _, err := s.DB().Exec(`CREATE TRIGGER fail_transition BEFORE UPDATE ON runs BEGIN SELECT RAISE(ABORT,'injected transition failure'); END`); err != nil {
		t.Fatal(err)
	}
	result, err := r.Step(context.Background(), "run")
	if err == nil || result.Advanced {
		t.Fatalf("write error swallowed: result=%+v err=%v", result, err)
	}
	saved, err := s.GetRun(context.Background(), "run")
	if err != nil {
		t.Fatal(err)
	}
	if saved.State != model.StateIdle {
		t.Fatalf("state changed: %+v", saved)
	}
}

func TestSameInstanceStepLeaseContention(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	finish := make(chan struct{})
	f := &recoveryForge{FakeForge: fakeforge.New(), branch: func(ctx context.Context) (string, error) {
		close(started)
		select {
		case <-finish:
			return strings.Repeat("a", 40), nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}}
	r, _ := recoverySetup(t, model.StateFetchSource, f)
	done := make(chan error, 1)
	go func() { _, err := r.Step(context.Background(), "run"); done <- err }()
	<-started
	// 同一 Reconciler 的两个 goroutine 也必须互斥，不能共用 holder 绕过租约。
	// Execution tokens prevent reentrant ownership inside one instance.
	result, err := r.Step(context.Background(), "run")
	if err != nil || result.Advanced || f.branchCalls.Load() != 1 {
		t.Fatalf("concurrent action executed: result=%+v err=%v calls=%d", result, err, f.branchCalls.Load())
	}
	close(finish)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// Wait for persisted renewal evidence instead of sleeping past a subsecond lease.
// CI may legitimately deschedule a 120 ms lease: expiry is then fail-closed, not a renewal bug.
func waitForLeaseRenewal(t *testing.T, ctx context.Context, s *store.Store) {
	t.Helper()
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	for {
		var renewals int
		if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM test_lease_heartbeats`).Scan(&renewals); err != nil {
			t.Fatal(err)
		}
		if renewals > 0 {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("heartbeat never committed: %v", ctx.Err())
		case <-poll.C:
		}
	}
}

func controlledLeaseAction(t *testing.T) (*store.Store, context.Context, chan<- time.Time, <-chan error) {
	t.Helper()
	started := make(chan struct{})
	f := &recoveryForge{FakeForge: fakeforge.New(), branch: func(ctx context.Context) (string, error) { close(started); <-ctx.Done(); return "", ctx.Err() }}
	r, s := recoverySetup(t, model.StateFetchSource, f)
	// Production-sized TTL keeps expiration separate from CI goroutine scheduling.
	// Force expiry explicitly in its own test rather than assuming a wall-clock delay.
	if _, err := s.DB().Exec(`CREATE TABLE test_lease_heartbeats (holder TEXT NOT NULL);
 CREATE TRIGGER test_lease_heartbeat AFTER UPDATE OF heartbeat_at ON repo_leases
 BEGIN INSERT INTO test_lease_heartbeats VALUES (NEW.holder_id); END`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	ticks := make(chan time.Time)
	done := make(chan error, 1)
	finished := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(10 * time.Second):
			t.Error("lease action did not shut down")
		}
	})
	go func() {
		defer close(finished)
		_, err := r.withRepoLeaseTicks(ctx, "repo", func(ownedCtx context.Context) error {
			result, err := r.step(ownedCtx, "run")
			if err == nil || result.Advanced {
				return fmt.Errorf("lost lease action reported successful progress: result=%+v error=%v", result, err)
			}
			return err
		}, ticks)
		done <- err
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("lease action never started")
	}
	return s, ctx, ticks, done
}

func TestLeaseRenewsAndLossCancelsAction(t *testing.T) {
	t.Parallel()
	s, ctx, ticks, done := controlledLeaseAction(t)
	// Exercise the complete acquisition/heartbeat/action/cancellation/release wrapper with real SQL.
	// The injected tick only chooses when the real renewal happens; lease timestamps stay real.
	select {
	case ticks <- time.Time{}:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	waitForLeaseRenewal(t, ctx, s)
	if won, err := s.AcquireLease(ctx, "repo", "contender", time.Minute); err != nil || won {
		t.Fatalf("renewed lease was not exclusive: won=%t err=%v", won, err)
	}
	if _, err := s.DB().ExecContext(ctx, `UPDATE repo_leases SET holder_id='successor' WHERE repo_id='repo'`); err != nil {
		t.Fatal(err)
	}
	select {
	case ticks <- time.Time{}:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "lease heartbeat") {
			t.Fatalf("lease loss did not cause cancellation: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("lease loss did not cancel action")
	}
	saved, err := s.GetRun(ctx, "run")
	if err != nil {
		t.Fatal(err)
	}
	if saved.State != model.StateFetchSource || saved.ErrorCode != "" {
		t.Fatalf("lost owner wrote recovery state: %+v", saved)
	}
	var owner string
	if err := s.DB().QueryRowContext(ctx, `SELECT holder_id FROM repo_leases WHERE repo_id='repo'`).Scan(&owner); err != nil || owner != "successor" {
		t.Fatalf("cleanup deleted successor: owner=%s err=%v", owner, err)
	}
}

func TestExpiredLeaseHeartbeatCancelsWithoutRevival(t *testing.T) {
	t.Parallel()
	s, ctx, ticks, done := controlledLeaseAction(t)
	if _, err := s.DB().ExecContext(ctx, `UPDATE repo_leases SET expires_at=? WHERE repo_id='repo'`, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	select {
	case ticks <- time.Time{}:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "lease heartbeat") {
			t.Fatalf("expired lease did not cancel: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("expired lease action did not finish")
	}
	var leases, renewals int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM repo_leases`).Scan(&leases); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM test_lease_heartbeats`).Scan(&renewals); err != nil {
		t.Fatal(err)
	}
	if leases != 0 || renewals != 0 {
		t.Fatalf("expired owner revived or leaked lease: leases=%d renewals=%d", leases, renewals)
	}
	saved, err := s.GetRun(ctx, "run")
	if err != nil || saved.State != model.StateFetchSource || saved.ErrorCode != "" {
		t.Fatalf("expired owner mutated state: %+v error=%v", saved, err)
	}
}

func TestLostLeaseCannotWriteSourceMetadata(t *testing.T) {
	t.Parallel()
	f := &recoveryForge{FakeForge: fakeforge.New()}
	r, s := recoverySetup(t, model.StateFetchSource, f)
	f.branch = func(context.Context) (string, error) {
		_, err := s.DB().Exec(`DELETE FROM repo_leases WHERE repo_id='repo'`)
		return strings.Repeat("c", 40), err
	}
	result, err := r.Step(context.Background(), "run")
	if err == nil || result.Advanced {
		t.Fatalf("lease loss accepted: %+v err=%v", result, err)
	}
	repo, err := s.GetRepo(context.Background(), "repo")
	if err != nil {
		t.Fatal(err)
	}
	if repo.LastSeenSourceSHA != "" {
		t.Fatalf("stale caller wrote source metadata: %+v", repo)
	}
}

func TestDefaultHolderUniqueAndCreateRunDeduplicated(t *testing.T) {
	t.Parallel()
	r, s := recoverySetup(t, model.StateStable, fakeforge.New())
	other := New(Options{Config: r.cfg, Store: s, Forge: r.forge})
	if r.holderID == other.holderID {
		t.Fatal("default holder IDs are shared")
	}
	first, err := r.CreateRun(context.Background(), "repo")
	if err != nil {
		t.Fatal(err)
	}
	second, err := other.CreateRun(context.Background(), "repo")
	if err != nil || first != second {
		t.Fatalf("duplicate active runs: %s %s err=%v", first, second, err)
	}
}

func TestManualRecoveryPreservesCheckpointAndReplacesClosedProposal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r, s := recoverySetup(t, model.StateAwaitSourcePR, fakeforge.New())
	recoveryPR(t, s, model.PRKindSourceWriteback)
	if err := s.FailRunRetryable(ctx, "run", model.StateAwaitSourcePR, "network", "outage"); err != nil {
		t.Fatal(err)
	}
	resumedID, err := r.RequestRetry(ctx, "run")
	if err != nil || resumedID != "run" {
		t.Fatalf("manual retry=%s err=%v", resumedID, err)
	}
	result, err := r.Step(ctx, "run")
	if err != nil || result.To != model.StateAwaitSourcePR {
		t.Fatalf("manual retry lost PR stage: %+v err=%v", result, err)
	}
	if err := s.TransitionRun(ctx, "run", model.StateBlocked, "closed source PR"); err != nil {
		t.Fatal(err)
	}
	replacement, err := r.RequestRetry(ctx, "run")
	if err != nil || replacement == "run" {
		t.Fatalf("closed proposal reused: %s err=%v", replacement, err)
	}
	original, err := s.GetRun(ctx, "run")
	if err != nil || original.State != model.StateBlocked {
		t.Fatalf("blocked history mutated: %+v err=%v", original, err)
	}
	created, err := s.GetRun(ctx, replacement)
	if err != nil || created.State != model.StateIdle {
		t.Fatalf("replacement=%+v err=%v", created, err)
	}
}

func TestCallerCancellationReleasesLeaseWithoutStateWrite(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	f := &recoveryForge{FakeForge: fakeforge.New(), branch: func(ctx context.Context) (string, error) { close(started); <-ctx.Done(); return "", ctx.Err() }}
	r, s := recoverySetup(t, model.StateFetchSource, f)
	done := make(chan error, 1)
	go func() { _, err := r.Step(ctx, "run"); done <- err }()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation lost: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled Step did not finish")
	}
	var leases int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM repo_leases`).Scan(&leases); err != nil || leases != 0 {
		t.Fatalf("lease leaked: count=%d err=%v", leases, err)
	}
	saved, err := s.GetRun(context.Background(), "run")
	if err != nil || saved.State != model.StateFetchSource {
		t.Fatalf("cancelled action changed state: %+v err=%v", saved, err)
	}
}

func TestBuildIdentityDriftStopsBeforeExternalAction(t *testing.T) {
	t.Parallel()
	f := &recoveryForge{FakeForge: fakeforge.New()}
	r, s := recoverySetup(t, model.StateAwaitSourcePR, f)
	recoveryPR(t, s, model.PRKindSourceWriteback)
	if err := s.UpdateRunFingerprint(context.Background(), "run", r.CurrentFingerprintHex()); err != nil {
		t.Fatal(err)
	}
	r.cfg.LLM.Model = "changed-model"
	result, err := r.Step(context.Background(), "run")
	if err != nil || result.To != model.StateStale || f.prCalls.Load() != 0 || f.branchCalls.Load() != 0 {
		t.Fatalf("old semantics performed remote action: %+v err=%v pr=%d branch=%d", result, err, f.prCalls.Load(), f.branchCalls.Load())
	}
}
