package incremental

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/writeback"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

// SQLite → shared primary/mirrors → parsed complete pair and sidecar must retain
// both semantic fields, exact evidence envelopes, every source and zero confidence.
func TestV015WritebackSQLiteGraphReadback(t *testing.T) {
	ctx := context.Background()
	st, _, _, _, _ := v014ReflowFixture(t)
	root := t.TempDir()
	node, err := st.nodes.GetByID(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	node.Summary = "Admission is conditional.\nIt changes after 2025."
	node.Description = "Before 2025, quota is required except emergency admission.\nAfter 2025, quota is required only during scheduled operation."
	if err := st.nodes.Update(ctx, node); err != nil {
		t.Fatal(err)
	}
	rows := []storage.NodeSource{{NodeUUID: "a", MemberID: "m-a", Skill: "one", FilePath: "a.md", StartLine: 7, EndLine: 8}, {NodeUUID: "a", MemberID: "m-a2", Skill: "two", FilePath: "b.md", StartLine: 93, EndLine: 94}}
	if err := st.sources.InsertBatch(ctx, rows); err != nil {
		t.Fatal(err)
	}
	description := `{"format":"cairn_relation_assertions_v1","assertions":[{"classification":"explicit_source_assertion","description":"Only during scheduled operation after 2025","sources":[{"file_path":"a.md","sha256":"frozen-source-sha"}],"evidence":[{"quote":"only during scheduled operation","start_line":27}]},{"classification":"legacy_unverified","description":"Prior uncertain inference"}]}`
	for _, edge := range []*dktypes.Edge{{SourceID: "a", TargetID: "b", Kind: dktypes.KindDependsOn, Description: description, Provenance: dktypes.ProvenanceExtraction, Confidence: .91, Properties: `{"condition":"scheduled"}`, SourceRefs: "one,two", Bidirectional: 1, Cardinality: "N:1", CreatedAt: time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)}, {SourceID: "a", TargetID: "c", Kind: dktypes.KindDependsOn, Description: "Retained uncertain inference", Provenance: dktypes.ProvenanceLLMInferred, Confidence: 0, Properties: `{"uncertain":true}`}} {
		if err := st.edges.Insert(ctx, edge); err != nil {
			t.Fatal(err)
		}
	}
	edges, err := st.edges.ListAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	names, err := loadLayerDisplayNames(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	updates, err := buildDocNodeUpdates(ctx, st, "a.md", names)
	if err != nil {
		t.Fatal(err)
	}
	primary := writeback.PrimaryRelPath(node.Domain, node.FileSlug, node.ID)
	if err := os.WriteFile(filepath.Join(root, "clean.md"), []byte("untouched human text"), 0600); err != nil {
		t.Fatal(err)
	}
	rs := newRunState()
	rs.affectedDocs["a.md"] = true
	rs.affectedDocs["b.md"] = true
	// A pending existing node adds its authoritative shared primary to the write set.
	ao := &alignOutcome{pending: map[string]*pendingNode{"a": {}}}
	rpt, err := applyIncrementalWriteback(ctx, st, rs, ao, nil, root)
	if err != nil {
		t.Fatal(err)
	}
	if rpt.PrimariesWritten != 1 {
		t.Fatalf("primary report %+v", rpt)
	}
	for _, rel := range []string{"a.md", "b.md", primary} {
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		sc, err := writeback.ReadSidecar(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		var found *writeback.SidecarNode
		for i := range sc.Nodes {
			if sc.Nodes[i].UUID == "a" {
				found = &sc.Nodes[i]
			}
		}
		if found == nil || found.GraphMetadata == nil {
			t.Fatalf("missing graph in %s", rel)
		}
		g := found.GraphMetadata
		if !g.RelationsKnown || len(g.Relations) != len(edges) {
			t.Fatalf("lost edges in %s: %+v", rel, g)
		}
		for i, snapshot := range g.Relations {
			if !reflect.DeepEqual(snapshot.Edge(), *edges[i]) {
				t.Fatalf("edge changed in %s: %+v != %+v", rel, snapshot.Edge(), *edges[i])
			}
		}
		if len(g.Sources) != 2 || g.GeneratedSpan == nil || g.GeneratedSpan.CoordinateSpace != "generated_document" {
			t.Fatalf("lost source/location metadata: %+v", g)
		}
		for _, src := range g.Sources {
			if src.CoordinateSpace != "source_document" {
				t.Fatal(src)
			}
		}
		if rel == "b.md" && found.Span.StartLine != 93 {
			t.Fatalf("wrong source span %+v", found.Span)
		}
		blocks, _, _ := parseKGBlocks(string(data))
		var block *ParsedBlock
		for _, b := range blocks {
			if b.UUID == "a" {
				block = b
			}
		}
		if block == nil || block.Summary != node.Summary {
			t.Fatalf("summary lost: %+v", block)
		}
		if rel == primary && block.Description != node.Description {
			t.Fatalf("description lost: %+v", block)
		}
	}
	if writeback.SidecarStale(root, "a.md", writeback.RenderDoc(updates), updates) {
		t.Fatal("fresh sidecar stale")
	}
	before, _ := os.ReadFile(filepath.Join(root, "a.md.kg.yaml"))
	if _, err := applyIncrementalWriteback(ctx, st, newRunStateWithDocs("a.md", "b.md"), ao, nil, root); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(root, "a.md.kg.yaml"))
	if string(before) != string(after) {
		t.Fatal("repeat rewrote unchanged sidecar")
	}
	clean, _ := os.ReadFile(filepath.Join(root, "clean.md"))
	if string(clean) != "untouched human text" {
		t.Fatal("clean human file changed")
	}
	afterSources, _ := st.sources.ListByNode(ctx, "a")
	if !reflect.DeepEqual(afterSources, rows) {
		t.Fatalf("source ledger rewritten with generated offsets: %+v", afterSources)
	}
}
func newRunStateWithDocs(paths ...string) *runState {
	rs := newRunState()
	for _, p := range paths {
		rs.affectedDocs[p] = true
	}
	return rs
}

// Existing RewriteDoc callers omit new optional metadata. Enrichment must never
// misclassify their later human edit as C3 and restore the old machine summary.
func TestV015WritebackLegacyAPIHumanEditSurvives(t *testing.T) {
	root, dbPath, _ := managedMoveFixture(t)
	db, err := storage.NewDB(storage.DBOptions{Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	st := newStores(db)
	ctx := context.Background()
	names, err := loadLayerDisplayNames(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	// A full editable one-source node exercises a legacy public API call.
	node, err := st.nodes.GetByID(ctx, identityTestNode)
	if err != nil {
		t.Fatal(err)
	}
	node.Summary = "Machine baseline summary"
	node.Description = "Machine baseline detail"
	if err := st.nodes.Update(ctx, node); err != nil {
		t.Fatal(err)
	}
	u := buildNodeUpdate(node, nil, names, "")
	u.Sources = nil
	u.Members = []string{"m1", "m2"}
	if err := writeback.RewriteDoc(root, identityTestDoc, []writeback.NodeUpdate{u}, time.Now()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, identityTestDoc))
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), node.Summary, "Human revised summary", 1))
	if err := os.WriteFile(filepath.Join(root, identityTestDoc), data, 0644); err != nil {
		t.Fatal(err)
	}
	// Direct diff/authoritative check proves optional unknown metadata doesn't signal C3.
	sc, err := writeback.ReadSidecar(filepath.Join(root, identityTestDoc))
	if err != nil {
		t.Fatal(err)
	}
	authoritative := buildNodeUpdate(node, []storage.NodeSource{{NodeUUID: node.ID, MemberID: "m1", FilePath: identityTestDoc}, {NodeUUID: node.ID, MemberID: "m2", FilePath: identityTestDoc}}, names, identityTestDoc)
	if !writeback.SidecarMetadataMatches(*sc, identityTestDoc, []writeback.NodeUpdate{authoritative}) {
		t.Fatal("legacy API metadata misclassified as C3")
	}
	changes := DiffDoc(identityTestDoc, sc, string(data), false)
	if len(changes.C1) != 1 || changes.C1[0].NewSummary != "Human revised summary" {
		t.Fatalf("human edit lost %+v", changes)
	}
	db.Close()
}

