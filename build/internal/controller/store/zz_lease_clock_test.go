package store

import (
	"context"
	"database/sql"
	"math"
	"testing"
	"time"
)

func waitForStoreCondition(t *testing.T, ctx context.Context, condition func() (bool, error)) {
	t.Helper()
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	for {
		ok, err := condition()
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("condition did not complete: %v", ctx.Err())
		case <-poll.C:
		}
	}
}

func heldStoreConnection(t *testing.T, ctx context.Context, s *Store) *sql.Conn {
	t.Helper()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func TestQueuedRenewalCannotReviveExpiredHolder(t *testing.T) {
	t.Parallel()
	s, _ := recoveryStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if won, err := s.AcquireLease(ctx, "repo", "owner", time.Minute); err != nil || !won {
		t.Fatalf("acquire=%t error=%v", won, err)
	}
	held := heldStoreConnection(t, ctx, s)
	before := s.db.Stats().WaitCount
	done := make(chan error, 1)
	go func() { done <- s.RenewLease(ctx, "repo", "owner", time.Minute) }()
	// WaitCount confirms RenewLease entered the pool queue, including its old captured-time point.
	waitForStoreCondition(t, ctx, func() (bool, error) { return s.db.Stats().WaitCount > before, nil })
	if _, err := held.ExecContext(ctx, `UPDATE repo_leases SET expires_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','+0.010 seconds') WHERE repo_id='repo'`); err != nil {
		t.Fatal(err)
	}
	// Observe actual expiry using the same SQL clock before releasing the queued operation.
	waitForStoreCondition(t, ctx, func() (bool, error) {
		var expired int
		err := held.QueryRowContext(ctx, `SELECT julianday(expires_at)<=julianday('now') FROM repo_leases WHERE repo_id='repo'`).Scan(&expired)
		return expired == 1, err
	})
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("queued renewal revived an expired lease")
		}
	case <-ctx.Done():
		t.Fatal("queued renewal never completed")
	}
	var expired int
	if err := s.db.QueryRowContext(ctx, `SELECT julianday(expires_at)<=julianday('now') FROM repo_leases WHERE repo_id='repo'`).Scan(&expired); err != nil || expired != 1 {
		t.Fatalf("expired holder gained new validity: expired=%d error=%v", expired, err)
	}
}

func TestQueuedAcquisitionGetsFullTTLAtExecution(t *testing.T) {
	t.Parallel()
	s, _ := recoveryStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	held := heldStoreConnection(t, ctx, s)
	before := s.db.Stats().WaitCount
	type result struct {
		won bool
		err error
	}
	done := make(chan result, 1)
	const ttl = 50 * time.Millisecond
	go func() { won, err := s.AcquireLease(ctx, "repo", "owner", ttl); done <- result{won, err} }()
	waitForStoreCondition(t, ctx, func() (bool, error) { return s.db.Stats().WaitCount > before, nil })
	var releaseAfter string
	if err := held.QueryRowContext(ctx, `SELECT strftime('%Y-%m-%dT%H:%M:%fZ','now','+0.100 seconds')`).Scan(&releaseAfter); err != nil {
		t.Fatal(err)
	}
	// This queue delay crosses the requested TTL; a captured grant time would already be expired.
	waitForStoreCondition(t, ctx, func() (bool, error) {
		var reached int
		err := held.QueryRowContext(ctx, `SELECT julianday('now')>julianday(?)`, releaseAfter).Scan(&reached)
		return reached == 1, err
	})
	var releasedAt string
	if err := held.QueryRowContext(ctx, `SELECT strftime('%Y-%m-%dT%H:%M:%fZ','now')`).Scan(&releasedAt); err != nil {
		t.Fatal(err)
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if result.err != nil || !result.won {
			t.Fatalf("acquisition=%t error=%v", result.won, result.err)
		}
	case <-ctx.Done():
		t.Fatal("queued acquisition did not complete")
	}
	var fresh int
	var grantedSeconds float64
	var acquired, expiry, heartbeat string
	if err := s.db.QueryRowContext(ctx, `SELECT julianday(acquired_at)>=julianday(?), (julianday(expires_at)-julianday(acquired_at))*86400, acquired_at,expires_at,heartbeat_at FROM repo_leases WHERE repo_id='repo'`, releasedAt).Scan(&fresh, &grantedSeconds, &acquired, &expiry, &heartbeat); err != nil {
		t.Fatal(err)
	}
	// Compare recorded execution facts, not whether a heavily loaded CI observer woke before expiry.
	if fresh != 1 || math.Abs(grantedSeconds-ttl.Seconds()) > 0.001 {
		t.Fatalf("queued grant used old time or shortened TTL: fresh=%d seconds=%.6f", fresh, grantedSeconds)
	}
	for _, timestamp := range []string{acquired, expiry, heartbeat} {
		parsed, err := time.Parse(time.RFC3339Nano, timestamp)
		if err != nil || parsed.Location() != time.UTC || len(timestamp) != 24 {
			t.Fatalf("lease timestamps must use UTC milliseconds: %q error=%v", timestamp, err)
		}
	}
}

func TestQueuedTransactionGuardChecksCurrentLease(t *testing.T) {
	t.Parallel()
	s, path := recoveryStore(t)
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if won, err := s.AcquireLease(ctx, "repo", "owner", time.Minute); err != nil || !won {
		t.Fatalf("acquire=%t error=%v", won, err)
	}
	tx, err := other.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	done := make(chan error, 1)
	go func() { done <- s.UpdateRepoSeen(s.WithRepoLease(ctx, "repo", "owner"), "repo", "stale-sha") }()
	// Immediate transactions own the write lock before checking the lease; wait until this one queues.
	waitForStoreCondition(t, ctx, func() (bool, error) { return s.db.Stats().InUse == 1, nil })
	if _, err := tx.ExecContext(ctx, `UPDATE repo_leases SET expires_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','-1 minute') WHERE repo_id='repo'`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("transaction queued past expiry wrote stale metadata")
		}
	case <-ctx.Done():
		t.Fatal("queued transaction did not complete")
	}
	repo, err := s.GetRepo(ctx, "repo")
	if err != nil || repo.LastSeenSourceSHA != "" {
		t.Fatalf("fencing failed: repo=%+v error=%v", repo, err)
	}
}

func TestLeaseTTLPrecisionAndValidation(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		ttl      time.Duration
		modifier string
	}{{time.Nanosecond, "+0.001 seconds"}, {1500 * time.Microsecond, "+0.002 seconds"}, {time.Minute, "+60.000 seconds"}, {time.Duration(1<<63 - 1), "+9223372036.855 seconds"}} {
		modifier, err := leaseTTLModifier(test.ttl)
		if err != nil || modifier != test.modifier {
			t.Fatalf("ttl=%v modifier=%s error=%v", test.ttl, modifier, err)
		}
	}
	s, _ := recoveryStore(t)
	for _, ttl := range []time.Duration{0, -time.Second} {
		if won, err := s.AcquireLease(context.Background(), "repo", "owner", ttl); err == nil || won {
			t.Fatalf("invalid acquisition ttl=%v won=%t error=%v", ttl, won, err)
		}
		if err := s.RenewLease(context.Background(), "repo", "owner", ttl); err == nil {
			t.Fatalf("invalid renewal ttl=%v", ttl)
		}
	}
}
