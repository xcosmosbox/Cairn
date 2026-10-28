package incremental

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/writeback"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

const v015DeleteIdentity = "git:github.com/owner/a"
const v015DeleteIncoming = "33333333-3333-4333-8333-333333333333"
const v015DeleteShared = "44444444-4444-4444-8444-444444444444"
const v015DeleteHuman = "55555555-5555-4555-8555-555555555555"
const v015DeleteClean = "66666666-6666-4666-8666-666666666666"
const v015DeleteIncomingDoc = "old-skill/references/incoming.md"
const v015DeleteSharedDoc = "old-skill/references/shared-one.md"
const v015DeleteMirrorDoc = "old-skill/references/shared-two.md"
const v015DeleteHumanDoc = "old-skill/references/human.md"
const v015DeleteCleanDoc = "old-skill/references/clean.md"

type v015DeleteAcceptanceFixture struct {
	root, dbPath string
	o            *IncrementalOrchestrator
	nodes        map[string]*dktypes.Node
	sources      map[string][]storage.NodeSource
}

func v015DeleteAcceptanceSetup(t *testing.T) *v015DeleteAcceptanceFixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	f := &v015DeleteAcceptanceFixture{root: filepath.Join(dir, "repo"), dbPath: filepath.Join(dir, "kg.db"), nodes: map[string]*dktypes.Node{}, sources: map[string][]storage.NodeSource{}}
	identityFixture(t, f.root, f.dbPath, v015DeleteIdentity, true)
	if err := os.WriteFile(filepath.Join(f.root, "old-skill/SKILL.md"), []byte("---\nname: old-skill\n---\n"), 0644); err != nil {
		t.Fatal(err)
	}
	db, err := storage.NewDB(storage.DBOptions{Path: f.dbPath})
	if err != nil {
		t.Fatal(err)
	}
	st := newStores(db)
	for _, n := range []*dktypes.Node{
		{ID: "skill::old-skill", Label: dktypes.LabelSkill, Name: "old-skill", Provenance: dktypes.ProvenanceExtraction, Confidence: 1},
		{ID: "domain::engineering", Label: dktypes.LabelDomain, Name: "engineering", Domain: "engineering", Provenance: dktypes.ProvenanceExtraction, Confidence: 1},
		{ID: "subdomain::engineering::queues", Label: dktypes.LabelSubdomain, Name: "queues", Domain: "engineering", Subdomain: "queues", Provenance: dktypes.ProvenanceExtraction, Confidence: 1},
		{ID: "subdomain::engineering::admission", Label: dktypes.LabelSubdomain, Name: "admission", Domain: "engineering", Subdomain: "admission", Provenance: dktypes.ProvenanceExtraction, Confidence: 1},
	} {
		if err := st.nodes.Insert(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range []struct {
		id, name string
		docs     []string
		human    bool
	}{
		{v015DeleteIncoming, "Independent incoming", []string{v015DeleteIncomingDoc}, false},
		{v015DeleteShared, "Shared outgoing", []string{v015DeleteSharedDoc, v015DeleteMirrorDoc}, false},
		{v015DeleteHuman, "Curated neighbor", []string{v015DeleteHumanDoc}, true},
		{v015DeleteClean, "Unrelated clean", []string{v015DeleteCleanDoc}, false},
	} {
		n := &dktypes.Node{ID: v.id, Label: dktypes.LabelConcept, Name: v.name, Summary: v.name + " summary.", Description: v.name + " complete detail.", Domain: "engineering", Subdomain: "admission", Confidence: 1, Provenance: dktypes.ProvenanceExtraction, SourceRefs: "old-skill"}
		if v.human {
			n.Provenance = dktypes.ProvenanceHumanCurated
		}
		if err := st.nodes.Insert(ctx, n); err != nil {
			t.Fatal(err)
		}
		for i, doc := range v.docs {
			if err := st.sources.InsertBatch(ctx, []storage.NodeSource{{NodeUUID: v.id, MemberID: v.id + string(rune('a'+i)), Skill: "old-skill", FilePath: doc, StartLine: 8 + i*10, EndLine: 9 + i*10}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	// A's subdomain becomes empty on deletion, so this checks existing zero-model C2.
	// Incoming/outgoing and a curated endpoint must all refresh; a retained edge must survive.
	for _, e := range []*dktypes.Edge{
		{SourceID: v015DeleteIncoming, TargetID: identityTestNode, Kind: dktypes.KindReferences, Description: "incoming to removed endpoint", Provenance: dktypes.ProvenanceExtraction, Confidence: 1},
		{SourceID: identityTestNode, TargetID: v015DeleteShared, Kind: dktypes.KindReferences, Description: "removed endpoint to shared survivor", Provenance: dktypes.ProvenanceExtraction, Confidence: 1},
		{SourceID: v015DeleteHuman, TargetID: identityTestNode, Kind: dktypes.KindReferences, Description: "machine edge to curated endpoint", Provenance: dktypes.ProvenanceExtraction, Confidence: 1},
		{SourceID: v015DeleteIncoming, TargetID: v015DeleteShared, Kind: dktypes.KindDependsOn, Description: "retained uncertain condition", Properties: `{"condition":"scheduled"}`, SourceRefs: "original-source", Provenance: dktypes.ProvenanceLLMInferred, Confidence: 0, Cardinality: "N:1", Bidirectional: 1},
	} {
		if err := st.edges.Insert(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	docs := []string{identityTestDoc, v015DeleteIncomingDoc, v015DeleteSharedDoc, v015DeleteMirrorDoc, v015DeleteHumanDoc, v015DeleteCleanDoc}
	if _, err := applyIncrementalWriteback(ctx, st, newRunStateWithDocs(docs...), &alignOutcome{pending: map[string]*pendingNode{v015DeleteShared: {}}}, nil, f.root); err != nil {
		t.Fatal(err)
	}
	db.Close()
	f.o, err = NewIncrementalOrchestrator(Options{Client: identityNoLLM{}, RepositoryIdentity: v015DeleteIdentity})
	if err != nil {
		t.Fatal(err)
	}
	rpt, err := f.o.Run(ctx, f.root, f.dbPath)
	if err != nil || rpt.DocsChanged != 0 || rpt.C3Violations != 0 {
		t.Fatalf("initial baseline %+v: %v", rpt, err)
	}
	db, err = storage.OpenReadOnly(f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, id := range []string{v015DeleteIncoming, v015DeleteShared, v015DeleteHuman, v015DeleteClean} {
		f.nodes[id], err = storage.NewNodeRepo(db).GetByID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		f.sources[id], err = storage.NewNodeSourceRepo(db).ListByNode(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func v015DeleteAcceptanceRead(t *testing.T, root, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (f *v015DeleteAcceptanceFixture) checkGraph(t *testing.T) {
	t.Helper()
	db, err := storage.OpenReadOnly(f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	gone, err := storage.NewNodeRepo(db).GetByID(ctx, identityTestNode)
	if err != nil || gone != nil {
		t.Fatalf("endpoint remains: %+v %v", gone, err)
	}
	edges, err := storage.NewEdgeRepo(db).ListAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	retained := false
	for _, e := range edges {
		if e.SourceID == identityTestNode || e.TargetID == identityTestNode {
			t.Fatalf("dangling edge %+v", e)
		}
		if e.Description == "retained uncertain condition" {
			retained = e.Confidence == 0 && e.Properties == `{"condition":"scheduled"}` && e.SourceRefs == "original-source" && e.Cardinality == "N:1" && e.Bidirectional == 1
		}
	}
	if !retained {
		t.Fatal("unrelated full-field edge changed")
	}
	for _, v := range []struct {
		id   string
		docs []string
	}{
		{v015DeleteIncoming, []string{v015DeleteIncomingDoc}},
		{v015DeleteShared, []string{v015DeleteSharedDoc, v015DeleteMirrorDoc, writeback.PrimaryRelPath("engineering", "", v015DeleteShared)}},
		{v015DeleteHuman, []string{v015DeleteHumanDoc}},
	} {
		for _, doc := range v.docs {
			sc, err := writeback.ReadSidecar(filepath.Join(f.root, doc))
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, n := range sc.Nodes {
				if n.UUID != v.id {
					continue
				}
				found = true
				if n.GraphMetadata == nil || !n.GraphMetadata.RelationsKnown {
					t.Fatalf("missing exact relation context in %s", doc)
				}
				want := writeback.IncidentRelations(edges, v.id)
				if len(n.GraphMetadata.Relations) != len(want) {
					t.Fatalf("stale relations in %s: %+v want %+v", doc, n.GraphMetadata.Relations, want)
				}
				for i, r := range n.GraphMetadata.Relations {
					if !reflect.DeepEqual(r.Edge(), *want[i]) {
						t.Fatalf("relation fields changed in %s", doc)
					}
				}
			}
			if !found {
				t.Fatalf("missing survivor in %s", doc)
			}
		}
	}
	for id, want := range f.nodes {
		got, err := storage.NewNodeRepo(db).GetByID(ctx, id)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("survivor node %s changed: %+v != %+v (%v)", id, got, want, err)
		}
		srcs, err := storage.NewNodeSourceRepo(db).ListByNode(ctx, id)
		if err != nil || !reflect.DeepEqual(srcs, f.sources[id]) {
			t.Fatalf("original source ledger %s changed: %+v (%v)", id, srcs, err)
		}
	}
}

func (f *v015DeleteAcceptanceFixture) humanEditAndRepeat(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	for _, v := range []struct{ id, doc string }{{v015DeleteIncoming, v015DeleteIncomingDoc}, {v015DeleteShared, writeback.PrimaryRelPath("engineering", "", v015DeleteShared)}} {
		before := v015DeleteAcceptanceRead(t, f.root, v.doc)
		detail := "Human correction for " + v.id + " after endpoint deletion."
		edited := strings.Replace(string(before), f.nodes[v.id].Description, detail, 1)
		if edited == string(before) {
			t.Fatal("human edit fixture unchanged")
		}
		if err := os.WriteFile(filepath.Join(f.root, v.doc), []byte(edited), 0644); err != nil {
			t.Fatal(err)
		}
		rpt, err := f.o.Run(ctx, f.root, f.dbPath)
		if err != nil || rpt.C1Edits != 1 || rpt.C3Violations != 0 {
			t.Fatalf("later human edit %+v: %v", rpt, err)
		}
		db, err := storage.OpenReadOnly(f.dbPath)
		if err != nil {
			t.Fatal(err)
		}
		n, err := storage.NewNodeRepo(db).GetByID(ctx, v.id)
		db.Close()
		if err != nil || n.Description != detail || n.Provenance != dktypes.ProvenanceHumanCurated {
			t.Fatalf("human correction lost: %+v %v", n, err)
		}
	}
	snapshots := map[string][]byte{}
	for _, doc := range []string{v015DeleteIncomingDoc, v015DeleteSharedDoc, v015DeleteMirrorDoc, writeback.PrimaryRelPath("engineering", "", v015DeleteShared), v015DeleteHumanDoc, v015DeleteCleanDoc} {
		for _, suffix := range []string{"", ".kg.yaml"} {
			snapshots[doc+suffix] = v015DeleteAcceptanceRead(t, f.root, doc+suffix)
		}
	}
	rpt, err := f.o.Run(ctx, f.root, f.dbPath)
	if err != nil || rpt.DocsChanged != 0 || rpt.C1Edits != 0 || rpt.C3Violations != 0 || rpt.DocsRewritten != 0 || rpt.PrimariesWritten != 0 {
		t.Fatalf("repeat did not settle %+v: %v", rpt, err)
	}
	for path, before := range snapshots {
		if !bytes.Equal(before, v015DeleteAcceptanceRead(t, f.root, path)) {
			t.Fatalf("clean repeat changed %s", path)
		}
	}
}

// 删除端点原先只标记已删文件，独立 incoming/outgoing 邻居保留死边快照，
// 导致后续人工 Description 被当 C3 覆盖。实际 Run 必须刷新所有邻居且不改人工/干净数据。
// Deletion must refresh independent and shared survivors before their next human edit.
func TestV015DeleteNeighborAcceptanceRun(t *testing.T) {
	t.Parallel()
	f := v015DeleteAcceptanceSetup(t)
	clean := map[string][]byte{}
	for _, suffix := range []string{"", ".kg.yaml"} {
		clean[suffix] = v015DeleteAcceptanceRead(t, f.root, v015DeleteCleanDoc+suffix)
	}
	if err := os.Remove(filepath.Join(f.root, identityTestDoc)); err != nil {
		t.Fatal(err)
	}
	rpt, err := f.o.Run(context.Background(), f.root, f.dbPath)
	if err != nil || rpt.NodesDeleted != 1 || rpt.C2Deletions != 1 || rpt.C3Violations != 0 {
		t.Fatalf("deletion %+v: %v", rpt, err)
	}
	f.checkGraph(t)
	for suffix, before := range clean {
		if !bytes.Equal(before, v015DeleteAcceptanceRead(t, f.root, v015DeleteCleanDoc+suffix)) {
			t.Fatal("unrelated clean pair changed")
		}
	}
	f.humanEditAndRepeat(t)
}

// 已提交的删除若遇邻居 sidecar 写失败，不能把失败后的合法人工编辑当旧元数据篡改。
// A postcommit survivor write failure must report its scope, retain its baseline,
// and preserve a human edit made before the next ordinary Run retries publication.
func TestV015DeleteNeighborAcceptanceFailureThenHumanEdit(t *testing.T) {
	t.Parallel()
	f := v015DeleteAcceptanceSetup(t)
	ctx := context.Background()
	db, err := storage.NewDB(storage.DBOptions{Path: f.dbPath})
	if err != nil {
		t.Fatal(err)
	}
	// A distinct old timestamp makes a silently advanced baseline observable.
	if _, err := db.Conn().ExecContext(ctx, `UPDATE file_states SET indexed_at='2001-01-01T00:00:00Z' WHERE repo_url=? AND file_path=?`, v015DeleteIdentity, v015DeleteIncomingDoc); err != nil {
		t.Fatal(err)
	}
	oldState, err := storage.NewFileStateRepo(db).Get(ctx, v015DeleteIdentity, v015DeleteIncomingDoc)
	db.Close()
	if err != nil || oldState == nil {
		t.Fatalf("baseline %+v %v", oldState, err)
	}
	sidecar := filepath.Join(f.root, v015DeleteIncomingDoc) + ".kg.yaml"
	oldSidecar := v015DeleteAcceptanceRead(t, f.root, v015DeleteIncomingDoc+".kg.yaml")
	oldMD := v015DeleteAcceptanceRead(t, f.root, v015DeleteIncomingDoc)
	f.o.beforeWriteback = func(context.Context) error {
		if err := os.Remove(sidecar); err != nil {
			return err
		}
		return os.Mkdir(sidecar, 0700)
	}
	if err := os.Remove(filepath.Join(f.root, identityTestDoc)); err != nil {
		t.Fatal(err)
	}
	rpt, runErr := f.o.Run(ctx, f.root, f.dbPath)
	f.o.beforeWriteback = nil
	if runErr == nil || rpt == nil || rpt.NodesDeleted != 1 {
		t.Fatalf("postcommit write failure not reported: %+v %v", rpt, runErr)
	}
	if !bytes.Equal(oldMD, v015DeleteAcceptanceRead(t, f.root, v015DeleteIncomingDoc)) {
		t.Fatal("failed sidecar refresh changed usable Markdown")
	}
	db, err = storage.OpenReadOnly(f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	state, err := storage.NewFileStateRepo(db).Get(ctx, v015DeleteIdentity, v015DeleteIncomingDoc)
	db.Close()
	if err != nil || !reflect.DeepEqual(state, oldState) {
		t.Fatalf("failed baseline advanced: %+v != %+v (%v)", state, oldState, err)
	}
	if err := os.Remove(sidecar); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sidecar, oldSidecar, 0644); err != nil {
		t.Fatal(err)
	}
	// The restored prior sidecar still proves the exact same UUID/member/source,
	// readonly node fields and original editable hashes. Only its derived incident
	// relation cache is stale; this is not an identity-ambiguous retry fixture.
	db, err = storage.OpenReadOnly(f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	st := newStores(db)
	names, err := loadLayerDisplayNames(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	updates, err := buildDocNodeUpdates(ctx, st, v015DeleteIncomingDoc, names)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	proof, err := writeback.ReadSidecar(filepath.Join(f.root, v015DeleteIncomingDoc))
	if err != nil {
		t.Fatal(err)
	}
	if writeback.SidecarMetadataMatches(*proof, v015DeleteIncomingDoc, updates) {
		t.Fatal("fixture no longer has a stale incident cache")
	}
	for i := range proof.Nodes {
		proof.Nodes[i].GraphMetadata.RelationsKnown = false
		proof.Nodes[i].GraphMetadata.Relations = nil
	}
	if !writeback.SidecarMetadataMatches(*proof, v015DeleteIncomingDoc, updates) {
		t.Fatal("restored prior sidecar differs in fields beyond the incident cache")
	}
	const humanDetail = "Human correction while deletion writeback is pending."
	edited := strings.Replace(string(oldMD), f.nodes[v015DeleteIncoming].Description, humanDetail, 1)
	if edited == string(oldMD) {
		t.Fatal("fixture did not change Description")
	}
	if err := os.WriteFile(filepath.Join(f.root, v015DeleteIncomingDoc), []byte(edited), 0644); err != nil {
		t.Fatal(err)
	}
	rpt, err = f.o.Run(ctx, f.root, f.dbPath)
	if err != nil {
		t.Fatalf("retry %+v: %v", rpt, err)
	}
	db, err = storage.OpenReadOnly(f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	n, err := storage.NewNodeRepo(db).GetByID(ctx, v015DeleteIncoming)
	db.Close()
	if err != nil || n.Description != humanDetail || n.Provenance != dktypes.ProvenanceHumanCurated || rpt.C1Edits != 1 {
		t.Fatalf("pending human edit lost on retry: node=%+v report=%+v err=%v", n, rpt, err)
	}
	// Retried metadata must converge even though the deleted endpoint is gone.
	f.nodes[v015DeleteIncoming] = n
	f.checkGraph(t)
	again, err := f.o.Run(ctx, f.root, f.dbPath)
	if err != nil || again.DocsChanged != 0 || again.C3Violations != 0 {
		t.Fatalf("failed-run recovery did not settle: %+v %v", again, err)
	}
}

// 同轮 C1 人工内容先入库，C2 拓扑失效随后发生；邻居刷新只能更新关系快照。
// A human edit already pending in the same Run must survive deletion publication.
func TestV015DeleteNeighborAcceptancePendingHumanEdit(t *testing.T) {
	t.Parallel()
	f := v015DeleteAcceptanceSetup(t)
	primary := writeback.PrimaryRelPath("engineering", "", v015DeleteShared)
	before := v015DeleteAcceptanceRead(t, f.root, primary)
	const detail = "Human shared detail pending during endpoint deletion."
	edited := strings.Replace(string(before), f.nodes[v015DeleteShared].Description, detail, 1)
	if edited == string(before) {
		t.Fatal("pending human edit fixture unchanged")
	}
	if err := os.WriteFile(filepath.Join(f.root, primary), []byte(edited), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.root, identityTestDoc)); err != nil {
		t.Fatal(err)
	}
	rpt, err := f.o.Run(context.Background(), f.root, f.dbPath)
	if err != nil || rpt.C1Edits != 1 || rpt.C2Deletions != 1 || rpt.NodesDeleted != 1 || rpt.C3Violations != 0 {
		t.Fatalf("pending edit/deletion %+v: %v", rpt, err)
	}
	db, err := storage.OpenReadOnly(f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	n, err := storage.NewNodeRepo(db).GetByID(context.Background(), v015DeleteShared)
	db.Close()
	if err != nil || n == nil {
		t.Fatal(n, err)
	}
	want := *f.nodes[v015DeleteShared]
	want.Description, want.Provenance, want.UpdatedAt = detail, dktypes.ProvenanceHumanCurated, n.UpdatedAt
	if !reflect.DeepEqual(*n, want) {
		t.Fatalf("pending human content or pinned fields changed: %+v != %+v", n, want)
	}
	f.nodes[v015DeleteShared] = n
	f.checkGraph(t)
	again, err := f.o.Run(context.Background(), f.root, f.dbPath)
	if err != nil || again.DocsChanged != 0 || again.C3Violations != 0 {
		t.Fatalf("pending edit repeat %+v: %v", again, err)
	}
}