// Actual rename helper must refresh the complete active ledger in every mirror
// and shared primary, then settle without false metadata edits or model work.
func TestV015WritebackMoveRefreshesSharedMetadata(t *testing.T) {
	root, dbPath, o := managedMoveFixture(t)
	renameManagedPair(t, root, moveTestDoc)
	report, err := o.Run(context.Background(), root, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.DocumentMoves) != 1 || report.C3Violations != 0 {
		t.Fatalf("move did not converge %+v", report)
	}
	for _, path := range []string{moveTestDoc, moveTestOther, writeback.PrimaryRelPath("engineering", "", moveTestShared)} {
		sc, err := writeback.ReadSidecar(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, node := range sc.Nodes {
			if node.UUID != moveTestShared {
				continue
			}
			found = true
			if node.GraphMetadata == nil || len(node.GraphMetadata.Sources) != 2 {
				t.Fatalf("missing shared ledger %s", path)
			}
			for _, source := range node.GraphMetadata.Sources {
				if source.FilePath == identityTestDoc {
					t.Fatalf("stale moved path in %s", path)
				}
				if source.MemberID == "m3" && (source.FilePath != moveTestDoc || source.StartLine != 20 || source.EndLine != 25) {
					t.Fatalf("source identity/span changed %+v", source)
				}
			}
		}
		if !found {
			t.Fatalf("missing shared node in %s", path)
		}
	}
	again, err := o.Run(context.Background(), root, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if again.DocsChanged != 0 || again.C3Violations != 0 {
		t.Fatalf("repeat not stable %+v", again)
	}
}

// A blocked write leaves the previous usable file pair and baseline, while a
// sibling still completes. Generated block offsets never flow back to SQLite.
func TestV015WritebackFailureRetainsPairAndBaseline(t *testing.T) {
	ctx := context.Background()
	st, _, _, _, _ := v014ReflowFixture(t)
	root := t.TempDir()
	rs := newRunStateWithDocs("a.md", "c.md")
	if _, err := applyIncrementalWriteback(ctx, st, rs, nil, nil, root); err != nil {
		t.Fatal(err)
	}
	beforeMD, _ := os.ReadFile(filepath.Join(root, "c.md"))
	beforeSC, _ := os.ReadFile(filepath.Join(root, "c.md.kg.yaml"))
	oldHash := writeback.HashEditable(string(beforeMD))
	if err := st.files.Upsert(ctx, "repo", "c.md", oldHash); err != nil {
		t.Fatal(err)
	}
	node, _ := st.nodes.GetByID(ctx, "c")
	node.Summary = "new summary"
	node.Description = "new full detail"
	if err := st.nodes.Update(ctx, node); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "c.md.kg-writeback.json"), 0700); err != nil {
		t.Fatal(err)
	}
	failed := newRunStateWithDocs("a.md", "c.md")
	rpt, err := applyIncrementalWriteback(ctx, st, failed, nil, nil, root)
	if err == nil || !failed.failedDocsSnapshot()["c.md"] || rpt.DocsSkipped != 1 {
		t.Fatalf("failure/sibling report %+v %v", rpt, err)
	}
	var o IncrementalOrchestrator
	if err := o.syncFileStates(ctx, st, "repo", &scanResult{all: []string{"a.md", "c.md"}}, nil, root, failed); err != nil {
		t.Fatal(err)
	}
	state, err := st.files.Get(ctx, "repo", "c.md")
	if err != nil || state.ContentHash != oldHash {
		t.Fatalf("failed baseline advanced %+v %v", state, err)
	}
	afterMD, _ := os.ReadFile(filepath.Join(root, "c.md"))
	afterSC, _ := os.ReadFile(filepath.Join(root, "c.md.kg.yaml"))
	if string(beforeMD) != string(afterMD) || string(beforeSC) != string(afterSC) {
		t.Fatal("previous usable pair changed on write failure")
	}
}

