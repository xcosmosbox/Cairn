package incremental

import (
	"context"
	"encoding/json"
	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/build/internal/writeback"
	"github.com/xcosmosbox/cairn/core/storage"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/core/dktypes"
)

// Preserve the exact old-failing control from #113 comment6434350672.
// A valid zero is existing knowledge, not an omitted confidence default.
func TestV015OmittedZeroConfidenceOldEdgeSQLite(t *testing.T) {
	st, dirty, key, docs, originals := v014ReflowFixture(t)
	ctx := context.Background()
	if err := st.edges.Insert(ctx, &dktypes.Edge{SourceID: "a", TargetID: "b", Kind: dktypes.KindDependsOn, Description: "Legacy uncertain support", Provenance: dktypes.ProvenanceLLMInferred, Confidence: 0, Properties: `{"uncertain":true}`}); err != nil {
		t.Fatal(err)
	}
	before, _ := st.edges.GetOutgoing(ctx, "a", 0)
	r := newReflower(&v014ReflowClient{verifierFailure: "unknown"}, 4096, st)
	rs := newRunState()
	final, kept := r.recomputeRelationsWithSources(ctx, dirty, nil, nil, docs, originals, extract.RelationProcessOptions{MaxRetries: -1}, rs)
	if len(final[key]) != 0 || len(kept[key]) != 1 || !reflect.DeepEqual(before[0], kept[key][0]) {
		t.Fatal("helper did not retain exact old edge")
	}
	if err := applyIncrementalIngest(ctx, st, dirty, nil, &reflowResult{relations: final, keptCross: kept}, rs); err != nil {
		t.Fatal(err)
	}
	after, _ := st.edges.GetOutgoing(ctx, "a", 0)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("omission/unknown rewrote full old Edge fields: before=%+v after=%+v old_confidence=%v new_confidence=%v", before[0], after[0], before[0].Confidence, after[0].Confidence)
	}
}

// Opt-in consumes an actually verified .3 outcome, then I-8 applies both fields.
// Legacy ordinary callers keep their explicit deferral until .6 chooses this adapter.
func TestV015VerifiedNodePairSQLiteAndNoStalePair(t *testing.T) {
	ctx := context.Background()
	st, old := nodeReflowStore(t, dktypes.ProvenanceLLMInferred)
	key := SubdomainKey{old.Domain, old.Subdomain}
	dirty := NewDirtySet()
	dirty.DirtyNodeFused(old.ID, key)
	ao := &alignOutcome{pending: map[string]*pendingNode{old.ID: {UUID: old.ID, Domain: old.Domain, Subdomain: old.Subdomain, NewMembers: []string{"member"}}}, unitsByID: map[string]*alignUnit{"member": {ID: "member", Skill: "fixture", FilePath: "original.md", Content: "Only above 2 bar.", Detail: "Calibration remains uncertain."}}}
	ao.sourceRows = []storage.NodeSource{{NodeUUID: old.ID, MemberID: "member", Skill: "fixture", FilePath: "original.md", StartLine: 7, EndLine: 8}, {NodeUUID: old.ID, MemberID: "member", Skill: "fixture", FilePath: "second.md", StartLine: 93, EndLine: 94}}
	sourceA := ao.unitsByID["member"]
	sourceB := *sourceA
	sourceB.FilePath = "second.md"
	ao.sourcesByID = map[string][]*alignUnit{"member": {sourceA, &sourceB}}
	summary := "Qualified operation and uncertain calibration"
	detail := old.Description + " Only above 2 bar. Calibration remains uncertain."
	r := newReflower(&nodeReflowClient{summary: summary, description: detail}, 4096, st)
	descriptions, contents := r.fuseDescriptionsWithVerifiedContent(ctx, dirty, ao, newRunState())
	if len(contents) != 1 || contents[old.ID].CandidateID == "" || contents[old.ID].MaterialID == "" || contents[old.ID].Outcome != annotation.DocumentComplete {
		t.Fatalf("no complete current verified pair: %+v", contents)
	}
	// A different body cannot borrow the accepted summary or cause a partial write.
	rr := &reflowResult{descriptions: map[string]string{old.ID: "mismatched body"}, verifiedNodeContent: contents}
	version, _ := st.version.Current(ctx)
	if err := applyIncrementalIngest(ctx, st, dirty, ao, rr, newRunState()); err == nil {
		t.Fatal("mismatched pair accepted")
	}
	unchanged, _ := st.nodes.GetByID(ctx, old.ID)
	if unchanged.Summary != old.Summary || unchanged.Description != old.Description {
		t.Fatal("prevalidation wrote stale pair")
	}
	if now, _ := st.version.Current(ctx); now != version {
		t.Fatal("failed pair advanced baseline")
	}
	rr.descriptions = descriptions
	if err := applyIncrementalIngest(ctx, st, dirty, ao, rr, newRunState()); err != nil {
		t.Fatal(err)
	}
	got, _ := st.nodes.GetByID(ctx, old.ID)
	if got.Summary != summary || got.Description != detail || got.ID != old.ID || got.Name != old.Name || got.Provenance != old.Provenance || got.Confidence != old.Confidence {
		t.Fatalf("pair or pinned metadata lost: %+v", got)
	}
	first := *got
	if err := applyIncrementalIngest(ctx, st, dirty, ao, rr, newRunState()); err != nil {
		t.Fatal(err)
	}
	repeated, _ := st.nodes.GetByID(ctx, old.ID)
	if !reflect.DeepEqual(&first, repeated) {
		t.Fatal("same pair changed node on repeat")
	}
	v015AssertSQLiteWritebackReadback(t, st, ao, old.ID, summary, detail)
	// Stale/partial outcomes are never write authorization, even with matching text.
	bad := contents[old.ID]
	bad.Outcome = annotation.DocumentUnavailable
	rr.verifiedNodeContent = map[string]extract.NodeDescriptionOutcome{old.ID: bad}
	if err := applyIncrementalIngest(ctx, st, dirty, ao, rr, newRunState()); err == nil {
		t.Fatal("unavailable outcome became write permission")
	}
}

