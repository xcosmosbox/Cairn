package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

// 来源 API 曾无逐文件契约；这些断言防止暴露工作机路径、伪造缺失 span 或写迁移。
// Assert safe stored locations, explicit unknown spans and byte-preserving legacy reads.
func TestNodeSourcesSanitizeAndRemainReadOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "sources.db")
	db, err := storage.NewDB(storage.DBOptions{Path: file, JournalMode: "DELETE"})
	if err != nil {
		t.Fatal(err)
	}
	node := &dktypes.Node{ID: "live", Name: "queue", Label: dktypes.LabelConcept, Provenance: dktypes.ProvenanceExtraction, SourceRefs: "skill-a, skill-a,docs/队列.md,https://private.invalid/secret,/private/secret,../secret,\nsecret"}
	if err := storage.NewNodeRepo(db).Insert(ctx, node); err != nil {
		t.Fatal(err)
	}
	rows := []storage.NodeSource{
		{NodeUUID: "live", MemberID: "a", Skill: "skill-a", FilePath: "docs/队列.md", StartLine: 6, EndLine: 8},
		{NodeUUID: "live", MemberID: "b", FilePath: "missing-span.md", StartLine: 9, EndLine: 2},
	}
	for i, bad := range []string{"/private/secret", `C:\private\secret`, `\\server\secret`, "../secret", "docs/../secret", "docs//secret", "https://private.invalid/secret", "%2e%2e/secret", "docs/secret\nname", "docs/secret\x00name", ".", ".."} {
		rows = append(rows, storage.NodeSource{NodeUUID: "live", MemberID: strings.Repeat("x", i+1), FilePath: bad})
	}
	if err := storage.NewNodeSourceRepo(db).InsertBatch(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(file)
	stat, _ := os.Stat(file)
	ro, err := storage.OpenReadOnly(file)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewKnowledgeService(ro, nil, nil)
	info, err := svc.GetNodeSources(ctx, "live")
	if err != nil || info.Status != "available" || info.NodeUUID != "live" || len(info.Locations) != 2 || info.Omitted != 16 {
		t.Fatalf("sources: %+v %v", info, err)
	}
	if len(info.SourceRefs) != 2 || info.SourceRefs[0] != "skill-a" || info.SourceRefs[1] != "docs/队列.md" {
		t.Fatalf("refs: %+v", info.SourceRefs)
	}
	if loc := info.Locations[0]; loc.StartLine != 6 || loc.EndLine != 8 || loc.SpanStatus != "available" || loc.Skill != "skill-a" {
		t.Fatalf("span: %+v", loc)
	}
	if loc := info.Locations[1]; loc.StartLine != 0 || loc.EndLine != 0 || loc.SpanStatus != "unknown" {
		t.Fatalf("invented invalid span: %+v", loc)
	}
	encoded, _ := json.Marshal(info)
	if bytes.Contains(encoded, []byte("secret")) {
		t.Fatalf("unsafe source escaped sanitizer: %s", encoded)
	}
	unknown, err := svc.GetNodeSources(ctx, "missing")
	if err != nil || unknown.Status != "unknown" || unknown.NodeUUID != "" || unknown.Reason != "node_not_found" {
		t.Fatalf("unknown: %+v %v", unknown, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := svc.GetNodeSources(cancelled, "live"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation hidden: %v", err)
	}
	if err := ro.Close(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(file)
	end, _ := os.Stat(file)
	if !bytes.Equal(before, after) || !stat.ModTime().Equal(end.ModTime()) {
		t.Fatal("source query changed DB bytes/mtime")
	}
}

func TestNodeSourcesLegacyAndEmptyAreUnknownWithoutMigration(t *testing.T) {
	t.Parallel()
	for _, missingTable := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "legacy-missing-table"}[missingTable], func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "legacy.db")
			db, err := storage.NewDB(storage.DBOptions{Path: file, JournalMode: "DELETE"})
			if err != nil {
				t.Fatal(err)
			}
			if err := storage.NewNodeRepo(db).Insert(context.Background(), &dktypes.Node{ID: "live", Label: dktypes.LabelConcept, Provenance: dktypes.ProvenanceExtraction, Name: "queue", SourceRefs: "skill-a"}); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Conn().Exec("ALTER TABLE nodes DROP COLUMN file_slug; PRAGMA user_version=4"); err != nil {
				t.Fatal(err)
			}
			if missingTable {
				if _, err := db.Conn().Exec("DROP TABLE node_sources"); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(file)
			ro, err := storage.OpenReadOnly(file)
			if err != nil {
				t.Fatal(err)
			}
			info, err := NewKnowledgeService(ro, nil, nil).GetNodeSources(context.Background(), "live")
			if err != nil || info.Status != "unknown" || len(info.SourceRefs) != 1 || len(info.Locations) != 0 {
				t.Fatalf("legacy: %+v %v", info, err)
			}
			if missingTable && info.Reason != "source_table_unavailable" {
				t.Fatalf("missing table hidden: %+v", info)
			}
			var version int
			if err := ro.Conn().QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 4 {
				t.Fatalf("schema mutated: %d %v", version, err)
			}
			if err := ro.Close(); err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(file)
			if !bytes.Equal(before, after) {
				t.Fatal("legacy source query migrated DB")
			}
		})
	}
}
