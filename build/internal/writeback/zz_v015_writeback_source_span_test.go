package writeback

import (
	"path/filepath"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/extract"
)

// Baseline takes the first source's lines for every document and the shared primary.
func TestV015ResearchSharedSourceSpans(t *testing.T) {
	root := t.TempDir()
	result := &extract.Result{Domains: []extract.Domain{{Slug: "d", Name: "Domain", Subdomains: []extract.Subdomain{{Slug: "s", Name: "Subdomain", Entities: []extract.Node{{ID: "n", Name: "Node", Summary: "Faithful summary", Description: "Only after 2025; except stopped queues.", Members: []string{"m"}}}}}}}}
	_, err := Writeback(result, map[string][]MemberSource{"m": {{Skill: "a", FilePath: "a.md", StartLine: 7, EndLine: 8}, {Skill: "b", FilePath: "b.md", StartLine: 93, EndLine: 94}}}, root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ReadSidecar(filepath.Join(root, "b.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Nodes) != 1 || b.Nodes[0].Span == nil || b.Nodes[0].Span.StartLine != 93 || b.Nodes[0].Span.EndLine != 94 {
		t.Fatalf("b.md source span mislabeled: %+v", b.Nodes)
	}
}
