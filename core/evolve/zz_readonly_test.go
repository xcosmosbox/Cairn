package evolve

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

// A read-only observer previously migrated the source and its archived snapshots.
func TestRecordPreservesSourceSchemaAndBytes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "knowledge?#.db")
	db, err := storage.NewDB(storage.DBOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = storage.NewKBVersionRepo(db).Bump(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Conn().Exec("ALTER TABLE nodes DROP COLUMN file_slug"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Conn().Exec("PRAGMA user_version=4"); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "evolution")
	if _, err = Record(ctx, path, dir, "audit"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	now, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(before) != sha256.Sum256(after) || !info.ModTime().Equal(now.ModTime()) {
		t.Fatal("observer modified source KG")
	}
	manifest, err := ReadManifest(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Changesets) != 1 {
		t.Fatalf("changesets=%d", len(manifest.Changesets))
	}
	snap, err := storage.OpenReadOnly(filepath.Join(dir, "snapshots", manifest.Changesets[0].SnapshotFile))
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close()
	version, err := snap.SchemaVersion()
	if err != nil || version != 4 {
		t.Fatalf("snapshot was migrated: version=%d error=%v", version, err)
	}
	evPath := filepath.Join(dir, "evolution.db")
	evBefore, err := os.ReadFile(evPath)
	if err != nil {
		t.Fatal(err)
	}
	evInfo, err := os.Stat(evPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ReadManifest(ctx, dir); err != nil {
		t.Fatal(err)
	}
	evAfter, err := os.ReadFile(evPath)
	if err != nil {
		t.Fatal(err)
	}
	evNow, err := os.Stat(evPath)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(evBefore) != sha256.Sum256(evAfter) || !evInfo.ModTime().Equal(evNow.ModTime()) {
		t.Fatal("manifest reader modified evolution DB")
	}
}

// WAL pages must be observed from one immutable generation; copying the main file loses them.
func TestRecordArchivesCommittedWALAndUsesSnapshotVersion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "live.db")
	db, err := storage.NewDB(storage.DBOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Conn().Exec("PRAGMA wal_autocheckpoint=0"); err != nil {
		t.Fatal(err)
	}
	n := &dktypes.Node{ID: "11111111-1111-4111-8111-111111111111", Label: dktypes.LabelConcept, Name: "WAL knowledge", Domain: "engineering", Subdomain: "queues", Confidence: 1, Provenance: dktypes.ProvenanceExtraction}
	if err = storage.NewNodeRepo(db).Insert(ctx, n); err != nil {
		t.Fatal(err)
	}
	version, err := storage.NewKBVersionRepo(db).Bump(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path + "-wal"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "evolution")
	if _, err = Record(ctx, path, dir, "audit"); err != nil {
		t.Fatal(err)
	}
	manifest, err := ReadManifest(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Changesets) != 1 || len(manifest.Metrics) != 1 {
		t.Fatal("incomplete evolution record")
	}
	row := manifest.Changesets[0]
	if row.KBVersion != version || manifest.Metrics[0].KBVersion != version || manifest.Metrics[0].NodeCount != 1 {
		t.Fatalf("inconsistent snapshot metadata: %+v %+v", row, manifest.Metrics[0])
	}
	snap, err := storage.OpenReadOnly(filepath.Join(dir, "snapshots", row.SnapshotFile))
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close()
	var count int
	if err = snap.Conn().QueryRow("SELECT COUNT(*) FROM nodes WHERE id=?", n.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("committed WAL data lost: count=%d error=%v", count, err)
	}
}

func TestMissingKnowledgeIsNotCreated(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "missing.db")
	if _, err := Record(context.Background(), path, filepath.Join(root, "evolution"), "audit"); err == nil {
		t.Fatal("expected missing source error")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("observer created missing KG: %v", err)
	}
}

// 失败不遗留阻塞同一 seq 的快照，崩溃留下的孤儿文件也不会被覆盖。
// Failed captures clean up their own artifact; crash orphans cannot block retries.
func TestRecordFailureAndCrashOrphanDoNotBlockRetry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "knowledge.db")
	db, err := storage.NewDB(storage.DBOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "evolution")
	if _, err = Record(ctx, path, dir, "audit"); err != nil {
		t.Fatal(err)
	}
	m, err := ReadManifest(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(dir, "snapshots", m.Changesets[0].SnapshotFile)
	if err = os.Rename(parent, parent+".held"); err != nil {
		t.Fatal(err)
	}
	if _, err = Record(ctx, path, dir, "audit"); err == nil {
		t.Fatal("missing parent should fail after archiving")
	}
	files, err := os.ReadDir(filepath.Dir(parent))
	if err != nil || len(files) != 1 {
		t.Fatalf("failed capture leaked its artifact: files=%v error=%v", files, err)
	}
	if err = os.Rename(parent+".held", parent); err != nil {
		t.Fatal(err)
	}
	orphan, err := archiveSnapshot(ctx, path, filepath.Dir(parent), 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Record(ctx, path, dir, "audit"); err != nil {
		t.Fatalf("retry blocked by orphan: %v", err)
	}
	m, err = ReadManifest(ctx, dir)
	if err != nil || len(m.Changesets) != 2 || m.Changesets[1].SnapshotFile == orphan {
		t.Fatalf("retry did not record an independent artifact: manifest=%+v error=%v", m, err)
	}
	if _, err = os.Stat(filepath.Join(dir, "snapshots", orphan)); err != nil {
		t.Fatalf("retry removed a different attempt's artifact: %v", err)
	}
}

// 多连接捕获不能争抢 seq，时间轴与指标也不能跨越不同提交读取。
// Concurrent recorders serialize, and readers observe matching timeline and metrics.
func TestConcurrentRecordAndManifestStayConsistent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "knowledge.db")
	db, err := storage.NewDB(storage.DBOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "evolution")
	if _, err = Record(ctx, path, dir, "seed"); err != nil {
		t.Fatal(err)
	}
	const writers = 8
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := Record(ctx, path, dir, "concurrent")
			errs <- err
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	check := func() *Manifest {
		t.Helper()
		m, err := ReadManifest(ctx, dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(m.Changesets) != len(m.Metrics) {
			t.Fatalf("mixed commits: changesets=%d metrics=%d", len(m.Changesets), len(m.Metrics))
		}
		for i, c := range m.Changesets {
			if c.Seq != int64(i+1) || m.Metrics[i].Seq != c.Seq || m.Metrics[i].KBVersion != c.KBVersion {
				t.Fatalf("inconsistent lineage at index %d: %+v %+v", i, c, m.Metrics[i])
			}
			if _, err := os.Stat(filepath.Join(dir, "snapshots", c.SnapshotFile)); err != nil {
				t.Fatal(err)
			}
		}
		return m
	}
	for {
		check()
		select {
		case <-done:
			for i := 0; i < writers; i++ {
				if err := <-errs; err != nil {
					t.Fatal(err)
				}
			}
			if m := check(); len(m.Changesets) != writers+1 {
				t.Fatalf("lost records: %d", len(m.Changesets))
			}
			return
		default:
		}
	}
}

