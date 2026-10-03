package storage

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/xcosmosbox/cairn/core/dktypes"
)

// The old DSN silently discarded every permission/pragma option.
func TestSQLiteDefaultPragmasAndSchema(t *testing.T) {
	t.Parallel()
	db, err := NewDB(DBOptions{Path: filepath.Join(t.TempDir(), "default.db"), MaxOpenConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	c1, err := db.Conn().Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c1.Close()
	c2, err := db.Conn().Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	for _, conn := range []*sql.Conn{c1, c2} {
		var journal string
		var busy, version int
		if err := conn.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journal); err != nil {
			t.Fatal(err)
		}
		if err := conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busy); err != nil {
			t.Fatal(err)
		}
		if err := conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
			t.Fatal(err)
		}
		if journal != "wal" || busy != 5000 || version != LatestSchemaVersion() {
			t.Fatalf("actual journal=%s timeout=%d version=%d", journal, busy, version)
		}
	}
}

func TestReadOnlyEscapedRelativePathsDoNotWriteOrMigrate(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"plain.db", "question?.db", "hash#.db", "空 格%.db"} {
		for openerName, open := range map[string]func(string) (*DB, error){"query": OpenReadOnly, "preflight": OpenPreflightReadOnly} {
			t.Run(name+"/"+openerName, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), name)
				db, err := NewDB(DBOptions{Path: path, JournalMode: "DELETE"})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.Conn().Exec("PRAGMA user_version=4"); err != nil {
					t.Fatal(err)
				}
				db.Close()
				before, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				cwd, err := os.Getwd()
				if err != nil {
					t.Fatal(err)
				}
				rel, err := filepath.Rel(cwd, path)
				if err != nil {
					t.Fatal(err)
				}
				ro, err := open(rel)
				if err != nil {
					t.Fatal(err)
				}
				var queryOnly, version int
				if err := ro.Conn().QueryRow("PRAGMA query_only").Scan(&queryOnly); err != nil {
					t.Fatal(err)
				}
				if err := ro.Conn().QueryRow("PRAGMA user_version").Scan(&version); err != nil {
					t.Fatal(err)
				}
				if queryOnly != 1 || version != 4 {
					t.Fatalf("protection=%d version=%d", queryOnly, version)
				}
				if _, err := ro.Conn().Exec("INSERT INTO kg_manifest(key,value) VALUES('probe','bad')"); err == nil {
					t.Fatal("readonly write succeeded")
				}
				// SQLite mode=ro remains authoritative even if a caller disables query_only.
				if _, err := ro.Conn().Exec("PRAGMA query_only=0"); err != nil {
					t.Fatal(err)
				}
				if _, err := ro.Conn().Exec("INSERT INTO kg_manifest(key,value) VALUES('probe','bad')"); err == nil {
					t.Fatal("mode=ro protection absent")
				}
				ro.Close()
				after, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				afterInfo, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(before, after) || !info.ModTime().Equal(afterInfo.ModTime()) {
					t.Fatal("readonly open changed bytes/mtime")
				}
			})
		}
	}
}

func TestReadOnlyAndExistingNeverCreateMissingDatabase(t *testing.T) {
	t.Parallel()
	for name, open := range map[string]func(string) (*DB, error){"query": OpenReadOnly, "preflight": OpenPreflightReadOnly, "existing": OpenExisting} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "missing.db")
			if db, err := open(path); err == nil {
				db.Close()
				t.Fatal("missing DB accepted")
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("database created: %v", err)
			}
		})
	}
}

