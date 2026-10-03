package incremental

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/build/internal/writeback"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/repoidentity"
	"github.com/xcosmosbox/cairn/core/storage"
)

type identityNoLLM struct{}

func (identityNoLLM) ProviderName() string { return "identity-test" }
func (identityNoLLM) Complete(context.Context, llm.CompleteRequest) (*llm.CompleteResponse, error) {
	return nil, errors.New("unexpected LLM call")
}

const identityTestNode = "11111111-1111-4111-8111-111111111111"
const identityTestDoc = "old-skill/references/owned.md"

func identityFixture(t *testing.T, repo, dbPath, identity string, sidecar bool) {
	t.Helper()
	ctx := context.Background()
	if err := os.MkdirAll(repo, 0755); err != nil {
		t.Fatal(err)
	}
	db, err := storage.NewDB(storage.DBOptions{Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	node := &dktypes.Node{ID: identityTestNode, Label: dktypes.LabelConcept, Name: "A-owned knowledge", Domain: "engineering", Subdomain: "queues", Provenance: dktypes.ProvenanceExtraction, Confidence: 1}
	if err := storage.NewNodeRepo(db).Insert(ctx, node); err != nil {
		t.Fatal(err)
	}
	if err := storage.NewNodeSourceRepo(db).InsertBatch(ctx, []storage.NodeSource{{NodeUUID: identityTestNode, MemberID: "m1", Skill: "old-skill", FilePath: identityTestDoc}}); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.NewKBVersionRepo(db).Bump(ctx); err != nil {
		t.Fatal(err)
	}
	if identity != "" {
		if err := storage.NewRepositoryIdentityRepo(db).Set(ctx, identity); err != nil {
			t.Fatal(err)
		}
	}
	if sidecar {
		if err := writeback.RewriteDoc(repo, identityTestDoc, []writeback.NodeUpdate{{UUID: identityTestNode, Tag: "Concept", Name: node.Name, Domain: node.Domain, Subdomain: node.Subdomain, DomainSlug: node.Domain, SubdomainSlug: node.Subdomain, Members: []string{"m1"}, Provenance: "extraction"}}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
}

// A's source ledger previously became C2 deletions when paired with empty B.
func TestWrongOrUnprovenRepositoryDoesNotDeleteKnowledge(t *testing.T) {
	t.Parallel()
	for _, stored := range []string{"", "git:github.com/owner/a"} {
		for _, adopt := range []bool{false, true} {
			t.Run(stored+"/adopt="+fmt.Sprint(adopt), func(t *testing.T) {
				dir := t.TempDir()
				repoA, repoB := filepath.Join(dir, "A"), filepath.Join(dir, "B")
				dbPath := filepath.Join(dir, "knowledge.db")
				identityFixture(t, repoA, dbPath, stored, true)
				if err := os.Mkdir(repoB, 0755); err != nil {
					t.Fatal(err)
				}
				before, err := os.ReadFile(dbPath)
				if err != nil {
					t.Fatal(err)
				}
				info, err := os.Stat(dbPath)
				if err != nil {
					t.Fatal(err)
				}
				o, err := NewIncrementalOrchestrator(Options{Client: identityNoLLM{}, RepositoryIdentity: "git:github.com/owner/b", AdoptLegacy: adopt})
				if err != nil {
					t.Fatal(err)
				}
				if rpt, err := o.Run(context.Background(), repoB, dbPath); err == nil {
					t.Fatalf("wrong repo accepted: %+v", rpt)
				}
				after, err := os.ReadFile(dbPath)
				if err != nil {
					t.Fatal(err)
				}
				afterInfo, err := os.Stat(dbPath)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(before, after) || !info.ModTime().Equal(afterInfo.ModTime()) {
					t.Fatal("rejected wrong repo modified database")
				}
			})
		}
	}
}

func TestLegacyAdoptionRequiresAllSourceProofAndPersistsIdentity(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	repo, dbPath := filepath.Join(dir, "A"), filepath.Join(dir, "knowledge.db")
	identityFixture(t, repo, dbPath, "", true)
	o, err := NewIncrementalOrchestrator(Options{Client: identityNoLLM{}, AdoptLegacy: true})
	if err != nil {
		t.Fatal(err)
	}
	if rpt, err := o.Run(context.Background(), repo, dbPath); err != nil {
		t.Fatalf("valid legacy adoption failed: %v %+v", err, rpt)
	}
	identity, err := repoidentity.Resolve(context.Background(), repo, "")
	if err != nil {
		t.Fatal(err)
	}
	db, err := storage.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := storage.NewRepositoryIdentityRepo(db).Get(context.Background())
	db.Close()
	if err != nil || stored != identity {
		t.Fatalf("identity not persisted: %s %s %v", stored, identity, err)
	}
	// After adoption, deleting the owned document is a legitimate C2 operation.
	if err := os.Remove(filepath.Join(repo, identityTestDoc)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repo, identityTestDoc) + ".kg.yaml"); err != nil {
		t.Fatal(err)
	}
	o, err = NewIncrementalOrchestrator(Options{Client: identityNoLLM{}})
	if err != nil {
		t.Fatal(err)
	}
	rpt, err := o.Run(context.Background(), repo, dbPath)
	if err != nil || rpt.NodesDeleted != 1 {
		t.Fatalf("owned deletion failed: %v %+v", err, rpt)
	}
}

func TestLegacyAdoptionRejectsPartialSidecarIntersection(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	repo, dbPath := filepath.Join(dir, "A"), filepath.Join(dir, "knowledge.db")
	identityFixture(t, repo, dbPath, "", true)
	db, err := storage.NewDB(storage.DBOptions{Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.NewNodeSourceRepo(db).InsertBatch(context.Background(), []storage.NodeSource{{NodeUUID: identityTestNode, MemberID: "missing-member", Skill: "other", FilePath: "other/references/missing.md"}}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	o, err := NewIncrementalOrchestrator(Options{Client: identityNoLLM{}, RepositoryIdentity: "git:github.com/owner/a", AdoptLegacy: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.Run(context.Background(), repo, dbPath); err == nil {
		t.Fatal("partial intersection accepted")
	}
}

func TestRebalanceCheckPreservesDatabaseBytesAndVersion(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	repo, dbPath := filepath.Join(dir, "A"), filepath.Join(dir, "knowledge.db")
	identityFixture(t, repo, dbPath, "git:github.com/owner/a", true)
	before, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	o, err := NewRebalanceOrchestrator(identityNoLLM{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.Run(context.Background(), repo, dbPath, RebalanceRunOpts{CheckOnly: true, RepositoryIdentity: "git:github.com/owner/a"}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Stat(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || !info.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatal("check-only changed DB")
	}
}

func TestNestedRepositoryRootCannotDeleteParentKnowledge(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	repo, dbPath := filepath.Join(dir, "repo"), filepath.Join(dir, "knowledge.db")
	identityFixture(t, repo, dbPath, "git:github.com/owner/a", true)
	for _, args := range [][]string{{"init", repo}, {"-C", repo, "remote", "add", "origin", "https://github.com/owner/a.git"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	subdir := filepath.Join(repo, "other-subdir")
	if err := os.Mkdir(subdir, 0755); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	o, err := NewIncrementalOrchestrator(Options{Client: identityNoLLM{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.Run(context.Background(), subdir, dbPath); err == nil {
		t.Fatal("nested scan root accepted parent KG")
	}
	after, err := os.ReadFile(dbPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("nested scan root changed parent KG")
	}
}

func TestLegacyAdoptionRejectsSymlinkedProof(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	repoA, repoB, dbPath := filepath.Join(dir, "A"), filepath.Join(dir, "B"), filepath.Join(dir, "knowledge.db")
	identityFixture(t, repoA, dbPath, "", true)
	if err := os.MkdirAll(filepath.Join(repoB, filepath.Dir(identityTestDoc)), 0755); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", ".kg.yaml"} {
		if err := os.Symlink(filepath.Join(repoA, identityTestDoc)+suffix, filepath.Join(repoB, identityTestDoc)+suffix); err != nil {
			t.Fatal(err)
		}
	}
	before, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	o, err := NewIncrementalOrchestrator(Options{Client: identityNoLLM{}, RepositoryIdentity: "git:github.com/owner/b", AdoptLegacy: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.Run(context.Background(), repoB, dbPath); err == nil {
		t.Fatal("symlink proof accepted")
	}
	after, err := os.ReadFile(dbPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("rejected symlink proof changed KG")
	}
}

func TestStampedKnowledgeRefusesUnsafeLedgerPathsBeforeMutation(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"source-traversal", "source-dot", "file-ledger-traversal", "source-symlink", "sidecar-symlink", "parent-symlink", "primary-symlink"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			repo, dbPath := filepath.Join(dir, "repo"), filepath.Join(dir, "knowledge.db")
			identity := "git:github.com/owner/a"
			identityFixture(t, repo, dbPath, identity, true)
			outside := filepath.Join(dir, "outside.md")
			expected := []byte("outside must remain unchanged")
			if err := os.WriteFile(outside, expected, 0644); err != nil {
				t.Fatal(err)
			}
			db, err := storage.NewDB(storage.DBOptions{Path: dbPath})
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "source-traversal", "source-dot":
				badPath := "../outside.md"
				if scenario == "source-dot" {
					badPath = "."
				}
				if err := storage.NewNodeSourceRepo(db).InsertBatch(context.Background(), []storage.NodeSource{{NodeUUID: identityTestNode, MemberID: "bad", Skill: "other", FilePath: badPath}}); err != nil {
					t.Fatal(err)
				}
			case "file-ledger-traversal":
				if err := storage.NewFileStateRepo(db).Upsert(context.Background(), identity, "../outside.md", "old"); err != nil {
					t.Fatal(err)
				}
			case "source-symlink", "sidecar-symlink":
				path := filepath.Join(repo, identityTestDoc)
				if scenario == "sidecar-symlink" {
					path += ".kg.yaml"
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, path); err != nil {
					t.Fatal(err)
				}
			case "parent-symlink":
				refs := filepath.Join(repo, filepath.Dir(identityTestDoc))
				moved := filepath.Join(dir, "external-references")
				if err := os.Rename(refs, moved); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(moved, refs); err != nil {
					t.Fatal(err)
				}
			case "primary-symlink":
				if err := storage.NewNodeSourceRepo(db).InsertBatch(context.Background(), []storage.NodeSource{{NodeUUID: identityTestNode, MemberID: "other", Skill: "other", FilePath: "other/references/second.md"}}); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(dir, filepath.Join(repo, "_shared")); err != nil {
					t.Fatal(err)
				}
			}
			db.Close()
			before, err := os.ReadFile(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			o, err := NewIncrementalOrchestrator(Options{Client: identityNoLLM{}, RepositoryIdentity: identity})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := o.Run(context.Background(), repo, dbPath); err == nil {
				t.Fatal("unsafe ledger accepted")
			}
			after, err := os.ReadFile(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			afterInfo, err := os.Stat(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) || !info.ModTime().Equal(afterInfo.ModTime()) {
				t.Fatal("unsafe ledger mutated KG before refusal")
			}
			actual, err := os.ReadFile(outside)
			if err != nil || !bytes.Equal(actual, expected) {
				t.Fatal("outside file changed")
			}
		})
	}
}

func TestUnsafePrimaryCleanupDoesNotDeleteOutsideArtifacts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	repo, external := filepath.Join(dir, "repo"), filepath.Join(dir, "external")
	if err := os.Mkdir(repo, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(external, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(repo, "_shared")); err != nil {
		t.Fatal(err)
	}
	uuid := identityTestNode
	path := filepath.Join(external, "domain", uuid+".md")
	if err := os.Mkdir(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("outside"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := deletePrimaryArtifacts(repo, "domain", uuid, nil); err == nil {
		t.Fatal("symlink cleanup succeeded")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "outside" {
		t.Fatal("cleanup removed outside primary")
	}
}