func TestV015DuplicateLegacyRowsActualSelectionRepeatSQLite(t *testing.T) {
	st, dirty, key, docs, originals := v014ReflowFixture(t)
	ctx := context.Background()
	old := v014OldEdge(t, st, "a", "b")
	second := &dktypes.Edge{SourceID: "a", TargetID: "b", Kind: dktypes.KindDependsOn, Description: "Independent old time condition", Properties: `{"keep":"second"}`, SourceRefs: "legacy-two", Provenance: dktypes.ProvenanceLLMInferred, Confidence: 0, Bidirectional: 1, Cardinality: "M:N", CreatedAt: old.CreatedAt}
	if err := st.edges.Insert(ctx, second); err != nil {
		t.Fatal(err)
	}
	before, _ := st.edges.GetOutgoing(ctx, "a", 0)
	var previous string
	for pass := 0; pass < 3; pass++ {
		r := newReflower(&v014ReflowClient{generate: []string{"Admission requires quota before 2025 except emergencies."}}, 4096, st)
		rs := newRunState()
		final, kept := r.recomputeRelationsWithSources(ctx, dirty, nil, nil, docs, originals, extract.RelationProcessOptions{MaxRetries: -1}, rs)
		if len(final[key]) != 1 || len(r.relationProcessReport().Selected) == 0 {
			t.Fatal("verified duplicate scope deferred or never selected")
		}
		if err := applyIncrementalIngest(ctx, st, dirty, nil, &reflowResult{relations: final, keptCross: kept}, rs); err != nil {
			t.Fatal(err)
		}
		got, err := st.edges.GetOutgoing(ctx, "a", 0)
		if err != nil || len(got) != 2 || !reflect.DeepEqual(got[1], before[1]) {
			t.Fatalf("old independent full row lost: %+v %v", got, err)
		}
		metadata := *got[0]
		metadata.Description = before[0].Description
		if !reflect.DeepEqual(&metadata, before[0]) {
			t.Fatal("first-row metadata changed")
		}
		var envelope reflowAssertionEnvelope
		if err := json.Unmarshal([]byte(got[0].Description), &envelope); err != nil {
			t.Fatal(err)
		}
		explicit, legacy := 0, 0
		for _, assertion := range envelope.Assertions {
			if assertion.Classification == "explicit" {
				explicit++
				if len(assertion.Sources) == 0 || len(assertion.Evidence) == 0 || assertion.Sources[0].SHA256 == "" || assertion.Evidence[0].Quote == "" {
					t.Fatal("explicit source evidence lost")
				}
			} else if assertion.Classification == "legacy_unverified" {
				legacy++
			}
		}
		if explicit != 1 || legacy != 2 {
			t.Fatalf("assertion credit wrong: %+v", envelope)
		}
		if pass > 0 && previous != got[0].Description {
			t.Fatal("repeat grew assertions")
		}
		previous = got[0].Description
	}
	v015AssertSQLiteWritebackReadback(t, st, nil, "a", docs[0].Items[0].Content, docs[0].Items[0].Detail)
}