func recordOwnerTestKG(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, "knowledge.db")
	db, err := storage.NewDB(storage.DBOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}
func recordOwnerTestRefs(dir string) int {
	recordOwners.Lock()
	defer recordOwners.Unlock()
	if owner := recordOwners.byDir[dir]; owner != nil {
		return owner.refs
	}
	return 0
}
func recordOwnerTestQueued(t *testing.T, dir string, refs int) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		if recordOwnerTestRefs(dir) == refs {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("owner refs=%d, expected%d", recordOwnerTestRefs(dir), refs)
		case <-tick.C:
		}
	}
}
func recordOwnerTestResult(t *testing.T, ch <-chan error) {
	t.Helper()
	select {
	case err := <-ch:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("record did not finish after owner release")
	}
}

// 原失败机制：有限5s busy等待在同进程writer排队/持锁时耗尽；不能通过加超时或重跑Record隐藏。
// This controls the OLD BeginTx admission mechanism, not a pristine-old-Record end-to-end counterfactual.
func TestRecordOwnerOutwaitsUnchangedSQLiteBusyMechanism(t *testing.T) {
	// Package-level serialization keeps the barrier experiment distinct from the unchanged stress test.
	root := t.TempDir()
	kg := recordOwnerTestKG(t, root)
	dir, err := canonicalRecordDir(filepath.Join(root, "evolution"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	release, err := acquireRecordOwner(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	holder, err := openEvolutionDB(filepath.Join(dir, "evolution.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	oldStyle, err := openEvolutionDB(filepath.Join(dir, "evolution.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer oldStyle.Close()
	var busyMS int
	if err := oldStyle.QueryRow("PRAGMA busy_timeout").Scan(&busyMS); err != nil || busyMS != 5000 {
		t.Fatalf("unchanged busy_timeout=%d err=%v", busyMS, err)
	}
	tx, err := holder.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	heldSince := time.Now()
	queued := make(chan error, 1)
	go func() { _, err := Record(ctx, kg, dir, "public-second"); queued <- err }()
	recordOwnerTestQueued(t, dir, 2)
	// WAL manifest reads must not share the writer owner gate or wait for its transaction.
	reader := make(chan error, 1)
	go func() { _, err := ReadManifest(ctx, dir); reader <- err }()
	select {
	case err := <-reader:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("read-only manifest blocked by writer owner")
	}
	probeSince := time.Now()
	oldTx, oldErr := oldStyle.BeginTx(ctx, nil)
	probeWait := time.Since(probeSince)
	if oldErr == nil {
		oldTx.Rollback()
		t.Fatal("old admission mechanism unexpectedly acquired held IMMEDIATE transaction")
	}
	var sqliteError interface{ Code() int }
	if !errors.As(oldErr, &sqliteError) || sqliteError.Code()&255 != 5 {
		t.Fatalf("expected SQLITE_BUSY control, got%v", oldErr)
	}
	// Keep the barrier strictly longer than the unchanged wait, without a production/global test hook.
	if remaining := 5100*time.Millisecond - time.Since(heldSince); remaining > 0 {
		timer := time.NewTimer(remaining)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			t.Fatal(ctx.Err())
		}
	}
	select {
	case err := <-queued:
		t.Fatalf("public Record bypassed owner while held: %v", err)
	default:
	}
	barrierHeld := time.Since(heldSince)
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := holder.Close(); err != nil {
		t.Fatal(err)
	}
	if err := oldStyle.Close(); err != nil {
		t.Fatal(err)
	}
	// First owner executes the real full capture exactly once; only then can the public waiter enter.
	if _, err := recordOwned(ctx, kg, dir, "owned-first"); err != nil {
		t.Fatal(err)
	}
	release()
	recordOwnerTestResult(t, queued)
	manifest, err := ReadManifest(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Changesets) != 2 || len(manifest.Metrics) != 2 {
		t.Fatalf("expected two exactly-once commits, got%+v", manifest)
	}
	for i, c := range manifest.Changesets {
		if c.Seq != int64(i+1) || manifest.Metrics[i].Seq != c.Seq || manifest.Metrics[i].KBVersion != c.KBVersion {
			t.Fatal("timeline/metrics lineage diverged")
		}
		if _, err := os.Stat(filepath.Join(dir, "snapshots", c.SnapshotFile)); err != nil {
			t.Fatal(err)
		}
	}
	if manifest.Changesets[0].Tool != "owned-first" || manifest.Changesets[1].Tool != "public-second" || manifest.Changesets[0].SnapshotFile == manifest.Changesets[1].SnapshotFile {
		t.Fatal("owner ordering or attempt identity lost")
	}
	files, err := os.ReadDir(filepath.Join(dir, "snapshots"))
	if err != nil || len(files) != 2 {
		t.Fatalf("snapshot count%d err%v", len(files), err)
	}
	if recordOwnerTestRefs(dir) != 0 {
		t.Fatal("completed directory owner leaked")
	}
	t.Logf("old BeginTx control: busy_timeout=%dms SQLITE_BUSY after%s; IMMEDIATE barrier held%s; new capture commits=2", busyMS, probeWait, barrierHeld)
}

// 取消必须在任何DB/DDL/快照副作用前生效，不能把有限SQLite等待当作可取消的本地队列。
func TestRecordOwnerQueuedCancellationCreatesNoArtifacts(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	kg := recordOwnerTestKG(t, root)
	dir, err := canonicalRecordDir(filepath.Join(root, "evolution"))
	if err != nil {
		t.Fatal(err)
	}
	release, err := acquireRecordOwner(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := Record(ctx, kg, dir, "cancelled"); done <- err }()
	recordOwnerTestQueued(t, dir, 2)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("queued cancellation error=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("queued cancellation was not context-aware")
	}
	for _, name := range []string{"evolution.db", "snapshots"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("cancelled waiter created%s: %v", name, err)
		}
	}
	if recordOwnerTestRefs(dir) != 1 {
		t.Fatal("cancelled waiter leaked owner reference")
	}
	release()
	if recordOwnerTestRefs(dir) != 0 {
		t.Fatal("last release retained owner entry")
	}
	unused := filepath.Join(root, "cancelled-before-directory")
	if _, err := Record(ctx, kg, unused, "cancelled-before-entry"); !errors.Is(err, context.Canceled) {
		t.Fatal("already-cancelled context accepted")
	}
	if _, err := os.Stat(unused); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("already-cancelled context created directory")
	}
}

// 别名必须归同一个owner，而另一个真实目录和只读manifest不得被全局互斥阻塞。
func TestRecordOwnerSymlinkAliasesShareGateAndDirectoriesStayIndependent(t *testing.T) {
	root := t.TempDir()
	kg := recordOwnerTestKG(t, root)
	dir, err := canonicalRecordDir(filepath.Join(root, "evolution"))
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	resolved, err := canonicalRecordDir(alias)
	if err != nil || resolved != dir {
		t.Fatal("existing symlink did not resolve to same owner key")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	release, err := acquireRecordOwner(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	aliased := make(chan error, 1)
	go func() { _, err := Record(ctx, kg, alias, "alias"); aliased <- err }()
	recordOwnerTestQueued(t, dir, 2)
	independent := filepath.Join(root, "independent")
	other := make(chan error, 1)
	go func() { _, err := Record(ctx, kg, independent, "independent"); other <- err }()
	select {
	case err := <-other:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("unrelated directory blocked by held owner")
	}
	select {
	case err := <-aliased:
		t.Fatalf("symlink alias bypassed same owner: %v", err)
	default:
	}
	release()
	recordOwnerTestResult(t, aliased)
	for _, path := range []string{dir, alias, independent} {
		manifest, err := ReadManifest(ctx, path)
		if err != nil || len(manifest.Changesets) != 1 || len(manifest.Metrics) != 1 {
			t.Fatalf("directory%s manifest%+v err%v", path, manifest, err)
		}
	}
	if recordOwnerTestRefs(dir) != 0 {
		t.Fatal("alias owner leaked")
	}
}

// 所有错误返回都必须释放owner；现有归档后失败测试继续覆盖快照清理和再次提交。
func TestRecordOwnerFailureReleasesBeforeNextRecord(t *testing.T) {
	for _, failure := range []string{"source", "db-open", "snapshot-directory"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			root := t.TempDir()
			kg := recordOwnerTestKG(t, root)
			dir, err := canonicalRecordDir(filepath.Join(root, "evolution"))
			if err != nil {
				t.Fatal(err)
			}
			badKG := kg
			var obstacle string
			switch failure {
			case "source":
				badKG = filepath.Join(root, "missing-source.db")
			case "db-open":
				obstacle = filepath.Join(dir, "evolution.db")
				if err := os.Mkdir(obstacle, 0o755); err != nil {
					t.Fatal(err)
				}
			case "snapshot-directory":
				obstacle = filepath.Join(dir, "snapshots")
				if err := os.WriteFile(obstacle, []byte("not a directory"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Record(ctx, badKG, dir, "expected-failure"); err == nil {
				t.Fatal("malformed capture unexpectedly succeeded")
			}
			if recordOwnerTestRefs(dir) != 0 {
				t.Fatal("error path retained owner")
			}
			if obstacle != "" {
				if err := os.Remove(obstacle); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Record(ctx, kg, dir, "after-failure"); err != nil {
				t.Fatal(err)
			}
			m, err := ReadManifest(ctx, dir)
			if err != nil || len(m.Changesets) != 1 || m.Changesets[0].Tool != "after-failure" {
				t.Fatal("failed attempt committed or next attempt blocked")
			}
		})
	}
}
