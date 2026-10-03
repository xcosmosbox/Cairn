package pipeline

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/core/storage"
)

// Full-built KG ownership must be in the published DB, not only its worktree.
func TestFullRebuildPersistsRepositoryIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "knowledge.db")
	identity := "git:github.com/owner/repo"
	o := &Orchestrator{}
	if _, err := o.fullRebuildIngest(ctx, path, &extract.Result{}, nil, nil, identity); err != nil {
		t.Fatal(err)
	}
	db, err := storage.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	got, err := storage.NewRepositoryIdentityRepo(db).Get(ctx)
	if err != nil || got != identity {
		t.Fatalf("identity=%q err=%v", got, err)
	}
}

func TestFailedFullIngestPreservesPreviousDatabaseAndWAL(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "knowledge.db")
	db, err := storage.NewDB(storage.DBOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Conn().Exec("PRAGMA wal_autocheckpoint=0; INSERT INTO kg_manifest(key,value) VALUES('previous','still-present')"); err != nil {
		t.Fatal(err)
	}
	main, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	wal, err := os.ReadFile(path + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	o := &Orchestrator{}
	if _, err := o.fullRebuildIngest(context.Background(), path, nil, nil, nil, "git:github.com/owner/repo"); err == nil {
		t.Fatal("invalid ingest succeeded")
	}
	afterMain, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(main, afterMain) {
		t.Fatal("failed full ingest modified previous main")
	}
	afterWAL, err := os.ReadFile(path + "-wal")
	if err != nil || !bytes.Equal(wal, afterWAL) {
		t.Fatal("failed full ingest removed/modified previous WAL")
	}
	var value string
	if err := db.Conn().QueryRow("SELECT value FROM kg_manifest WHERE key='previous'").Scan(&value); err != nil || value != "still-present" {
		t.Fatalf("previous data lost: %s %v", value, err)
	}
	files, err := filepath.Glob(filepath.Join(dir, ".cairn-full-*"))
	if err != nil || len(files) != 0 {
		t.Fatalf("temporary DB leaked: %v %v", files, err)
	}
}

func TestFullRebuildRefusesActiveWALThenPublishesClosedCandidate(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "knowledge.db")
	db, err := storage.NewDB(storage.DBOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec("INSERT INTO kg_manifest(key,value) VALUES('previous','old')"); err != nil {
		t.Fatal(err)
	}
	o := &Orchestrator{}
	identity := "git:github.com/owner/repo"
	if _, err := o.fullRebuildIngest(context.Background(), path, &extract.Result{}, nil, nil, identity); err == nil {
		t.Fatal("replaced active WAL database")
	}
	db.Close()
	if _, err := o.fullRebuildIngest(context.Background(), path, &extract.Result{}, nil, nil, identity); err != nil {
		t.Fatal(err)
	}
	replaced, err := storage.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer replaced.Close()
	got, err := storage.NewRepositoryIdentityRepo(replaced).Get(context.Background())
	if err != nil || got != identity {
		t.Fatalf("candidate identity missing: %s %v", got, err)
	}
	var old int
	if err := replaced.Conn().QueryRow("SELECT count(*) FROM kg_manifest WHERE key='previous'").Scan(&old); err != nil || old != 0 {
		t.Fatalf("stale DB content remained: %d %v", old, err)
	}
}
