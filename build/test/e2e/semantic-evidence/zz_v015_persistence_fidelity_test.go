package semanticevidence

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/ingest"
	"github.com/xcosmosbox/cairn/build/internal/writeback"
	"github.com/xcosmosbox/cairn/core/storage"
)

// Match the historical arithmetic without replaying or modifying its raw packet:
// 172 attempted rows can represent 171 valid distinct incidences, all committed.
func TestV015Raw172Distinct171CommittedSQLite(t *testing.T) {
	ctx := context.Background()
	db, err := storage.NewDB(storage.DBOptions{Path: filepath.Join(t.TempDir(), "graph.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	result := &extract.Result{Domains: []extract.Domain{{Slug: "public", Name: "Public", SourceSkills: []string{"fixture"}, Subdomains: []extract.Subdomain{{Slug: "evidence", Name: "Evidence", Entities: []extract.Node{{ID: "stable-node", Name: "Qualified node", Summary: "Only above 2 bar.", Description: "Does not operate during maintenance; calibration remains uncertain.", Confidence: 1, Members: []string{"m"}, SourceSkills: []string{"fixture"}}}}}}}}
	var sources []ingest.MemberSource
	for i := 0; i < 171; i++ {
		sources = append(sources, ingest.MemberSource{Skill: "fixture", FilePath: fmt.Sprintf("source-%03d.md", i), StartLine: i + 1, EndLine: i + 1})
	}
	sources = append(sources, sources[0])
	opts := ingest.IngestDomainsOptions{Result: result, MemberSources: map[string][]ingest.MemberSource{"m": sources}}
	for pass := 0; pass < 1; pass++ {
		got, err := ingest.IngestDomains(ctx, db, opts)
		if err != nil {
			t.Fatal(err)
		}
		if got.NodeSourcesAttempted != 172 || got.NodeSourcesSelectedDistinct != 171 || got.NodeSourcesDuplicates != 1 || got.NodeSourcesInserted != 171 {
			t.Fatalf("raw/distinct/committed conflated: %+v", got)
		}
		rows, err := storage.NewNodeSourceRepo(db).ListAll(ctx)
		if err != nil || len(rows) != 171 {
			t.Fatalf("actual source readback=%d: %v", len(rows), err)
		}
		for _, row := range rows {
			if row.StartLine <= 0 || row.EndLine != row.StartLine {
				t.Fatal("original range lost")
			}
		}
	}
	// Full rebuild receives a fresh DB by contract; repeat the shared source helper
	// on this live DB to verify idempotence without treating full Insert as upsert.
	rows, err := storage.NewNodeSourceRepo(db).ListAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rows = append(rows, rows[0])
	repeat, err := storage.NewNodeSourceRepo(db).InsertBatchWithResult(ctx, rows)
	if err != nil || repeat.Attempted != 172 || repeat.DistinctSelected != 171 || repeat.DuplicateRows != 1 || repeat.Committed != 171 {
		t.Fatalf("repeat helper counts=%+v err=%v", repeat, err)
	}

}

// Inject an actual SQLite write failure: no successful source count or baseline
// advancement may conceal a scope that never persisted its selected incidences.
func TestV015FullSourceWriteFailureNoBaselineAdvance(t *testing.T) {
	ctx := context.Background()
	db, err := storage.NewDB(storage.DBOptions{Path: filepath.Join(t.TempDir(), "graph.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	result := &extract.Result{Domains: []extract.Domain{{Slug: "d", Name: "Domain", Subdomains: []extract.Subdomain{{Slug: "s", Name: "Subdomain", Entities: []extract.Node{{ID: "n", Name: "Node", Summary: "Summary", Description: "Qualified details", Confidence: 1, Members: []string{"m"}}}}}}}}
	opts := ingest.IngestDomainsOptions{Result: result, MemberSources: map[string][]ingest.MemberSource{"m": {{Skill: "fixture", FilePath: "raw.md", StartLine: 9, EndLine: 10}}}}
	version, err := storage.NewKBVersionRepo(db).Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec(`CREATE TRIGGER reject_v015_source BEFORE INSERT ON node_sources BEGIN SELECT RAISE(ABORT, 'fixture source failure'); END`); err != nil {
		t.Fatal(err)
	}
	got, err := ingest.IngestDomains(ctx, db, opts)
	if err == nil {
		t.Fatalf("failed scope reported successful: %+v", got)
	}
	if got != nil && got.NodeSourcesInserted != 0 {
		t.Fatal("rollback claimed committed sources")
	}
	after, err := storage.NewKBVersionRepo(db).Current(ctx)
	if err != nil || after != version {
		t.Fatal("failed persistence advanced graph baseline")
	}
	rows, err := storage.NewNodeSourceRepo(db).ListAll(ctx)
	if err != nil || len(rows) != 0 {
		t.Fatal("failed selected sources survived rollback")
	}
}

// Full helper composition starts with a selected graph fixture, materializes it,
// then passes actual SQLite edges to the bounded writer and reads every pair.
// This tests persistence/readback, not the upstream model's semantic judgment.
func TestV015FullSQLiteSelectedGraphWriteback(t *testing.T) {
	ctx := context.Background()
	db, err := storage.NewDB(storage.DBOptions{Path: filepath.Join(t.TempDir(), "graph.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := t.TempDir()
	summary := "Admission is conditional.\nEmergency admission is exempt."
	detail := "Before 2025 quota is required except emergencies.\n\nAfter 2025 quota is required only during scheduled admission; historical calibration remains uncertain."
	envelope := `{"format":"cairn_relation_assertions_v1","assertions":[{"classification":"explicit","description":"Before 2025 quota is required except emergencies","sources":[{"id":"frozen-public-a","path":"a.md","sha256":"fixed-fixture-source"}],"evidence":[{"quote":"except emergencies","start_line":7,"end_line":8}]},{"classification":"legacy_unverified","description":"Historical uncertain calibration"}]}`
	result := &extract.Result{Domains: []extract.Domain{{Slug: "public", Name: "Public", SourceSkills: []string{"fixture"}, Subdomains: []extract.Subdomain{{Slug: "admission", Name: "Admission", Entities: []extract.Node{{ID: "a", Name: "Admission", Summary: summary, Description: detail, Confidence: .9, Members: []string{"ma"}, SourceSkills: []string{"fixture"}}, {ID: "b", Name: "Quota", Summary: "Conditional quota", Description: "Emergency admission is exempt.", Confidence: .9, Members: []string{"mb"}, SourceSkills: []string{"fixture"}}}, Relations: []extract.Relation{{Source: "a", Target: "b", Kind: "depends_on", Description: envelope, Confidence: .91}}}}}}}
	members := map[string][]ingest.MemberSource{"ma": {{Skill: "fixture", FilePath: "a.md", StartLine: 7, EndLine: 8}, {Skill: "fixture", FilePath: "b.md", StartLine: 93, EndLine: 94}}, "mb": {{Skill: "fixture", FilePath: "b.md", StartLine: 97, EndLine: 98}}}
	if got, err := ingest.IngestDomains(ctx, db, ingest.IngestDomainsOptions{Result: result, MemberSources: members}); err != nil || got.NodeSourcesInserted != 3 {
		t.Fatalf("full persistence: %+v %v", got, err)
	}
	node, err := storage.NewNodeRepo(db).GetByID(ctx, "a")
	if err != nil || node.Summary != summary || node.Description != detail {
		t.Fatal("SQLite paired material changed")
	}
	edges, err := storage.NewEdgeRepo(db).ListAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mapping := map[string][]writeback.MemberSource{}
	for id, rows := range members {
		for _, s := range rows {
			mapping[id] = append(mapping[id], writeback.MemberSource{Skill: s.Skill, FilePath: s.FilePath, StartLine: s.StartLine, EndLine: s.EndLine})
		}
	}
	if _, err := writeback.WritebackContextWithRelations(ctx, result, mapping, edges, root); err != nil {
		t.Fatal(err)
	}
	incident := writeback.IncidentRelations(edges, "a")
	for _, rel := range []string{"a.md", "b.md", writeback.PrimaryRelPath("public", "", "a")} {
		md, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		expectedSummary := summary
		if !strings.HasPrefix(rel, "_shared/") {
			expectedSummary = strings.Join(writeback.MirrorSummaryLines("Admission", summary), "\n")
		}
		if !strings.Contains(string(md), expectedSummary) || strings.Contains(string(md), "cairn_relation_assertions_v1") {
			t.Fatalf("wrong editable content: %s", rel)
		}
		if strings.HasPrefix(rel, "_shared/") && !strings.Contains(string(md), detail) {
			t.Fatal("full primary lost detailed semantics")
		}
		sc, err := writeback.ReadSidecar(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, n := range sc.Nodes {
			if n.UUID == "a" {
				found = true
				g := n.GraphMetadata
				if g == nil || !g.RelationsKnown || !g.SourcesKnown || len(g.Sources) != 2 || len(g.Relations) != len(incident) {
					t.Fatalf("full graph readback lost in %s", rel)
				}
				for i, row := range g.Relations {
					if !reflect.DeepEqual(row.Edge(), *incident[i]) {
						t.Fatal("selected SQLite edge differs from sidecar")
					}
				}
				if rel == "b.md" && (n.Span == nil || n.Span.StartLine != 93 || n.Span.EndLine != 94) {
					t.Fatal("shared source assigned another document's location")
				}
				if g.GeneratedSpan == nil || g.GeneratedSpan.CoordinateSpace != "generated_document" {
					t.Fatal("generated offset unlabelled")
				}
				for _, source := range g.Sources {
					if source.CoordinateSpace != "source_document" {
						t.Fatal("source offset unlabelled")
					}
				}
			}
		}
		if !found {
			t.Fatal("shared node missing")
		}
	}
}
