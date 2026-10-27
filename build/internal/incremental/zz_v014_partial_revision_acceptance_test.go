package incremental

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

const (
	v014PartialOld       = "A always depends on B."
	v014PartialCorrected = "From 2025 A depends on B only when bypass is off."
	v014PartialEarlier   = "Before 2025 A depends on B only during startup."
	v014PartialGood      = "C references D."
	v014PartialUnknown   = "C references A only during inspection."
)

type v014PartialRevisionClient struct {
	generation int
	verified   map[string]int
}

func (*v014PartialRevisionClient) ProviderName() string { return "offline-partial-revision-acceptance" }
func (c *v014PartialRevisionClient) Complete(_ context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
	respond := func(v any) (*llm.CompleteResponse, error) {
		b, err := json.Marshal(v)
		return &llm.CompleteResponse{Text: string(b), FinishReason: "stop"}, err
	}
	if strings.Contains(req.System, "SOURCE-RELATION GENERATE") {
		var input struct {
			Nodes []struct {
				Alias   string
				Members []string
			}
			Feedback string
		}
		if err := json.Unmarshal([]byte(req.User), &input); err != nil {
			return nil, err
		}
		aliases := map[string]string{}
		for _, n := range input.Nodes {
			for _, member := range n.Members {
				aliases[member] = n.Alias
			}
		}
		c.generation++
		first, last := v014PartialOld, "C always references A."
		if c.generation == 2 {
			if !strings.Contains(input.Feedback, "whole set") {
				return nil, fmt.Errorf("revision lost whole-set critique")
			}
			first, last = v014PartialCorrected, v014PartialUnknown
		}
		return respond(map[string]any{"relations": []extract.Relation{
			{Source: aliases["m-a"], Target: aliases["m-b"], Kind: "depends_on", Description: first, Confidence: .9},
			{Source: aliases["m-a"], Target: aliases["m-b"], Kind: "depends_on", Description: v014PartialEarlier, Confidence: .9},
			{Source: aliases["m-c"], Target: aliases["m-d"], Kind: "references", Description: v014PartialGood, Confidence: .9},
			{Source: aliases["m-c"], Target: aliases["m-a"], Kind: "references", Description: last, Confidence: .9},
		}})
	}
	if strings.Contains(req.System, "SOURCE-RELATION COVERAGE") {
		return respond(map[string]any{"complete": true, "supported": true, "reason": "all explicit source relations accounted for", "critique": ""})
	}
	var input struct {
		Relation       extract.Relation `json:"relation_candidate"`
		Source, Target extract.CrossEndpoint
		Sources        []struct {
			ID, Content string
			Members     []string
		} `json:"original_sources"`
	}
	if err := json.Unmarshal([]byte(req.User), &input); err != nil {
		return nil, err
	}
	c.verified[input.Relation.Description]++
	if input.Relation.Description == "C always references A." {
		return respond(map[string]any{"disposition": "rejected", "reason": "Repair the whole set: C references A only during inspection; the preceding A/B assertion must say from 2025 only when bypass is off, never always."})
	}
	if input.Relation.Description == v014PartialUnknown {
		return respond(map[string]any{"disposition": "insufficient_evidence", "reason": "inspection relationship assessment remains unknown"})
	}
	evidence := []map[string]string{}
	for role, endpoint := range map[string]extract.CrossEndpoint{"source": input.Source, "target": input.Target} {
		for _, source := range input.Sources {
			for _, member := range source.Members {
				if len(endpoint.Members) > 0 && member == endpoint.Members[0] {
					evidence = append(evidence, map[string]string{"endpoint": role, "member_id": member, "source_id": source.ID, "quote": source.Content})
				}
			}
		}
	}
	return respond(map[string]any{"disposition": "supported", "reason": "controlled assessment accepts this candidate", "relation_supported": true, "direction_supported": true, "explicit_source_assertion": true, "evidence": evidence})
}

