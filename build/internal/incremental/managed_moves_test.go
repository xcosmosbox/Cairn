package incremental

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/writeback"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

const moveTestDoc = "old-skill/references/renamed.md"
const moveTestOther = "old-skill/references/other.md"
const moveTestShared = "22222222-2222-4222-8222-222222222222"

func managedMoveFixture(t *testing.T) (string, string, *IncrementalOrchestrator) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	root, dbPath := filepath.Join(dir, "repo"), filepath.Join(dir, "kg.db")
	identityFixture(t, root, dbPath, "git:github.com/owner/a", true)
	if err := os.WriteFile(filepath.Join(root, "old-skill/SKILL.md"), []byte("---\nname: old-skill\n---\n"), 0644); err != nil {
		t.Fatal(err)
	}
	db, err := storage.NewDB(storage.DBOptions{Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	st := newStores(db)
	shared := &dktypes.Node{ID: moveTestShared, Label: dktypes.LabelConcept, Name: "shared knowledge", Summary: "shared summary", Description: "shared detail", Domain: "engineering", Subdomain: "queues", Confidence: 1, Provenance: dktypes.ProvenanceExtraction}
	if err := st.nodes.Insert(ctx, shared); err != nil {
		t.Fatal(err)
	}
	if err := st.sources.InsertBatch(ctx, []storage.NodeSource{
		{NodeUUID: identityTestNode, MemberID: "m2", Skill: "old-skill", FilePath: identityTestDoc, StartLine: 10, EndLine: 12},
		{NodeUUID: moveTestShared, MemberID: "m3", Skill: "old-skill", FilePath: identityTestDoc, StartLine: 20, EndLine: 25},
		{NodeUUID: moveTestShared, MemberID: "m4", Skill: "old-skill", FilePath: moveTestOther, StartLine: 30, EndLine: 32},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.edges.Insert(ctx, &dktypes.Edge{SourceID: identityTestNode, TargetID: moveTestShared, Kind: dktypes.KindReferences, Confidence: 1, Provenance: dktypes.ProvenanceExtraction}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{identityTestDoc, moveTestOther} {
		views, err := authoritativeSidecarViews(ctx, st, path, false, "")
		if err != nil {
			t.Fatal(err)
		}
		if err := writeback.RewriteDoc(root, path, views, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	views, err := authoritativeSidecarViews(ctx, st, "", true, moveTestShared)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeback.RewritePrimary(root, views[0], time.Now()); err != nil {
		t.Fatal(err)
	}
	db.Close()
	o, err := NewIncrementalOrchestrator(Options{Client: identityNoLLM{}, RepositoryIdentity: "git:github.com/owner/a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.Run(ctx, root, dbPath); err != nil {
		t.Fatal(err)
	}
	return root, dbPath, o
}

func renameManagedPair(t *testing.T, root, newPath string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, newPath)), 0755); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", ".kg.yaml"} {
		if err := os.Rename(filepath.Join(root, identityTestDoc)+suffix, filepath.Join(root, newPath)+suffix); err != nil {
			t.Fatal(err)
		}
	}
}

type moveSnapshot struct {
	Nodes   []*dktypes.Node
	Edges   []*dktypes.Edge
	Sources []storage.NodeSource
	States  []*storage.FileState
	Version string
}

func snapshotMoveLedger(t *testing.T, dbPath string) moveSnapshot {
	t.Helper()
	db, err := storage.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st, ctx := newStores(db), context.Background()
	snapshot := moveSnapshot{}
	if snapshot.Nodes, err = st.nodes.ListAll(ctx); err != nil {
		t.Fatal(err)
	}
	if snapshot.Edges, err = st.edges.ListAll(ctx); err != nil {
		t.Fatal(err)
	}
	if snapshot.Sources, err = st.sources.ListAll(ctx); err != nil {
		t.Fatal(err)
	}
	if snapshot.States, err = st.files.ListByRepo(ctx, "git:github.com/owner/a"); err != nil {
		t.Fatal(err)
	}
	if snapshot.Version, err = st.version.Current(ctx); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestManagedMovePreservesGraphLedgerAndSettlesWithoutLLM(t *testing.T) {
	t.Parallel()
	root, dbPath, o := managedMoveFixture(t)
	before := snapshotMoveLedger(t, dbPath)
	renameManagedPair(t, root, moveTestDoc)
	report, err := o.Run(context.Background(), root, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.DocumentMoves) != 1 || report.DocumentMoves[0].SourceRows != 3 || report.DocumentMoves[0].Recovered || report.C2Deletions != 0 || report.NodesDeleted != 0 || len(report.Warnings) != 0 {
		t.Fatalf("unexpected move result: %+v", report)
	}
	after := snapshotMoveLedger(t, dbPath)
	if !reflect.DeepEqual(before.Nodes, after.Nodes) || !reflect.DeepEqual(before.Edges, after.Edges) || before.Version == after.Version {
		t.Fatal("rename changed node identities/content/edges or did not advance KB version")
	}
	for i := range before.Sources {
		if before.Sources[i].FilePath == identityTestDoc {
			before.Sources[i].FilePath = moveTestDoc
		}
	}
	if !reflect.DeepEqual(before.Sources, after.Sources) {
		t.Fatalf("source members/skills/spans changed: before=%+v after=%+v", before.Sources, after.Sources)
	}
	foundNew := false
	for _, state := range after.States {
		if state.FilePath == identityTestDoc {
			t.Fatal("old file_state survived move")
		}
		if state.FilePath == moveTestDoc {
			foundNew = true
		}
	}
	if !foundNew {
		t.Fatal("new file_state is missing")
	}
	sc, err := writeback.ReadSidecar(filepath.Join(root, moveTestDoc))
	if err != nil || sc.Doc != moveTestDoc {
		t.Fatalf("sidecar was not rebound: %+v %v", sc, err)
	}
	settled, err := o.Run(context.Background(), root, dbPath)
	if err != nil || settled.DocsChanged != 0 || settled.DocsRewritten != 0 || len(settled.DocumentMoves) != 0 {
		t.Fatalf("second run did not settle: %+v %v", settled, err)
	}
	stable := snapshotMoveLedger(t, dbPath)
	// Ordinary syncFileStates updates timestamps, but no graph or baseline hash.
	if !reflect.DeepEqual(after.Nodes, stable.Nodes) || !reflect.DeepEqual(after.Edges, stable.Edges) || !reflect.DeepEqual(after.Sources, stable.Sources) || after.Version != stable.Version {
		t.Fatal("settled move changed knowledge on a second run")
	}
}

func TestManagedMoveRejectsAmbiguityBeforeDeletingAnything(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"two destinations", "copy instead of move", "missing sidecar", "foreign member", "simultaneous edit", "missing origin proof", "empty nodes and missing origin proof", "cross skill", "occupied destination"} {
		t.Run(scenario, func(t *testing.T) {
			root, dbPath, o := managedMoveFixture(t)
			newPath := moveTestDoc
			if scenario == "cross skill" {
				newPath = "other-skill/references/renamed.md"
				if err := os.MkdirAll(filepath.Join(root, "other-skill"), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "other-skill/SKILL.md"), []byte("---\nname: other-skill\n---\n"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			renameManagedPair(t, root, newPath)
			path := filepath.Join(root, newPath)
			switch scenario {
			case "two destinations", "copy instead of move":
				copyPath := "old-skill/references/second.md"
				if scenario == "copy instead of move" {
					copyPath = identityTestDoc
				}
				for _, suffix := range []string{"", ".kg.yaml"} {
					data, err := os.ReadFile(path + suffix)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(root, copyPath)+suffix, data, 0644); err != nil {
						t.Fatal(err)
					}
				}
			case "missing sidecar":
				if err := os.Remove(path + ".kg.yaml"); err != nil {
					t.Fatal(err)
				}
			case "foreign member", "missing origin proof", "empty nodes and missing origin proof":
				sc, err := writeback.ReadSidecar(path)
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "foreign member" {
					sc.Nodes[0].Members = []string{"foreign-member"}
				} else {
					sc.Doc = newPath
					if scenario == "empty nodes and missing origin proof" {
						sc.Nodes = sc.Nodes[:0]
					}
				}
				if err := writeback.WriteSidecar(path, *sc); err != nil {
					t.Fatal(err)
				}
			case "simultaneous edit":
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, append(data, []byte("\nnew human prose\n")...), 0644); err != nil {
					t.Fatal(err)
				}
			case "occupied destination":
				db, err := storage.NewDB(storage.DBOptions{Path: dbPath})
				if err != nil {
					t.Fatal(err)
				}
				if err := storage.NewFileStateRepo(db).Upsert(context.Background(), "git:github.com/owner/a", newPath, "already-owned"); err != nil {
					t.Fatal(err)
				}
				db.Close()
			}
			before := snapshotMoveLedger(t, dbPath)
			md, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if report, err := o.Run(context.Background(), root, dbPath); err == nil {
				t.Fatalf("unproven move accepted: %+v", report)
			}
			after := snapshotMoveLedger(t, dbPath)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("rejected move mutated graph, source ledger, file_states or version")
			}
			afterMD, err := os.ReadFile(path)
			if err != nil || string(md) != string(afterMD) {
				t.Fatal("rejected move overwrote human document")
			}
		})
	}
}

func TestManagedMoveResumesAfterLedgerCommitBeforeSidecarWrite(t *testing.T) {
	t.Parallel()
	for _, editAfterCrash := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged", true: "new human edit"}[editAfterCrash], func(t *testing.T) {
			root, dbPath, o := managedMoveFixture(t)
			renameManagedPair(t, root, moveTestDoc)
			db, err := storage.NewDB(storage.DBOptions{Path: dbPath})
			if err != nil {
				t.Fatal(err)
			}
			st := newStores(db)
			_, scanned, err := o.detect(context.Background(), st, root, "git:github.com/owner/a")
			if err != nil || len(scanned.moves) != 1 {
				t.Fatalf("move not planned: %+v %v", scanned, err)
			}
			if err := st.mutations.MoveSourceDocuments(context.Background(), "git:github.com/owner/a", []storage.SourceDocumentMove{scanned.moves[0].SourceDocumentMove}); err != nil {
				t.Fatal(err)
			}
			db.Close() // simulated interruption before the sidecar Doc is rebound
			path := filepath.Join(root, moveTestDoc)
			if editAfterCrash {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, append(data, []byte("\npost-crash human prose\n")...), 0644); err != nil {
					t.Fatal(err)
				}
			}
			before := snapshotMoveLedger(t, dbPath)
			report, err := o.Run(context.Background(), root, dbPath)
			if editAfterCrash {
				if err == nil {
					t.Fatal("post-crash edit was not protected")
				}
				data, _ := os.ReadFile(path)
				if !strings.Contains(string(data), "post-crash human prose") {
					t.Fatal("post-crash human edit was discarded")
				}
			} else if err != nil || len(report.DocumentMoves) != 1 || !report.DocumentMoves[0].Recovered || report.NodesDeleted != 0 {
				t.Fatalf("move recovery failed: %+v %v", report, err)
			}
			after := snapshotMoveLedger(t, dbPath)
			if !reflect.DeepEqual(before.Nodes, after.Nodes) || !reflect.DeepEqual(before.Sources, after.Sources) || before.Version != after.Version {
				t.Fatal("recovery changed knowledge or repeated the ledger migration")
			}
		})
	}
}
