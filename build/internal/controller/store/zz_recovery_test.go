package store

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/controller/model"
)

func recoveryStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "controller.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.UpsertRepo(context.Background(), model.ManagedRepo{ID: "repo", GitHubOwner: "owner", GitHubName: "repo", Branch: "main", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	return s, path
}

func TestRetryCheckpointAndAttemptsSurviveReopen(t *testing.T) {
	t.Parallel()
	s, path := recoveryStore(t)
	ctx := context.Background()
	if err := s.CreateRun(ctx, model.Run{RunID: "run", RepoID: "repo", State: model.StateAwaitSourcePR}); err != nil {
		t.Fatal(err)
	}
	// 原先只修改内存 Attempt，等待 PR 的网络故障还会从 FetchSource 重建。
	// A persisted checkpoint must resume the existing PR without resetting candidate identity.
	for attempt := 0; attempt <= 10; attempt++ {
		if err := s.FailRunRetryable(ctx, "run", model.StateAwaitSourcePR, "network", "outage"); err != nil {
			t.Fatal(err)
		}
		before, err := s.GetRun(ctx, "run")
		if err != nil {
			t.Fatal(err)
		}
		if before.Attempt != attempt || before.RetryState != model.StateAwaitSourcePR {
			t.Fatalf("checkpoint=%+v", before)
		}
		expected := time.Minute
		for i := 0; i < attempt && expected < 30*time.Minute; i++ {
			expected *= 2
		}
		if expected > 30*time.Minute {
			expected = 30 * time.Minute
		}
		if wait := time.Until(before.NextRetryAt); wait > expected || wait < expected-time.Second {
			t.Fatalf("backoff %s, want %s", wait, expected)
		}
		if attempt < 10 {
			if _, advanced, err := s.RetryRun(ctx, "run", 10); err != nil || advanced {
				t.Fatalf("early retry advanced=%t err=%v", advanced, err)
			}
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		s, err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if err := s.SetRunError(ctx, "run", "network", "outage", time.Now().Add(-time.Minute)); err != nil {
			t.Fatal(err)
		}
		resumed, advanced, err := s.RetryRun(ctx, "run", 10)
		if err != nil || !advanced {
			t.Fatalf("retry advanced=%t err=%v", advanced, err)
		}
		if attempt == 10 {
			if resumed.State != model.StateFailedPermanent || resumed.Attempt != 10 {
				t.Fatalf("not exhausted: %+v", resumed)
			}
			break
		}
		if resumed.State != model.StateAwaitSourcePR || resumed.Attempt != attempt+1 {
			t.Fatalf("lost checkpoint or count: %+v", resumed)
		}
	}
}

func TestRetryCheckpointWriteIsAtomic(t *testing.T) {
	t.Parallel()
	s, _ := recoveryStore(t)
	ctx := context.Background()
	if err := s.CreateRun(ctx, model.Run{RunID: "run", RepoID: "repo", State: model.StateAwaitCatalogPR}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER fail_run BEFORE UPDATE ON runs BEGIN SELECT RAISE(ABORT,'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.FailRunRetryable(ctx, "run", model.StateAwaitCatalogPR, "network", "outage"); err == nil {
		t.Fatal("write fault swallowed")
	}
	r, err := s.GetRun(ctx, "run")
	if err != nil {
		t.Fatal(err)
	}
	if r.State != model.StateAwaitCatalogPR || r.RetryState != "" || r.ErrorCode != "" || !r.NextRetryAt.IsZero() {
		t.Fatalf("partial failure checkpoint: %+v", r)
	}
}

func TestConcurrentLeaseAcquisitionAndFencedWrites(t *testing.T) {
	t.Parallel()
	s, path := recoveryStore(t)
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var journal string
	var timeout int
	if err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`PRAGMA busy_timeout`).Scan(&timeout); err != nil {
		t.Fatal(err)
	}
	if journal != "wal" || timeout != 5000 {
		t.Fatalf("unsafe connection journal=%s timeout=%d", journal, timeout)
	}
	ctx := context.Background()
	var wins atomic.Int32
	var wg sync.WaitGroup
	for _, c := range []*Store{s, other} {
		wg.Add(1)
		go func(c *Store) {
			defer wg.Done()
			won, err := c.AcquireLease(ctx, "repo", time.Now().String(), time.Second)
			if err != nil {
				t.Error(err)
			}
			if won {
				wins.Add(1)
			}
		}(c)
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("lease winners=%d", wins.Load())
	}
	if _, err := s.db.Exec(`DELETE FROM repo_leases`); err != nil {
		t.Fatal(err)
	}
	if won, err := s.AcquireLease(ctx, "repo", "old", time.Second); err != nil || !won {
		t.Fatalf("acquire: %t %v", won, err)
	}
	if _, err := s.db.Exec(`UPDATE repo_leases SET expires_at=? WHERE repo_id='repo'`, time.Now().Add(-time.Second).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := s.RenewLease(ctx, "repo", "old", time.Second); err == nil {
		t.Fatal("expired owner resurrected")
	}
	if won, err := other.AcquireLease(ctx, "repo", "new", time.Second); err != nil || !won {
		t.Fatalf("takeover: %t %v", won, err)
	}
	// 旧执行即使忽略取消也不能覆盖 source/stable 元数据。
	// Ownership must be checked inside the transaction that mutates metadata.
	if err := s.UpdateRepoSeen(WithRepoLease(ctx, "repo", "old"), "repo", "wrong-sha"); err == nil {
		t.Fatal("stale execution wrote metadata")
	}
	repo, err := s.GetRepo(ctx, "repo")
	if err != nil {
		t.Fatal(err)
	}
	if repo.LastSeenSourceSHA != "" {
		t.Fatal("metadata changed")
	}
	if err := s.ReleaseLease(ctx, "repo", "old"); err != nil {
		t.Fatal(err)
	}
	if err := other.RenewLease(ctx, "repo", "new", time.Second); err != nil {
		t.Fatal("old owner released successor:", err)
	}
}

func TestEffectIdentityConflictIsRejected(t *testing.T) {
	t.Parallel()
	s, _ := recoveryStore(t)
	ctx := context.Background()
	effect := model.ExternalEffect{EffectKey: "effect", EffectType: "push", TargetRepo: "owner/repo", RequestFingerprint: "digest"}
	if _, _, err := s.ClaimEffect(ctx, effect); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkEffectApplied(ctx, effect.EffectKey, "123", `{"ok":true}`); err != nil {
		t.Fatal(err)
	}
	effect.RequestFingerprint = "different"
	if _, _, err := s.ClaimEffect(ctx, effect); err == nil {
		t.Fatal("conflicting request reused existing receipt")
	}
	saved, err := s.GetEffect(ctx, "effect")
	if err != nil || saved == nil || saved.ExternalID != "123" || saved.ResponseSummary != `{"ok":true}` {
		t.Fatalf("receipt=%+v err=%v", saved, err)
	}
}

func TestVersionTwoStoreMigratesCheckpointWithoutLosingRun(t *testing.T) {
	t.Parallel()
	s, path := recoveryStore(t)
	ctx := context.Background()
	if err := s.CreateRun(ctx, model.Run{RunID: "old", RepoID: "repo", State: model.StateFailedRetryable, Attempt: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`ALTER TABLE runs DROP COLUMN retry_state; DELETE FROM schema_version; INSERT INTO schema_version VALUES (2)`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	run, err := migrated.GetRun(ctx, "old")
	if err != nil || run.Attempt != 3 || run.RetryState != "" {
		t.Fatalf("migration lost old row: %+v err=%v", run, err)
	}
	resumed, advanced, err := migrated.RetryRun(ctx, "old", 10)
	if err != nil || !advanced || resumed.State != model.StateFetchSource || resumed.Attempt != 4 {
		t.Fatalf("legacy retry failed: %+v advanced=%t err=%v", resumed, advanced, err)
	}
}

func TestSynchronousLeaseGuardRejectsTakeoverBeforeHeartbeat(t *testing.T) {
	t.Parallel()
	s, _ := recoveryStore(t)
	ctx := context.Background()
	if won, err := s.AcquireLease(ctx, "repo", "owner", time.Minute); err != nil || !won {
		t.Fatalf("acquire=%t err=%v", won, err)
	}
	owned := s.WithRepoLease(ctx, "repo", "owner")
	if err := CheckLease(owned); err != nil {
		t.Fatal(err)
	}
	// 凭证获取期间可丢失租约；下一次远端/文件写入必须同步发现，而不是等心跳。
	// Detect ownership loss at action boundaries before the asynchronous heartbeat runs.
	if _, err := s.db.Exec(`UPDATE repo_leases SET holder_id='successor' WHERE repo_id='repo'`); err != nil {
		t.Fatal(err)
	}
	if err := CheckLease(owned); err == nil {
		t.Fatal("lost ownership passed synchronous guard")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := CheckLease(cancelled); err == nil {
		t.Fatal("cancelled standalone action passed guard")
	}
}
