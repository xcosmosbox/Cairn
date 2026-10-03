package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/ingest"
	"github.com/xcosmosbox/cairn/build/internal/writeback"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

func materializationFixture(t *testing.T) (*Orchestrator, string, *extract.Result, []*dktypes.AnnotatedDocument) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "references", "doc.md"), []byte("original source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	orch, err := NewOrchestrator(Options{Client: writebackFixtureLLM{}, MinConfidence: 0.7, RepositoryIdentity: "local:fixture"})
	if err != nil {
		t.Fatal(err)
	}
	res := &extract.Result{Domains: []extract.Domain{{Name: "Domain", Slug: "domain", SourceSkills: []string{"test"}, Subdomains: []extract.Subdomain{{Name: "Subdomain", Slug: "subdomain", Concepts: []extract.Node{
		{ID: "kept", Name: "authoritative", Description: "generated description", Confidence: 0.9, Members: []string{"member-a"}},
		{ID: "other", Name: "other", Description: "other description", Confidence: 0.9, Members: []string{"member-b"}},
		{ID: "kept", Name: "excluded alias", Description: "must not overwrite", Confidence: 0, Members: []string{"member-a"}},
		{ID: "excluded", Name: "excluded", Confidence: 0, Members: []string{"member-c"}},
	}, Relations: []extract.Relation{
		{Source: "kept", Target: "other", Kind: "references", Confidence: 0.9},
		{Source: "kept", Target: "excluded", Kind: "references", Confidence: 0.9},
	}}}}}}
	docs := []*dktypes.AnnotatedDocument{{Skill: "test", FilePath: "references/doc.md", Items: []dktypes.AnnotatedItem{
		{ID: "member-a", Detail: "source detail a"}, {ID: "member-b", Detail: "source detail b"}, {ID: "member-c", Detail: "source detail c"},
	}}}
	return orch, root, res, docs
}

func TestMaterializationUsesIdenticalConfidenceCandidateForDatabaseAndSidecar(t *testing.T) {
	t.Parallel()
	o, root, res, docs := materializationFixture(t)
	path := filepath.Join(root, "candidate.db")
	rpt, err := o.MaterializeCompletedExtraction(context.Background(), root, path, res, docs, []ingest.SkillMeta{{Name: "test"}})
	if err != nil {
		t.Fatal(err)
	}
	if rpt.Selection.NodesFiltered != 2 || rpt.Selection.RelationsFiltered != 1 || rpt.Ingest.ConceptNodes != 2 || rpt.Ingest.EdgesSkipped != 0 || rpt.Ingest.SemanticEdges != 1 || rpt.Writeback.NodesWritten != 2 {
		t.Fatalf("candidate outputs diverged: selection=%+v ingest=%+v writeback=%+v", rpt.Selection, rpt.Ingest, rpt.Writeback)
	}
	db, err := storage.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	node, err := storage.NewNodeRepo(db).GetByID(context.Background(), "kept")
	if err != nil || node == nil || node.Name != "authoritative" || node.Description != "generated description" {
		t.Fatalf("authoritative node overwritten: %+v %v", node, err)
	}
	sidecar, err := writeback.ReadSidecar(filepath.Join(root, "references", "doc.md"))
	if err != nil || len(sidecar.Nodes) != 2 {
		t.Fatalf("unexpected sidecar nodes: %+v %v", sidecar, err)
	}
	for _, node := range sidecar.Nodes {
		if node.UUID != "kept" && node.UUID != "other" {
			t.Fatalf("excluded UUID materialized: %s", node.UUID)
		}
	}
	md, err := os.ReadFile(filepath.Join(root, "references", "doc.md"))
	if err != nil || strings.Contains(string(md), "must not overwrite") || strings.Contains(string(md), "excluded alias") {
		t.Fatalf("excluded content written: %s %v", md, err)
	}
}

func TestMaterializationRejectsEligibleUUIDCollisionBeforeAnyOutput(t *testing.T) {
	t.Parallel()
	o, root, res, docs := materializationFixture(t)
	res.Domains[0].Subdomains[0].Concepts[2].Confidence = 0.9
	path := filepath.Join(root, "candidate.db")
	if _, err := o.MaterializeCompletedExtraction(context.Background(), root, path, res, docs, nil); err == nil || !strings.Contains(err.Error(), "duplicate eligible UUID") {
		t.Fatalf("collision accepted: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("database created before collision gate: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "references", "doc.md")); err != nil || string(data) != "original source\n" {
		t.Fatalf("source changed before collision gate: %q %v", data, err)
	}
}
