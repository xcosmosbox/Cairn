package ingest

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

// 原物化循环在创建后续子域节点前尝试语义边，导致合法跨域 target 被当成缺失而跳过。
func TestIngestDomainsDefersCrossSemanticEdgesUntilAllNodesExist(t *testing.T) {
	t.Parallel()
	for _, reverse := range []bool{false, true} {
		t.Run(map[bool]string{false: "forward", true: "reverse"}[reverse], func(t *testing.T) {
			ctx := context.Background()
			db, err := storage.NewDB(storage.DBOptions{Path: filepath.Join(t.TempDir(), "graph.db")})
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			a := extract.Node{ID: "node-a", Name: "A", Confidence: .95, Members: []string{"member-a"}, SourceSkills: []string{"s"}}
			b := extract.Node{ID: "node-b", Name: "B", Confidence: .95, Members: []string{"member-b"}, SourceSkills: []string{"s"}}
			c := extract.Node{ID: "node-filtered", Name: "Filtered", Confidence: .2, Members: []string{"member-filtered"}}
			domains := []extract.Domain{
				{Slug: "domain-a", Name: "A", SourceSkills: []string{"s"}, Subdomains: []extract.Subdomain{{Slug: "first", Name: "First", Entities: []extract.Node{a}, Relations: []extract.Relation{{Source: "node-a", Target: "node-b", Kind: "depends_on", Description: "original dependency", Confidence: .91}, {Source: "node-a", Target: "node-filtered", Kind: "references"}, {Source: "node-a", Target: "absent", Kind: "references"}}}}},
				{Slug: "domain-b", Name: "B", Subdomains: []extract.Subdomain{{Slug: "later", Name: "Later", Concepts: []extract.Node{b, c}}}},
			}
			if reverse {
				domains[0], domains[1] = domains[1], domains[0]
			}
			got, err := IngestDomains(ctx, db, IngestDomainsOptions{Result: &extract.Result{Domains: domains}, MinConfidence: .8, MemberSources: map[string][]MemberSource{
				"member-a": {{Skill: "s", FilePath: "references/a.md"}},
				"member-b": {{Skill: "s", FilePath: "references/b.md"}},
				// 既有行为也物化 c；完整映射不能靠遗漏该选中成员假装来源成功。
				// Preserve the existing low-confidence selection and all edge assertions.
				"member-filtered": {{Skill: "s", FilePath: "references/filtered.md"}},
			}})
			if err != nil {
				t.Fatal(err)
			}
			if got.SemanticEdges != 2 || got.EdgesSkipped != 1 || got.NodeSourcesInserted != 3 {
				t.Fatalf("wrong persistence: %+v", got)
			}
			var source, target, kind, description, provenance string
			var confidence float64
			if err := db.Conn().QueryRowContext(ctx, `SELECT source_id,target_id,kind,description,confidence,provenance FROM edges WHERE source_id=? AND target_id=?`, "node-a", "node-b").Scan(&source, &target, &kind, &description, &confidence, &provenance); err != nil {
				t.Fatal(err)
			}
			if source != "node-a" || target != "node-b" || kind != "depends_on" || description != "original dependency" || confidence != .91 || provenance != string(dktypes.ProvenanceLLMInferred) {
				t.Fatalf("semantic metadata changed: %q %q %q %.2f %q", source, target, description, confidence, provenance)
			}
			var nullSpans int
			if err := db.Conn().QueryRowContext(ctx, `SELECT COUNT(*) FROM node_sources WHERE start_line IS NULL AND end_line IS NULL`).Scan(&nullSpans); err != nil || nullSpans != 3 {
				t.Fatalf("unknown source spans changed: count=%d err=%v", nullSpans, err)
			}
		})
	}
}
