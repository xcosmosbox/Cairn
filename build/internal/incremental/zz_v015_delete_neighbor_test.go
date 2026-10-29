package incremental

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/writeback"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

// Legal existing C2 path: two independent one-source nodes are connected by an
// edge; removing A's last source must refresh B's read-only incident snapshots.
// Otherwise a later ordinary human edit in B is falsely treated as C3.
func TestV015R2RoundtripDeleteEndpointRefreshesSurvivor(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	root, dbPath := filepath.Join(dir, "repo"), filepath.Join(dir, "kg.db")
	identityFixture(t, root, dbPath, "git:github.com/owner/a", true)
	if err := os.WriteFile(filepath.Join(root, "old-skill/SKILL.md"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}
	db, err := storage.NewDB(storage.DBOptions{Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	st := newStores(db)
	for _, layer := range []*dktypes.Node{
		{ID: "skill::old-skill", Label: dktypes.LabelSkill, Name: "old-skill", Provenance: dktypes.ProvenanceExtraction, Confidence: 1},
		{ID: "domain::engineering", Label: dktypes.LabelDomain, Name: "engineering", Domain: "engineering", Provenance: dktypes.ProvenanceExtraction, Confidence: 1},
		{ID: "subdomain::engineering::queues", Label: dktypes.LabelSubdomain, Name: "queues", Domain: "engineering", Subdomain: "queues", Provenance: dktypes.ProvenanceExtraction, Confidence: 1},
		{ID: "subdomain::engineering::admission", Label: dktypes.LabelSubdomain, Name: "admission", Domain: "engineering", Subdomain: "admission", Provenance: dktypes.ProvenanceExtraction, Confidence: 1},
	} {
		if err := st.nodes.Insert(ctx, layer); err != nil {
			t.Fatal(err)
		}
	}
	const survivor = "33333333-3333-4333-8333-333333333333"
	const survivorDoc = "old-skill/references/survivor.md"
	const originalDetail = "Surviving independently sourced detail."
	n := &dktypes.Node{ID: survivor, Label: dktypes.LabelConcept, Name: "Surviving independent knowledge", Summary: "Surviving summary.", Description: originalDetail, Domain: "engineering", Subdomain: "admission", Confidence: 1, Provenance: dktypes.ProvenanceExtraction}
	if err := st.nodes.Insert(ctx, n); err != nil {
		t.Fatal(err)
	}
	if err := st.sources.InsertBatch(ctx, []storage.NodeSource{{NodeUUID: survivor, MemberID: "survivor-member", Skill: "old-skill", FilePath: survivorDoc, StartLine: 8, EndLine: 9}}); err != nil {
		t.Fatal(err)
	}
	if err := st.edges.Insert(ctx, &dktypes.Edge{SourceID: identityTestNode, TargetID: survivor, Kind: dktypes.KindReferences, Description: "A references independently sourced B.", Confidence: 1, Provenance: dktypes.ProvenanceExtraction}); err != nil {
		t.Fatal(err)
	}
	if _, err := applyIncrementalWriteback(ctx, st, newRunStateWithDocs(identityTestDoc, survivorDoc), nil, nil, root); err != nil {
		t.Fatal(err)
	}
	db.Close()
	o, err := NewIncrementalOrchestrator(Options{Client: identityNoLLM{}, RepositoryIdentity: "git:github.com/owner/a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.Run(ctx, root, dbPath); err != nil {
		t.Fatalf("initial baseline: %v", err)
	}
	if err := os.Remove(filepath.Join(root, identityTestDoc)); err != nil {
		t.Fatal(err)
	}
	report, err := o.Run(ctx, root, dbPath)
	if err != nil {
		t.Fatalf("existing C2 deletion: %+v %v", report, err)
	}
	t.Logf("deletion report: %+v", report)
	db, err = storage.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	edges, err := storage.NewEdgeRepo(db).ListAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, edge := range edges {
		if edge.SourceID == identityTestNode || edge.TargetID == identityTestNode {
			t.Fatalf("fixture edge not deleted: %+v", edge)
		}
	}
	db.Close()
	sc, err := writeback.ReadSidecar(filepath.Join(root, survivorDoc))
	if err != nil {
		t.Fatal(err)
	}
	if len(sc.Nodes) != 1 || sc.Nodes[0].GraphMetadata == nil {
		t.Fatal("missing survivor metadata")
	}
	g := sc.Nodes[0].GraphMetadata
	if !g.RelationsKnown || len(g.Relations) != 0 {
		t.Errorf("successful C2 left stale incident relation snapshot: %+v", g.Relations)
	}
	before, err := os.ReadFile(filepath.Join(root, survivorDoc))
	if err != nil {
		t.Fatal(err)
	}
	const humanDetail = "Human correction: preserve the independent condition after A is removed."
	edited := strings.Replace(string(before), originalDetail, humanDetail, 1)
	if edited == string(before) {
		t.Fatal("fixture human edit not applied")
	}
	if err := os.WriteFile(filepath.Join(root, survivorDoc), []byte(edited), 0644); err != nil {
		t.Fatal(err)
	}
	report, err = o.Run(ctx, root, dbPath)
	if err != nil {
		t.Fatalf("human edit run: %+v %v", report, err)
	}
	t.Logf("later human edit report: %+v", report)
	db, err = storage.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	got, err := storage.NewNodeRepo(db).GetByID(ctx, survivor)
	if err != nil {
		t.Fatal(err)
	}
	if got.Description != humanDetail || got.Provenance != dktypes.ProvenanceHumanCurated {
		t.Errorf("later human edit lost: Description=%q Provenance=%q, C1=%d C3=%d", got.Description, got.Provenance, report.C1Edits, report.C3Violations)
	}
}

// 原失败模式：邻居回写路径未传播；把邻居加 touched 则会误扩大 I-7 语义重算。
// Only writeback docs expand; semantic dirty/touched scopes retain their original meaning.
func TestV015DeleteNeighborBypassScope(t *testing.T) {
	t.Parallel()
	for _, phantom := range []bool{false, true} {
		name := "delete"
		if phantom {
			name = "phantom"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			db, err := storage.NewDB(storage.DBOptions{Path: filepath.Join(t.TempDir(), "kg.db")})
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			st := newStores(db)
			for _, id := range []string{"deleted", "incoming", "outgoing"} {
				if err := st.nodes.Insert(ctx, &dktypes.Node{ID: id, Label: dktypes.LabelConcept, Name: id, Domain: "d", Subdomain: id, Provenance: dktypes.ProvenanceExtraction, Confidence: 1}); err != nil {
					t.Fatal(err)
				}
				if err := st.sources.InsertBatch(ctx, []storage.NodeSource{{NodeUUID: id, MemberID: id, Skill: "one", FilePath: id + ".md"}}); err != nil {
					t.Fatal(err)
				}
			}
			for _, pair := range [][2]string{{"deleted", "outgoing"}, {"incoming", "deleted"}} {
				if err := st.edges.Insert(ctx, &dktypes.Edge{SourceID: pair[0], TargetID: pair[1], Kind: dktypes.KindReferences, Provenance: dktypes.ProvenanceExtraction, Confidence: 1}); err != nil {
					t.Fatal(err)
				}
			}
			if phantom {
				if _, err := db.Conn().Exec(`PRAGMA foreign_keys=OFF`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Conn().Exec(`DELETE FROM nodes WHERE id='deleted'`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Conn().Exec(`PRAGMA foreign_keys=ON`); err != nil {
					t.Fatal(err)
				}
			}
			rs := newRunState()
			if err := applyBypassD(ctx, st, "deleted.md", "deleted", rs); err != nil {
				t.Fatal(err)
			}
			docs := rs.affectedDocsSnapshot()
			if len(docs) != 3 || !docs["deleted.md"] || !docs["incoming.md"] || !docs["outgoing.md"] {
				t.Fatalf("incident writeback docs lost/expanded: %+v", docs)
			}
			wantDirty := 1
			if phantom {
				wantDirty = 0
			}
			if len(rs.touched) != 0 || len(rs.dirty.Subdomains) != wantDirty {
				t.Fatalf("neighbor expanded semantic recomputation: touched=%+v dirty=%+v", rs.touched, rs.dirty.Subdomains)
			}
			if !phantom {
				if _, ok := rs.dirty.Subdomains[SubdomainKey{Domain: "d", Subdomain: "deleted"}]; !ok {
					t.Fatal("original deletion scope lost")
				}
			}
		})
	}
}