func TestWALReaderAndBusyWriter(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "live.db")
	db, err := NewDB(DBOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	writer, err := OpenSQLite(path, SQLiteOptions{BusyTimeoutMs: 80, ImmediateTransactions: true})
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	tx, err := db.Conn().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("INSERT INTO kg_manifest(key,value) VALUES('pending','value')"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := ro.Conn().QueryRow("SELECT count(*) FROM kg_manifest WHERE key='pending'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("WAL read: count=%d err=%v", count, err)
	}
	started := time.Now()
	if _, err := writer.Exec("INSERT INTO kg_manifest(key,value) VALUES('contender','value')"); err == nil {
		t.Fatal("competing writer unexpectedly succeeded")
	}
	if elapsed := time.Since(started); elapsed < 50*time.Millisecond {
		t.Fatalf("busy_timeout did not wait: %v", elapsed)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := ro.Conn().QueryRow("SELECT count(*) FROM kg_manifest WHERE key='pending'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("committed WAL invisible: count=%d err=%v", count, err)
	}
}

func TestSnapshotIncludesUncheckpointedWAL(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	source, destination := filepath.Join(dir, "source.db"), filepath.Join(dir, "snapshot?.db")
	db, err := NewDB(DBOptions{Path: source})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Conn().Exec("PRAGMA wal_autocheckpoint=0; INSERT INTO kg_manifest(key,value) VALUES('wal-only','value')"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := SnapshotDatabase(context.Background(), source, destination); err != nil {
		t.Fatal(err)
	}
	snapshot, err := OpenReadOnly(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	var count int
	if err := snapshot.Conn().QueryRow("SELECT count(*) FROM kg_manifest WHERE key='wal-only'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("WAL missing: count=%d err=%v", count, err)
	}
	after, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || !info.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatal("snapshot mutated source")
	}
	if err := SnapshotDatabase(context.Background(), source, destination); err == nil {
		t.Fatal("snapshot overwrote existing destination")
	}
}

func TestFailedSnapshotPreservesAnotherDestination(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bad, target := filepath.Join(dir, "corrupt.db"), filepath.Join(dir, "owned-by-another.db")
	if err := os.WriteFile(bad, []byte("not SQLite"), 0644); err != nil {
		t.Fatal(err)
	}
	expected := []byte("another caller's data")
	if err := os.WriteFile(target, expected, 0644); err != nil {
		t.Fatal(err)
	}
	if err := SnapshotDatabase(context.Background(), bad, target); err == nil {
		t.Fatal("existing target accepted")
	}
	actual, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(actual, expected) {
		t.Fatalf("target lost: %v %q", err, actual)
	}
	newTarget := filepath.Join(dir, "new.db")
	if err := SnapshotDatabase(context.Background(), bad, newTarget); err == nil {
		t.Fatal("corrupt source accepted")
	}
	if _, err := os.Stat(newTarget); !os.IsNotExist(err) {
		t.Fatal("failed snapshot published corrupt output")
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".cairn-snapshot-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary leaked: %v %v", matches, err)
	}
}

func TestConcurrentSnapshotsNeverEraseWinner(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	source, destination := filepath.Join(dir, "source.db"), filepath.Join(dir, "same-output.db")
	db, err := NewDB(DBOptions{Path: source})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Conn().Exec("INSERT INTO kg_manifest(key,value) VALUES('present','value')"); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 4)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- SnapshotDatabase(context.Background(), source, destination)
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful publications=%d", successes)
	}
	winner, err := OpenReadOnly(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer winner.Close()
	var count int
	if err := winner.Conn().QueryRow("SELECT count(*) FROM kg_manifest WHERE key='present'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("winner lost: count=%d err=%v", count, err)
	}
}

func TestRealSchema4ReadCompatibilityWithoutMigration(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := NewDB(DBOptions{Path: path, JournalMode: "DELETE"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	node := &dktypes.Node{ID: "node", Label: dktypes.LabelConcept, Name: "legacy", Domain: "domain", Subdomain: "sub", Confidence: 1, Provenance: dktypes.ProvenanceExtraction}
	if err := NewNodeRepo(db).Insert(ctx, node); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec("ALTER TABLE nodes DROP COLUMN file_slug; PRAGMA user_version=4"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	r := NewNodeRepo(ro)
	got, err := r.GetByID(ctx, "node")
	if err != nil || got == nil || got.FileSlug != "" {
		t.Fatalf("legacy node: %+v %v", got, err)
	}
	if got, err := r.GetByIDs(ctx, []string{"node"}); err != nil || len(got) != 1 {
		t.Fatalf("GetByIDs: %v %v", got, err)
	}
	for _, read := range []func() ([]*dktypes.Node, error){func() ([]*dktypes.Node, error) { return r.ListAll(ctx) }, func() ([]*dktypes.Node, error) { return r.ListByDomain(ctx, "domain") }, func() ([]*dktypes.Node, error) { return r.ListBySubdomain(ctx, "domain", "sub") }, func() ([]*dktypes.Node, error) { return r.SearchByNameSimilarity(ctx, "legacy", 0) }} {
		if got, err := read(); err != nil || len(got) != 1 {
			t.Fatalf("legacy list: %v %v", got, err)
		}
	}
	ro.Close()
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || !info.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatal("reading true v4 modified source")
	}
}

func TestOpenExistingPreservesLegacyJournalAndSchema(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := NewDB(DBOptions{Path: path, JournalMode: "DELETE"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec("ALTER TABLE nodes DROP COLUMN file_slug; PRAGMA user_version=4"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	existing, err := OpenExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	defer existing.Close()
	if err := NewManifestRepo(existing).Set(context.Background(), "sentinel.history", "[]"); err != nil {
		t.Fatal(err)
	}
	var journal string
	var version, slugColumns int
	if err := existing.Conn().QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if err := existing.Conn().QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := existing.Conn().QueryRow("SELECT count(*) FROM pragma_table_info('nodes') WHERE name='file_slug'").Scan(&slugColumns); err != nil {
		t.Fatal(err)
	}
	if journal != "delete" || version != 4 || slugColumns != 0 {
		t.Fatalf("metadata write changed legacy layout: journal=%s version=%d slug=%d", journal, version, slugColumns)
	}
}
