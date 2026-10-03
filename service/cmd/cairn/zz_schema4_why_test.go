package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
	"github.com/xcosmosbox/cairn/service/internal/service"
)

func TestWhyReadsTrueSchema4WithoutMigration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := storage.NewDB(storage.DBOptions{Path: path, JournalMode: "DELETE"})
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.NewNodeRepo(db).Insert(ctx, &dktypes.Node{ID: "legacy-node", Label: dktypes.LabelConcept, Name: "legacy", Domain: "domain", Subdomain: "sub", Confidence: 1, Provenance: dktypes.ProvenanceExtraction}); err != nil {
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
	ro, err := storage.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := runWhy(service.NewKnowledgeService(ro, nil, nil), ro, []string{"legacy-node"}); err != nil {
		t.Fatal(err)
	}
	ro.Close()
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("why migrated/modified true v4")
	}
}
