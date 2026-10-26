package incremental

import (
	"context"
	"encoding/json"
	"github.com/xcosmosbox/cairn/build/internal/writeback"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func pendingOwnedPair(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, identityTestDoc)
	md, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sidecar, err := os.ReadFile(path + ".kg.yaml")
	if err != nil {
		t.Fatal(err)
	}
	journal, err := json.Marshal(map[string]any{"md": md, "sidecar": sidecar, "old_md": md, "old_sidecar": sidecar, "had_md": true, "had_sidecar": true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".kg-writeback.json", journal, 0600); err != nil {
		t.Fatal(err)
	}
	return path + ".kg-writeback.json"
}

func TestOwnedJournalRecoveryPrecedesIncrementalDiffAndPreservesUUID(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	dbPath := filepath.Join(root, "kg.db")
	identityFixture(t, repo, dbPath, "git:github.com/owner/a", true)
	journal := pendingOwnedPair(t, repo)
	orchestrator, err := NewIncrementalOrchestrator(Options{Client: identityNoLLM{}, RepositoryIdentity: "git:github.com/owner/a"})
	if err != nil {
		t.Fatal(err)
	}
	report, err := orchestrator.Run(context.Background(), repo, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if report.DocsChanged != 0 {
		t.Fatalf("journal was misclassified as new content: %+v", report)
	}
	if _, err := os.Stat(journal); !os.IsNotExist(err) {
		t.Fatal("owned journal did not recover before diff")
	}
	db, err := storage.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	node, err := storage.NewNodeRepo(db).GetByID(context.Background(), identityTestNode)
	if err != nil || node == nil {
		t.Fatal("original UUID was replaced")
	}
}

func TestRebalanceCheckOnlyLeavesPendingPairUntouched(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	dbPath := filepath.Join(root, "kg.db")
	identityFixture(t, repo, dbPath, "git:github.com/owner/a", true)
	journal := pendingOwnedPair(t, repo)
	before, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	o, err := NewRebalanceOrchestrator(identityNoLLM{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.Run(context.Background(), repo, dbPath, RebalanceRunOpts{CheckOnly: true, RepositoryIdentity: "git:github.com/owner/a"}); err == nil {
		t.Fatal("check-only accepted pending materialization")
	}
	after, err := os.ReadFile(journal)
	if err != nil || string(after) != string(before) {
		t.Fatal("check-only recovered pending writeback")
	}
}

func TestOwnedJournalFailureAndCancellationPreserveHumanFiles(t *testing.T) {
	for _, scenario := range []string{"new-human-edit", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			repo := filepath.Join(root, "repo")
			dbPath := filepath.Join(root, "kg.db")
			identityFixture(t, repo, dbPath, "git:github.com/owner/a", true)
			journal := pendingOwnedPair(t, repo)
			path := filepath.Join(repo, identityTestDoc)
			ctx := context.Background()
			if scenario == "new-human-edit" {
				if err := os.WriteFile(path, []byte("new human prose after crash"), 0644); err != nil {
					t.Fatal(err)
				}
			} else {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			md, _ := os.ReadFile(path)
			sc, _ := os.ReadFile(path + ".kg.yaml")
			pending, _ := os.ReadFile(journal)
			kg, _ := os.ReadFile(dbPath)
			info, _ := os.Stat(path)
			o, err := NewIncrementalOrchestrator(Options{Client: identityNoLLM{}, RepositoryIdentity: "git:github.com/owner/a"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := o.Run(ctx, repo, dbPath); err == nil {
				t.Fatal("failed recovery was accepted")
			}
			afterMD, _ := os.ReadFile(path)
			afterSC, _ := os.ReadFile(path + ".kg.yaml")
			afterJournal, _ := os.ReadFile(journal)
			afterKG, _ := os.ReadFile(dbPath)
			afterInfo, _ := os.Stat(path)
			if string(md) != string(afterMD) || string(sc) != string(afterSC) || string(pending) != string(afterJournal) || !info.ModTime().Equal(afterInfo.ModTime()) {
				t.Fatal("failed/cancelled recovery mutated source")
			}
			if scenario == "cancelled" && string(kg) != string(afterKG) {
				t.Fatal("cancelled run mutated KG")
			}
		})
	}
}

// A conflicting journal used to stop every document before diff or baseline progress.
func TestConflictedJournalKeepsLaterEditAndAdvancesIndependentDocument(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	dbPath := filepath.Join(root, "kg.db")
	const identity = "git:github.com/owner/a"
	identityFixture(t, repo, dbPath, identity, true)
	journal := pendingOwnedPair(t, repo)
	conflict := filepath.Join(repo, identityTestDoc)
	if err := os.WriteFile(conflict, []byte("new human prose after crash"), 0644); err != nil {
		t.Fatal(err)
	}
	beforeJournal, _ := os.ReadFile(journal)
	const goodDoc = "old-skill/references/healthy.md"
	const goodID = "22222222-2222-4222-8222-222222222222"
	db, err := storage.NewDB(storage.DBOptions{Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	node := &dktypes.Node{ID: goodID, Label: dktypes.LabelConcept, Name: "Healthy knowledge", Summary: "Healthy old summary", Description: "Healthy old content", Domain: "engineering", Subdomain: "queues", Provenance: dktypes.ProvenanceExtraction, Confidence: 1}
	if err := storage.NewNodeRepo(db).Insert(ctx, node); err != nil {
		t.Fatal(err)
	}
	if err := storage.NewNodeSourceRepo(db).InsertBatch(ctx, []storage.NodeSource{{NodeUUID: goodID, MemberID: "m2", Skill: "old-skill", FilePath: goodDoc}}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := writeback.RewriteDoc(repo, goodDoc, []writeback.NodeUpdate{{UUID: goodID, Tag: "Concept", Name: node.Name, Summary: node.Summary, Description: node.Description, Domain: node.Domain, Subdomain: node.Subdomain, DomainSlug: node.Domain, SubdomainSlug: node.Subdomain, Members: []string{"m2"}, Provenance: "extraction"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	goodPath := filepath.Join(repo, goodDoc)
	goodBefore, _ := os.ReadFile(goodPath)
	edited := strings.ReplaceAll(string(goodBefore), "Healthy old content", "Healthy edited content")
	if edited == string(goodBefore) {
		t.Fatal("fixture did not edit block")
	}
	if err := os.WriteFile(goodPath, []byte(edited), 0644); err != nil {
		t.Fatal(err)
	}
	o, _ := NewIncrementalOrchestrator(Options{Client: identityNoLLM{}, RepositoryIdentity: identity})
	report, err := o.Run(ctx, repo, dbPath)
	if err == nil || report == nil || report.C1Edits != 1 || report.DocsRewritten == 0 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	after, _ := os.ReadFile(conflict)
	afterJournal, _ := os.ReadFile(journal)
	if string(after) != "new human prose after crash" || string(afterJournal) != string(beforeJournal) {
		t.Fatal("conflict erased later human edit or journal")
	}
	db, err = storage.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	goodState, err := storage.NewFileStateRepo(db).Get(ctx, identity, goodDoc)
	if err != nil || goodState == nil {
		t.Fatalf("healthy baseline did not advance: %v", err)
	}
	badState, err := storage.NewFileStateRepo(db).Get(ctx, identity, identityTestDoc)
	if err != nil || badState != nil {
		t.Fatalf("conflicted baseline advanced: %+v %v", badState, err)
	}
}
