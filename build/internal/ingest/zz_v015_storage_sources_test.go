package ingest

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

// 原路径把 raw slice 长度当 SQL 提交数，静默漏来源，并在来源事务失败后推进版本。
// Raw duplicates, missing mappings and a failed provenance transaction must remain distinct.
func TestV015StorageIngestSourceAccounting(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"duplicate", "duplicate-node-contribution", "missing", "write-failure"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			db, err := storage.NewDB(storage.DBOptions{Path: filepath.Join(t.TempDir(), "source.db")})
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			version, err := storage.NewKBVersionRepo(db).Bump(ctx)
			if err != nil {
				t.Fatal(err)
			}
			nodes := []extract.Node{{ID: "shared", Name: "Qualified pump", Summary: "Operate only above 2 bar", Description: "Do not operate during maintenance. Calibration remains uncertain.", Confidence: .9, Members: []string{"m"}}}
			source := MemberSource{Skill: "s", FilePath: "original/a.md", StartLine: 17, EndLine: 19}
			mapping := map[string][]MemberSource{"m": {source, source}}
			if mode == "duplicate-node-contribution" {
				nodes = append(nodes, extract.Node{ID: "shared", Name: "Qualified pump", Confidence: .9, Members: []string{"other"}})
				mapping["m"] = []MemberSource{source}
				mapping["other"] = []MemberSource{{Skill: "s", FilePath: "original/b.md", StartLine: 31, EndLine: 32}}
			}
			if mode == "missing" {
				delete(mapping, "m")
			}
			if mode == "write-failure" {
				_, err = db.Conn().Exec(`CREATE TRIGGER reject_source BEFORE INSERT ON node_sources BEGIN SELECT RAISE(ABORT,'injected source failure'); END`)
				if err != nil {
					t.Fatal(err)
				}
			}
			got, err := IngestDomains(ctx, db, IngestDomainsOptions{Result: &extract.Result{Domains: []extract.Domain{{Slug: "d", Name: "D", SourceSkills: []string{"s"}, Subdomains: []extract.Subdomain{{Slug: "sd", Name: "SD", Entities: nodes}}}}}, MemberSources: mapping})
			if mode == "missing" || mode == "write-failure" {
				if err == nil {
					t.Fatal("lost selected provenance reported success")
				}
				if got == nil || got.NodeSourcesInserted != 0 {
					t.Fatalf("failed source scope lacks factual zero committed count: %+v", got)
				}
				if mode == "missing" && (len(got.NodeSourceMissingMembers) != 1 || got.NodeSourceMissingMembers[0] != "shared/m") {
					t.Fatalf("missing member identity lost: %+v", got)
				}
				if mode == "write-failure" && (got.NodeSourcesAttempted != 2 || got.NodeSourcesSelectedDistinct != 1 || got.NodeSourcesDuplicates != 1) {
					t.Fatalf("failed attempts misclassified: %+v", got)
				}
				after, e := storage.NewKBVersionRepo(db).Current(ctx)
				if e != nil || after != version {
					t.Fatalf("failed scope advanced baseline: %q %v", after, e)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			rows, err := storage.NewNodeSourceRepo(db).ListAll(ctx)
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if mode == "duplicate-node-contribution" {
				want = 2
			}
			if len(rows) != want || got.NodeSourcesInserted != want {
				t.Fatalf("committed distinct mismatch: rows=%+v result=%+v", rows, got)
			}
			if got.NodeSourcesAttempted != 2 || got.NodeSourcesSelectedDistinct != want || got.NodeSourcesDuplicates != 2-want {
				t.Fatalf("raw/distinct/duplicate classification wrong: %+v", got)
			}
		})
	}
}

// 同一原文内重复的已接受语义共享 member ID，位置差异不构成来源身份冲突。
// The original span is a real representative hint, not an exhaustive occurrence ledger.
func TestV015StorageAcceptedRepeatedOriginalOccurrence(t *testing.T) {
	t.Parallel()
	item := dktypes.AnnotatedItem{Tag: dktypes.TagConcept, Name: "Maintenance prohibition", Content: "Pump must not run during maintenance.", Detail: "The prohibition applies throughout maintenance.", Confidence: 1, SourceSpan: &dktypes.SourceSpan{StartLine: 7, EndLine: 8}}
	again := item
	again.SourceSpan = &dktypes.SourceSpan{StartLine: 42, EndLine: 43}
	docs := extract.AssignIDs([]*dktypes.AnnotatedDocument{{Skill: "ops", FilePath: "original/ops.md", Items: []dktypes.AnnotatedItem{item, again}}})
	first, second := docs[0].Items[0], docs[0].Items[1]
	if first.ID != second.ID {
		t.Fatal("accepted semantic identity contract changed")
	}
	for _, reverse := range []bool{false, true} {
		db, err := storage.NewDB(storage.DBOptions{Path: filepath.Join(t.TempDir(), "repeat.db")})
		if err != nil {
			t.Fatal(err)
		}
		mapping := map[string][]MemberSource{}
		for _, it := range docs[0].Items {
			mapping[it.ID] = append(mapping[it.ID], MemberSource{Skill: docs[0].Skill, FilePath: docs[0].FilePath, StartLine: it.SourceSpan.StartLine, EndLine: it.SourceSpan.EndLine})
		}
		if reverse {
			mapping[first.ID][0], mapping[first.ID][1] = mapping[first.ID][1], mapping[first.ID][0]
		}
		candidate := &extract.Result{Domains: []extract.Domain{{Slug: "d", Name: "D", Subdomains: []extract.Subdomain{{Slug: "sd", Name: "SD", Concepts: []extract.Node{{ID: "n", Name: first.Name, Summary: first.Content, Description: first.Detail, Confidence: 1, Members: []string{first.ID}}}}}}}}
		got, err := IngestDomains(context.Background(), db, IngestDomainsOptions{Result: candidate, MemberSources: mapping})
		if err != nil {
			db.Close()
			t.Fatalf("valid accepted repeated location rejected: %+v %v", got, err)
		}
		if got.NodeSourcesAttempted != 2 || got.NodeSourcesSelectedDistinct != 1 || got.NodeSourcesDuplicates != 1 || got.NodeSourcesInserted != 1 {
			t.Fatalf("occurrence/incidence counts conflated: %+v", got)
		}
		rows, err := storage.NewNodeSourceRepo(db).ListAll(context.Background())
		if err != nil || len(rows) != 1 || rows[0].StartLine != 7 || rows[0].EndLine != 8 || rows[0].MemberID != first.ID || rows[0].FilePath != docs[0].FilePath {
			t.Fatalf("not a deterministic genuine original location: %+v %v", rows, err)
		}
		db.Close()
	}
}
