package incremental

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

type v014ChangedAcceptanceClient struct {
	requests []llm.CompleteRequest
	mode     string
}

func (*v014ChangedAcceptanceClient) ProviderName() string { return "v014-independent-changed-control" }
func (c *v014ChangedAcceptanceClient) Complete(_ context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
	c.requests = append(c.requests, req)
	for _, s := range []string{"HUMAN-SECRET", "11111111-1111-5111-8111-111111111111", "22222222-2222-5222-8222-222222222222"} {
		if strings.Contains(req.User, s) {
			return nil, fmt.Errorf("protected identity/content entered relation request: %s", s)
		}
	}
	reply := func(v any) (*llm.CompleteResponse, error) {
		b, e := json.Marshal(v)
		return &llm.CompleteResponse{Text: string(b), FinishReason: "stop"}, e
	}
	if c.mode == "transport" {
		return nil, fmt.Errorf("temporary relation verifier transport failure")
	}
	if strings.Contains(req.System, "SOURCE-RELATION GENERATE") {
		if c.mode == "omission" {
			return reply(map[string]any{"relations": []any{}})
		}
		var in struct {
			Nodes []struct {
				Alias string `json:"alias"`
				Name  string `json:"name"`
			} `json:"nodes"`
		}
		if e := json.Unmarshal([]byte(req.User), &in); e != nil {
			return nil, e
		}
		names := map[string]string{}
		for _, n := range in.Nodes {
			names[n.Name] = n.Alias
		}
		return reply(map[string]any{"relations": []extract.Relation{{Source: names["Atlas pump"], Target: names["Controller C"], Kind: "depends_on", Description: "The pump depends on Controller C from 2025-06-01.", Confidence: .9}}})
	}
	if strings.Contains(req.System, "SOURCE-RELATION COVERAGE") {
		if c.mode == "omission" {
			return reply(map[string]any{"complete": false, "supported": false, "reason": "Original dated dependency omitted.", "critique": "Restore the complete pump-to-C dependency from 2025-06-01."})
		}
		return reply(map[string]any{"complete": true, "supported": true, "reason": "Mapped source assertion preserved.", "critique": ""})
	}
	var in struct {
		Source  extract.CrossEndpoint `json:"source"`
		Target  extract.CrossEndpoint `json:"target"`
		Sources []struct {
			ID      string   `json:"id"`
			Content string   `json:"content"`
			Members []string `json:"members"`
		} `json:"original_sources"`
	}
	if e := json.Unmarshal([]byte(req.User), &in); e != nil {
		return nil, e
	}
	if c.mode == "unknown" {
		return reply(map[string]any{"disposition": "insufficient_evidence", "reason": "Source assertion cannot presently be verified."})
	}
	var evidence []map[string]string
	for _, endpoint := range []struct {
		side    string
		members []string
	}{{"source", in.Source.Members}, {"target", in.Target.Members}} {
		found := false
		for _, s := range in.Sources {
			for _, m := range endpoint.members {
				for _, sm := range s.Members {
					if m == sm {
						quote := s.Content
						if !strings.Contains(s.Content, quote) {
							return nil, fmt.Errorf("test source missing exact assertion")
						}
						evidence = append(evidence, map[string]string{"endpoint": endpoint.side, "member_id": m, "source_id": s.ID, "quote": quote})
						found = true
						break
					}
				}
				if found {
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("endpoint missing original member")
		}
	}
	return reply(map[string]any{"disposition": "supported", "reason": "Explicit pump-to-C dated dependency in original.", "relation_supported": true, "direction_supported": true, "explicit_source_assertion": true, "evidence": evidence})
}

func v014ChangedAcceptanceFixture(t *testing.T) (*storage.DB, *stores, *DirtySet, []*dktypes.AnnotatedDocument, []extract.FrozenSource, []*dktypes.Edge) {
	t.Helper()
	ctx := context.Background()
	db, err := storage.NewDB(storage.DBOptions{Path: filepath.Join(t.TempDir(), "relation.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st := newStores(db)
	b, err := os.ReadFile(filepath.Join("..", "..", "test", "e2e", "semantic-evidence", "testdata", "v011", "supplement.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Sources []struct {
			ID      string `json:"source_id"`
			Path    string `json:"path"`
			Content string `json:"content"`
		} `json:"sources"`
	}
	if err = json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	var original extract.FrozenSource
	for _, s := range f.Sources {
		if s.ID == "F04" {
			original = extract.FreezeSource("operations", s.Path, s.Content)
		}
	}
	if original.Content == "" {
		t.Fatal("F04 denominator missing")
	}
	c, p, h := "11111111-1111-5111-8111-111111111111", "22222222-2222-5222-8222-222222222222", "33333333-3333-5333-8333-333333333333"
	for _, n := range []*dktypes.Node{{ID: c, Name: "Controller C", Label: dktypes.LabelEntity, Summary: "Controller", Description: "Generated controller text", Domain: "control", Subdomain: "signals", Provenance: dktypes.ProvenanceLLMInferred, Confidence: .9}, {ID: p, Name: "Atlas pump", Label: dktypes.LabelEntity, Summary: "Pump", Description: "Generated pump text", Domain: "operations", Subdomain: "equipment", Provenance: dktypes.ProvenanceLLMInferred, Confidence: .9}, {ID: h, Name: "HUMAN-SECRET name", Label: dktypes.LabelEntity, Summary: "HUMAN-SECRET summary", Description: "HUMAN-SECRET detail", Domain: "operations", Subdomain: "equipment", Provenance: dktypes.ProvenanceHumanCurated, Confidence: 1}} {
		if err = st.nodes.Insert(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	docs := []*dktypes.AnnotatedDocument{{Skill: "operations", FilePath: original.Path, Items: []dktypes.AnnotatedItem{{ID: "c-member", Tag: dktypes.TagEntity, Name: "Controller C", Content: original.Content, Confidence: 1}, {ID: "p-member", Tag: dktypes.TagEntity, Name: "Atlas pump", Content: original.Content, Confidence: 1}}}}
	if err = st.sources.InsertBatch(ctx, []storage.NodeSource{{NodeUUID: c, MemberID: "c-member", Skill: "operations", FilePath: original.Path}, {NodeUUID: p, MemberID: "p-member", Skill: "operations", FilePath: original.Path}}); err != nil {
		t.Fatal(err)
	}
	created := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, e := range []*dktypes.Edge{{SourceID: p, TargetID: c, Kind: dktypes.KindDependsOn, Description: "Legacy inferred dependency, not yet source credited.", Properties: `{"audit":"keep-machine"}`, SourceRefs: "original-source-ref", Provenance: dktypes.ProvenanceLLMInferred, Confidence: .57, Bidirectional: 1, Cardinality: "1:N", CreatedAt: created}, {SourceID: p, TargetID: h, Kind: dktypes.KindReferences, Description: "HUMAN-SECRET relation", Properties: `{"audit":"keep-human"}`, SourceRefs: "human-source-ref", Provenance: dktypes.ProvenanceHumanCurated, Confidence: .99, Bidirectional: 1, Cardinality: "N:1", CreatedAt: created}} {
		if err = st.edges.Insert(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	var before []*dktypes.Edge
	for _, id := range []string{c, p} {
		edges, e := st.edges.GetOutgoing(ctx, id, 0)
		if e != nil {
			t.Fatal(e)
		}
		before = append(before, edges...)
	}
	dirty := NewDirtySet()
	dirty.DirtyNodeContent(c, SubdomainKey{"control", "signals"})
	return db, st, dirty, docs, []extract.FrozenSource{original}, before
}
func v014ChangedAcceptanceEdges(t *testing.T, st *stores) []*dktypes.Edge {
	t.Helper()
	var out []*dktypes.Edge
	for _, id := range []string{"11111111-1111-5111-8111-111111111111", "22222222-2222-5222-8222-222222222222"} {
		es, e := st.edges.GetOutgoing(context.Background(), id, 0)
		if e != nil {
			t.Fatal(e)
		}
		out = append(out, es...)
	}
	return out
}

// A touched machine edge used to disappear when the response omitted it. Cross
// edges also bypassed revalidation when only their target received new evidence.
func TestV014AcceptanceChangedOmissionUnknownAndHumanFullRows(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"legacy", "omission", "unknown", "transport"} {
		t.Run(mode, func(t *testing.T) {
			_, st, dirty, docs, originals, before := v014ChangedAcceptanceFixture(t)
			client := &v014ChangedAcceptanceClient{mode: mode}
			r := newReflower(client, 65536, st)
			rs := newRunState()
			var final map[SubdomainKey][]extract.Relation
			var kept map[SubdomainKey][]*dktypes.Edge
			if mode == "legacy" {
				final, kept = r.recomputeRelations(context.Background(), dirty, nil, nil, rs)
			} else {
				final, kept = r.recomputeRelationsWithSources(context.Background(), dirty, nil, nil, docs, originals, extract.RelationProcessOptions{MaxRetries: -1}, rs)
			}
			if mode == "legacy" && len(client.requests) != 0 {
				t.Fatal("source-less entry attempted certification")
			}
			if mode != "legacy" && !dirty.Subdomains[SubdomainKey{"operations", "equipment"}] {
				t.Fatal("changed cross target did not revalidate incoming source owner")
			}
			if err := applyIncrementalIngest(context.Background(), st, dirty, nil, &reflowResult{descriptions: map[string]string{}, relations: final, keptCross: kept}, rs); err != nil {
				t.Fatal(err)
			}
			after := v014ChangedAcceptanceEdges(t, st)
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("%s lost full old edge fields:\nbefore=%+v\nafter=%+v", mode, before, after)
			}
			if mode == "omission" || mode == "unknown" || mode == "transport" {
				report := r.relationProcessReport()
				if report == nil || len(report.Scopes) == 0 || report.Scopes[0].Outcome == annotation.DocumentComplete {
					t.Fatal("unknown/transport scope certified complete")
				}
			}
			sawHuman := false
			for _, out := range r.relationReflowOutcomes() {
				if out.State == "retained_human" {
					sawHuman = true
				}
				if out.State == "retained_old" && out.Classification == "explicit" {
					t.Fatal("retention manufactured explicit-source credit")
				}
			}
			if mode != "legacy" && !sawHuman {
				t.Fatal("human protection outcome missing in affected source-aware scope")
			}
		})
	}
}

func v014SelectedSignatures(selected []extract.RelationProcessDecision) []string {
	var out []string
	for _, d := range selected {
		b, _ := json.Marshal(struct {
			Kind, Description, Class string
			Sources                  []extract.CrossSource
			Evidence                 []extract.CrossEvidence
		}{d.Relation.Kind, d.Relation.Description, d.Classification, d.Sources, d.Evidence})
		out = append(out, string(b))
	}
	sort.Strings(out)
	return out
}

// The changed-document adapter must call the same original-aware helper, carry
// its evidence unchanged, and distinguish old inferred text from verified facets.
func TestV014AcceptanceFullChangedParityAndLabeledFacets(t *testing.T) {
	t.Parallel()
	_, st, dirty, docs, originals, before := v014ChangedAcceptanceFixture(t)
	client := &v014ChangedAcceptanceClient{}
	r := newReflower(client, 65536, st)
	final, kept := r.recomputeRelationsWithSources(context.Background(), dirty, nil, nil, docs, originals, extract.RelationProcessOptions{MaxRetries: -1}, newRunState())
	report := r.relationProcessReport()
	if report == nil || len(report.Selected) != 1 || len(final[SubdomainKey{"operations", "equipment"}]) != 1 {
		t.Fatalf("source-backed cross relation not selected: %+v", report)
	}
	relation := final[SubdomainKey{"operations", "equipment"}][0]
	if relation.Source != before[0].SourceID || relation.Target != before[0].TargetID {
		t.Fatal("changed adapter reversed endpoints or lost pinned identity")
	}
	var envelope struct {
		Assertions []struct {
			Class       string                  `json:"classification"`
			Description string                  `json:"description"`
			Sources     []extract.CrossSource   `json:"sources"`
			Evidence    []extract.CrossEvidence `json:"evidence"`
		} `json:"cairn_relation_assertions_v1"`
	}
	if err := json.Unmarshal([]byte(relation.Description), &envelope); err != nil {
		t.Fatalf("missing machine-readable qualifier/source representation: %v", err)
	}
	legacy, explicit := false, false
	for _, a := range envelope.Assertions {
		if a.Class == "legacy_unverified" && a.Description == before[0].Description {
			legacy = true
		}
		if a.Class == "explicit" && strings.Contains(a.Description, "from 2025-06-01") && len(a.Sources) > 0 && len(a.Evidence) == 2 {
			explicit = true
		}
	}
	if !legacy || !explicit {
		t.Fatalf("old/new claim distinction lost: %+v", envelope)
	}
	humanPreserved := false
	for _, edges := range kept {
		for _, edge := range edges {
			if edge.Provenance == dktypes.ProvenanceHumanCurated && reflect.DeepEqual(edge, before[1]) {
				humanPreserved = true
			}
		}
	}
	if !humanPreserved {
		t.Fatal("human edge not retained verbatim")
	}
	full := &extract.Result{Domains: []extract.Domain{{Name: "Control", Slug: "control", Subdomains: []extract.Subdomain{{Name: "Signals", Slug: "signals", Entities: []extract.Node{{ID: "c", Name: "Controller C", Members: []string{"c-member"}, Label: "Entity", Confidence: 1}}}}}, {Name: "Operations", Slug: "operations", Subdomains: []extract.Subdomain{{Name: "Equipment", Slug: "equipment", Entities: []extract.Node{{ID: "p", Name: "Atlas pump", Members: []string{"p-member"}, Label: "Entity", Confidence: 1}}}}}}}
	fullReport := extract.NewExtractor(&v014ChangedAcceptanceClient{}, 65536).ProcessRelations(context.Background(), full, docs, originals, extract.RelationProcessOptions{MaxRetries: -1})
	if !reflect.DeepEqual(v014SelectedSignatures(fullReport.Selected), v014SelectedSignatures(report.Selected)) {
		t.Fatalf("full/changed helper evidence differs: full=%+v changed=%+v", fullReport.Selected, report.Selected)
	}
}