// Two sharing source renames commit in one existing transaction. Optional full
// source snapshots must not invalidate the second old-path attestation midway.
func TestV015WritebackTwoSharedMovesConverge(t *testing.T) {
	root, dbPath, o := managedMoveFixture(t)
	renameManagedPair(t, root, moveTestDoc)
	otherNew := "old-skill/references/other-renamed.md"
	for _, suffix := range []string{"", ".kg.yaml"} {
		if err := os.Rename(filepath.Join(root, moveTestOther)+suffix, filepath.Join(root, otherNew)+suffix); err != nil {
			t.Fatal(err)
		}
	}
	report, err := o.Run(context.Background(), root, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.DocumentMoves) != 2 || report.C3Violations != 0 {
		t.Fatalf("two moves failed %+v", report)
	}
	for _, path := range []string{moveTestDoc, otherNew, writeback.PrimaryRelPath("engineering", "", moveTestShared)} {
		sc, err := writeback.ReadSidecar(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		if sc.Doc != path {
			t.Fatalf("stale document identity %s: %s", path, sc.Doc)
		}
		for _, node := range sc.Nodes {
			if node.UUID != moveTestShared {
				continue
			}
			for _, src := range node.GraphMetadata.Sources {
				if src.FilePath == identityTestDoc || src.FilePath == moveTestOther {
					t.Fatalf("stale shared source %+v", src)
				}
			}
		}
	}
	again, err := o.Run(context.Background(), root, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if again.DocsChanged != 0 {
		t.Fatalf("repeat %+v", again)
	}
}

// A metadata refresh failure after the move transaction leaves the old moved
// Doc attestation available. Retry recovers both committed paths without C2/LLM.
func TestV015WritebackTwoSharedMovesRetryMetadataFailure(t *testing.T) {
	root, dbPath, o := managedMoveFixture(t)
	renameManagedPair(t, root, moveTestDoc)
	otherNew := "old-skill/references/other-renamed.md"
	for _, suffix := range []string{"", ".kg.yaml"} {
		if err := os.Rename(filepath.Join(root, moveTestOther)+suffix, filepath.Join(root, otherNew)+suffix); err != nil {
			t.Fatal(err)
		}
	}
	primary := filepath.Join(root, writeback.PrimaryRelPath("engineering", "", moveTestShared))
	oldSidecar, err := os.ReadFile(primary + ".kg.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(primary + ".kg.yaml"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(primary+".kg.yaml", 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Run(context.Background(), root, dbPath); err == nil {
		t.Fatal("expected metadata refresh failure")
	}
	// Destinations still contain their old Doc proof; no hash baseline was absorbed.
	for _, path := range []string{moveTestDoc, otherNew} {
		sc, err := writeback.ReadSidecar(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		if sc.Doc == path {
			t.Fatal("destination attestation erased before siblings refreshed")
		}
	}
	if err := os.Remove(primary + ".kg.yaml"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(primary+".kg.yaml", oldSidecar, 0600); err != nil {
		t.Fatal(err)
	}
	report, err := o.Run(context.Background(), root, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.DocumentMoves) != 2 || !report.DocumentMoves[0].Recovered || !report.DocumentMoves[1].Recovered || report.C2Deletions != 0 || report.C3Violations != 0 {
		t.Fatalf("recovery failed %+v", report)
	}
	again, err := o.Run(context.Background(), root, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if again.DocsChanged != 0 {
		t.Fatalf("recovery repeat %+v", again)
	}
}

// Full writer sees two raw occurrences of one accepted member in one file;
// SQLite has one physical incidence and a genuine representative span. The
// same graph must remain byte-stable when I9 reads back that canonical ledger.
func TestV015WritebackRawOccurrencesMatchPersistedRepresentative(t *testing.T) {
	ctx := context.Background()
	st, _, key, _, _ := v014ReflowFixture(t)
	root := t.TempDir()
	node, _ := st.nodes.GetByID(ctx, "a")
	if _, err := st.sources.DeleteByNodeAndFile(ctx, "b", "b.md"); err != nil {
		t.Fatal(err)
	}
	selected := []storage.NodeSource{{NodeUUID: "a", MemberID: "m-a", Skill: "fixture", FilePath: "a.md", StartLine: 7, EndLine: 8}, {NodeUUID: "a", MemberID: "m-extra", Skill: "fixture", FilePath: "b.md", StartLine: 93, EndLine: 94}}
	if err := st.sources.InsertBatch(ctx, selected); err != nil {
		t.Fatal(err)
	}
	result := &extract.Result{Domains: []extract.Domain{{Slug: key.Domain, Name: key.Domain, Subdomains: []extract.Subdomain{{Slug: key.Subdomain, Name: key.Subdomain, Concepts: []extract.Node{{ID: node.ID, Name: node.Name, Summary: node.Summary, Description: node.Description, Members: []string{"m-extra", "m-a"}}}}}}}}
	raw := map[string][]writeback.MemberSource{"m-a": {{Skill: "fixture", FilePath: "a.md", StartLine: 40, EndLine: 41}, {Skill: "fixture", FilePath: "a.md", StartLine: 7, EndLine: 8}, {Skill: "fixture", FilePath: "a.md", StartLine: 1, EndLine: 0}}, "m-extra": {{Skill: "fixture", FilePath: "b.md", StartLine: 93, EndLine: 94}}}
	if _, err := writeback.WritebackContextWithRelations(ctx, result, raw, []*dktypes.Edge{}, root); err != nil {
		t.Fatal(err)
	}
	primary := writeback.PrimaryRelPath(key.Domain, "", node.ID)
	before := map[string][]byte{}
	for _, path := range []string{"a.md", "b.md", primary} {
		before[path], _ = os.ReadFile(filepath.Join(root, path))
		before[path+".kg.yaml"], _ = os.ReadFile(filepath.Join(root, path+".kg.yaml"))
	}
	ao := &alignOutcome{pending: map[string]*pendingNode{"a": {}}}
	report, err := applyIncrementalWriteback(ctx, st, newRunStateWithDocs("a.md", "b.md"), ao, nil, root)
	if err != nil {
		t.Fatal(err)
	}
	if report.DocsRewritten != 0 || report.PrimariesWritten != 0 {
		t.Fatalf("same persisted graph was rewritten: %+v", report)
	}
	for path, old := range before {
		after, _ := os.ReadFile(filepath.Join(root, path))
		if string(after) != string(old) {
			t.Fatalf("full→I9 metadata drift: %s", path)
		}
	}
	sc, err := writeback.ReadSidecar(filepath.Join(root, "a.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(sc.Nodes[0].GraphMetadata.Sources) != 2 || sc.Nodes[0].Span.StartLine != 7 || sc.Nodes[0].Span.EndLine != 8 {
		t.Fatalf("noncanonical incidence/span %+v", sc.Nodes[0])
	}
}