// 原失败模式：修订集合的另一条边耗尽后，已被替换的新增假断言仍带 explicit 信用进入 I8。
// Exercise the actual changed helper and SQLite; persistent old text has a separate legacy obligation.
func TestV014PartialRevisionChangedSQLiteAcceptance(t *testing.T) {
	t.Parallel()
	for _, persistentOld := range []bool{false, true} {
		t.Run(fmt.Sprintf("persistent_old_%t", persistentOld), func(t *testing.T) {
			ctx := context.Background()
			db, err := storage.NewDB(storage.DBOptions{Path: filepath.Join(t.TempDir(), "partial.db")})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			st := newStores(db)
			key := SubdomainKey{"fixture", "partial-revision"}
			text := v014PartialCorrected + " " + v014PartialEarlier + " " + v014PartialGood + " " + v014PartialUnknown
			doc := &dktypes.AnnotatedDocument{Skill: "fixture", FilePath: "relations.md"}
			for _, id := range []string{"a", "b", "c", "d"} {
				if err := st.nodes.Insert(ctx, &dktypes.Node{ID: id, Label: dktypes.LabelConcept, Name: strings.ToUpper(id), Summary: text, Description: text, Domain: key.Domain, Subdomain: key.Subdomain, Provenance: dktypes.ProvenanceLLMInferred, Confidence: .9}); err != nil {
					t.Fatal(err)
				}
				if err := st.sources.InsertBatch(ctx, []storage.NodeSource{{NodeUUID: id, MemberID: "m-" + id, Skill: doc.Skill, FilePath: doc.FilePath}}); err != nil {
					t.Fatal(err)
				}
				doc.Items = append(doc.Items, dktypes.AnnotatedItem{ID: "m-" + id, Name: strings.ToUpper(id), Content: text, Detail: text})
			}
			var old *dktypes.Edge
			if persistentOld {
				edge := &dktypes.Edge{SourceID: "a", TargetID: "b", Kind: dktypes.KindDependsOn, Description: v014PartialOld, Properties: `{"historical":"retain"}`, Provenance: dktypes.ProvenanceLLMInferred, Confidence: .73, SourceRefs: "historical-source", Bidirectional: 1, Cardinality: "1:N", CreatedAt: time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)}
				if err := st.edges.Insert(ctx, edge); err != nil {
					t.Fatal(err)
				}
				rows, err := st.edges.GetOutgoing(ctx, "a", 0)
				if err != nil || len(rows) != 1 {
					t.Fatalf("old row fixture missing: %v %v", rows, err)
				}
				old = rows[0]
			}
			client := &v014PartialRevisionClient{verified: map[string]int{}}
			r := newReflower(client, 4096, st)
			dirty := NewDirtySet()
			dirty.DirtyNodeFused("a", key)
			rs := newRunState()
			final, kept := r.recomputeRelationsWithSources(ctx, dirty, nil, nil, []*dktypes.AnnotatedDocument{doc}, []extract.FrozenSource{extract.FreezeSource(doc.Skill, doc.FilePath, text)}, extract.RelationProcessOptions{MaxRetries: 1}, rs)
			report := r.relationProcessReport()
			if len(report.Scopes) != 1 || report.Scopes[0].Outcome != annotation.DocumentUnavailable || !report.Scopes[0].Exhausted || !strings.Contains(report.Scopes[0].Reason, "unknown") || len(report.Scopes[0].Attempts) != 10 {
				t.Fatalf("partial revision lost truthful finite scope: %+v", report)
			}
			if client.generation != 2 || client.verified[v014PartialEarlier] != 1 || client.verified[v014PartialGood] != 1 || client.verified[v014PartialUnknown] != 1 {
				t.Fatalf("successful siblings replayed or budget reset: generation=%d verification=%v", client.generation, client.verified)
			}
			selected := map[string]bool{}
			for _, decision := range report.Selected {
				selected[decision.Relation.Description] = true
			}
			wantSelected := map[string]bool{v014PartialCorrected: true, v014PartialEarlier: true, v014PartialGood: true}
			if !reflect.DeepEqual(selected, wantSelected) {
				t.Errorf("superseded assertion remains newly selected, or good facet lost: %v", selected)
			}
			checkFacets := func(description string) {
				t.Helper()
				var envelope reflowAssertionEnvelope
				if err := json.Unmarshal([]byte(description), &envelope); err != nil {
					t.Fatal(err)
				}
				explicit, legacy := map[string]bool{}, false
				for _, facet := range envelope.Assertions {
					if facet.Classification == "explicit" {
						explicit[facet.Description] = true
					}
					if facet.Description == v014PartialOld {
						if !persistentOld || facet.Classification != "legacy_unverified" {
							t.Errorf("superseded NEW assertion gained explicit/persistent credit: %+v", facet)
						}
						legacy = facet.Classification == "legacy_unverified"
					}
				}
				if !reflect.DeepEqual(explicit, map[string]bool{v014PartialCorrected: true, v014PartialEarlier: true}) || legacy != persistentOld {
					t.Errorf("same-triple qualifiers/history mismatch: %s", description)
				}
			}
			for _, relation := range final[key] {
				if relation.Source == "a" && relation.Target == "b" {
					checkFacets(relation.Description)
				}
			}
			if len(final[key]) != 2 {
				t.Fatalf("corrected same-triple facets or independent good relation missing: %+v", final)
			}
			if err := applyIncrementalIngest(ctx, st, dirty, nil, &reflowResult{relations: final, keptCross: kept}, rs); err != nil {
				t.Fatal(err)
			}
			rows, err := st.edges.GetOutgoing(ctx, "a", 0)
			if err != nil || len(rows) != 1 {
				t.Fatalf("unexpected SQLite A rows: %v %v", rows, err)
			}
			checkFacets(rows[0].Description)
			if persistentOld {
				want := *old
				want.Description = rows[0].Description
				if !reflect.DeepEqual(&want, rows[0]) {
					t.Errorf("nonzero-confidence old metadata changed: before=%+v after=%+v", old, rows[0])
				}
			}
			good, err := st.edges.GetOutgoing(ctx, "c", 0)
			if err != nil || len(good) != 1 || good[0].TargetID != "d" {
				t.Fatalf("independent good relation lost or unknown relation applied: %v %v", good, err)
			}
		})
	}
}
