package service

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

func TestTrueSchema4StatusSentinelAndGetNodeAreReadonly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := storage.NewDB(storage.DBOptions{Path: path, JournalMode: "DELETE"})
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.NewNodeRepo(db).Insert(ctx, &dktypes.Node{ID: "legacy-node", Label: dktypes.LabelConcept, Name: "legacy", Domain: "domain", Subdomain: "sub", Confidence: 1, Provenance: dktypes.ProvenanceExtraction}); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.NewKBVersionRepo(db).Bump(ctx); err != nil {
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
	ro, err := storage.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewKnowledgeService(ro, nil, nil)
	status, err := svc.Status(ctx)
	if err != nil || status.TotalNodes != 1 || status.SchemaVersion != 4 {
		t.Fatalf("status: %+v %v", status, err)
	}
	if _, err := svc.Sentinel(ctx, false); err != nil {
		t.Fatalf("sentinel: %v", err)
	}
	if detail, err := svc.GetNode(ctx, "legacy-node"); err != nil || detail == nil || detail.Node.FileSlug != "" {
		t.Fatalf("node: %+v %v", detail, err)
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
		t.Fatal("service read migrated/modified true v4")
	}
}
