package evolve

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"sync"
	"testing"

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
