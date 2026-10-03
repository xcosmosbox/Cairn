package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/writeback"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

func TestCandidateValidationRequiresSourceSidecarGraphClosure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "kg.db")
	db, err := storage.NewDB(storage.DBOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	node := &dktypes.Node{ID: "node", Label: dktypes.LabelConcept, Name: "concept", Domain: "domain", Subdomain: "subdomain", Summary: "summary", Description: "description", Confidence: 1, Provenance: dktypes.ProvenanceLLMInferred}
	if err := storage.NewNodeRepo(db).Insert(ctx, node); err != nil {
		t.Fatal(err)
	}
	if err := storage.NewNodeSourceRepo(db).InsertBatch(ctx, []storage.NodeSource{{NodeUUID: "node", MemberID: "member", Skill: "skill", FilePath: "references/doc.md"}}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	update := writeback.NodeUpdate{UUID: node.ID, Tag: string(node.Label), Name: node.Name, Domain: "domain", Subdomain: "subdomain", DomainSlug: "domain", SubdomainSlug: "subdomain", Summary: node.Summary, Description: node.Description, Members: []string{"member"}, Provenance: string(node.Provenance)}
	if err := writeback.RewriteDoc(root, "references/doc.md", []writeback.NodeUpdate{update}, time.Now()); err != nil {
		t.Fatal(err)
	}
	request := ValidateRequest{DBPath: path, RepoPath: root}
	report, err := validateCandidate(ctx, request)
	if err != nil || !report.OK {
		t.Fatalf("valid closure rejected: %+v %v", report, err)
	}
	os.WriteFile(filepath.Join(root, "references", "doc.md"), []byte("new unmaterialized source prose"), 0o644)
	report, err = validateCandidate(ctx, request)
	if err != nil || report.OK || !strings.Contains(strings.Join(report.Errors, " "), "source closure") {
		t.Fatalf("mismatched closure accepted: %+v %v", report, err)
	}
}

func TestCandidateClosureRejectsExternalSymlink(t *testing.T) {
	for _, component := range []string{"parent", "sidecar"} {
		t.Run(component, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			outside := t.TempDir()
			path := filepath.Join(root, "kg.db")
			db, err := storage.NewDB(storage.DBOptions{Path: path})
			if err != nil {
				t.Fatal(err)
			}
			node := &dktypes.Node{ID: "node", Label: dktypes.LabelConcept, Name: "concept", Domain: "domain", Subdomain: "subdomain", Summary: "summary", Description: "description", Confidence: 1, Provenance: dktypes.ProvenanceLLMInferred}
			if err := storage.NewNodeRepo(db).Insert(ctx, node); err != nil {
				t.Fatal(err)
			}
			if err := storage.NewNodeSourceRepo(db).InsertBatch(ctx, []storage.NodeSource{{NodeUUID: "node", MemberID: "member", Skill: "skill", FilePath: "references/doc.md"}}); err != nil {
				t.Fatal(err)
			}
			db.Close()
			update := writeback.NodeUpdate{UUID: node.ID, Tag: string(node.Label), Name: node.Name, Domain: "domain", Subdomain: "subdomain", DomainSlug: "domain", SubdomainSlug: "subdomain", Summary: node.Summary, Description: node.Description, Members: []string{"member"}, Provenance: string(node.Provenance)}
			if err := writeback.RewriteDoc(root, "references/doc.md", []writeback.NodeUpdate{update}, time.Now()); err != nil {
				t.Fatal(err)
			}
			if component == "parent" {
				if err := os.Rename(filepath.Join(root, "references"), filepath.Join(outside, "references")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(outside, "references"), filepath.Join(root, "references")); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Rename(filepath.Join(root, "references/doc.md.kg.yaml"), filepath.Join(outside, "doc.md.kg.yaml")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(outside, "doc.md.kg.yaml"), filepath.Join(root, "references/doc.md.kg.yaml")); err != nil {
					t.Fatal(err)
				}
			}
			report, err := validateCandidate(ctx, ValidateRequest{DBPath: path, RepoPath: root})
			if err == nil && report.OK {
				t.Fatalf("published closure accepted %s symlink outside managed root: %+v", component, report)
			}
		})
	}
}