// Compose the actual selected SQLite graph with I-9 and its serialized readback.
// The acceptance checks all incident rows; editable Markdown stays relation-free.
func v015AssertSQLiteWritebackReadback(t *testing.T, st *stores, ao *alignOutcome, id, summary, detail string) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	sources, err := st.sources.ListByNode(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	rs := newRunState()
	for _, s := range sources {
		rs.affectedDocs[s.FilePath] = true
	}
	node, err := st.nodes.GetByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) > 1 {
		rs.touched[id] = &touchedNodeInfo{SourceDocs: []string{sources[0].FilePath, sources[1].FilePath}, WasShared: true}
	}
	clean := filepath.Join(root, "clean.md")
	if err := os.WriteFile(clean, []byte("untouched curated prose"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := applyIncrementalWriteback(ctx, st, rs, ao, nil, root); err != nil {
		t.Fatal(err)
	}
	edges, err := st.edges.ListAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	incident := writeback.IncidentRelations(edges, id)
	paths := []string{}
	for _, s := range sources {
		paths = append(paths, s.FilePath)
	}
	if len(sources) > 1 {
		paths = append(paths, writeback.PrimaryRelPath(node.Domain, node.FileSlug, id))
	}
	for _, rel := range paths {
		md, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		blocks, _, anomalies := parseKGBlocks(string(md))
		if len(anomalies) > 0 {
			t.Fatal(anomalies)
		}
		found := false
		for _, b := range blocks {
			if b.UUID == id {
				found = true
				if b.Summary != summary {
					t.Fatalf("summary changed in %s: %q", rel, b.Summary)
				}
				if !b.Mirror && b.Description != detail {
					t.Fatalf("detail changed in %s: %q", rel, b.Description)
				}
			}
		}
		if !found {
			t.Fatalf("node absent in %s", rel)
		}
		if strings.Contains(string(md), "cairn_relation_assertions_v1") {
			t.Fatal("relation envelope leaked into editable Markdown")
		}
		sc, err := writeback.ReadSidecar(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		for _, sn := range sc.Nodes {
			if sn.UUID == id {
				g := sn.GraphMetadata
				if g == nil || !g.SourcesKnown || !g.RelationsKnown || len(g.Sources) != len(sources) || len(g.Relations) != len(incident) {
					t.Fatalf("graph readback lost in %s: %+v", rel, g)
				}
				for i, snapshot := range g.Relations {
					if !reflect.DeepEqual(snapshot.Edge(), *incident[i]) {
						t.Fatalf("actual edge changed in %s", rel)
					}
				}
				for _, src := range g.Sources {
					if src.CoordinateSpace != "source_document" {
						t.Fatal("generated offsets claimed as source")
					}
				}
			}
		}
	}
	after, err := st.sources.ListByNode(ctx, id)
	if err != nil || !reflect.DeepEqual(sources, after) {
		t.Fatal("writeback rewrote original source ledger")
	}
	body, err := os.ReadFile(clean)
	if err != nil || string(body) != "untouched curated prose" {
		t.Fatal("clean human prose changed")
	}
}

// Confidence zero is also a legal value for a newly selected edge, not only an
// old retained row. I-8 must not rewrite it before the storage transaction sees it.
func TestV015NewSelectedZeroConfidenceSQLite(t *testing.T) {
	st, dirty, key, _, _ := v014ReflowFixture(t)
	rr := &reflowResult{relations: map[SubdomainKey][]extract.Relation{key: {{Source: "a", Target: "b", Kind: "depends_on", Description: "Zero-confidence selected control", Confidence: 0}}}}
	if err := applyIncrementalIngest(context.Background(), st, dirty, nil, rr, newRunState()); err != nil {
		t.Fatal(err)
	}
	rows, err := st.edges.GetOutgoing(context.Background(), "a", 0)
	if err != nil || len(rows) != 1 || rows[0].Confidence != 0 {
		t.Fatalf("I-8 defaulted valid zero: %+v %v", rows, err)
	}
}
