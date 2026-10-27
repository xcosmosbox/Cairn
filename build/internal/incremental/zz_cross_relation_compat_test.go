package incremental

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

type crossCompatClient struct{ calls int }

func (c *crossCompatClient) ProviderName() string { return "offline-cross-compat" }
func (c *crossCompatClient) Complete(_ context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
	c.calls++
	if strings.Contains(req.User, "11111111-1111-5111-8111-111111111111") || strings.Contains(req.User, "22222222-2222-5222-8222-222222222222") || strings.Contains(req.User, "external target") {
		return nil, fmt.Errorf("UUID or cross target entered local relation prompt")
	}
	return &llm.CompleteResponse{Text: `{"relations":[]}`, FinishReason: "stop"}, nil
}

// 新的全量跨边必须能经历既有 I6/I7/I8：人工正文不被重融合、跨边不进局部 LLM，且完整列稳定。
func TestCrossRelationIncrementalCompatibilityPreservesHumanAndEdgeMetadata(t *testing.T) {
	t.Parallel()
	for _, localNeighbor := range []bool{false, true} {
		t.Run(fmt.Sprint(localNeighbor), func(t *testing.T) {
			ctx := context.Background()
			db, err := storage.NewDB(storage.DBOptions{Path: filepath.Join(t.TempDir(), "graph.db")})
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			st := newStores(db)
			sourceID, targetID := "11111111-1111-5111-8111-111111111111", "22222222-2222-5222-8222-222222222222"
			source := &dktypes.Node{ID: sourceID, Name: "Human admission", Summary: "Human summary", Description: "Human curated description.", Label: dktypes.LabelConcept, Domain: "engineering", Subdomain: "admission", Provenance: dktypes.ProvenanceHumanCurated, Confidence: 1}
			target := &dktypes.Node{ID: targetID, Name: "external target", Summary: "Quota", Description: "Quota prerequisite", Label: dktypes.LabelConcept, Domain: "engineering", Subdomain: "capacity", Provenance: dktypes.ProvenanceLLMInferred, Confidence: 1}
			for _, node := range []*dktypes.Node{source, target} {
				if err := st.nodes.Insert(ctx, node); err != nil {
					t.Fatal(err)
				}
			}
			if localNeighbor {
				if err := st.nodes.Insert(ctx, &dktypes.Node{ID: "33333333-3333-5333-8333-333333333333", Name: "Local helper", Summary: "Helper", Description: "Local helper", Label: dktypes.LabelConcept, Domain: source.Domain, Subdomain: source.Subdomain, Provenance: dktypes.ProvenanceLLMInferred, Confidence: 1}); err != nil {
					t.Fatal(err)
				}
			}
			edge := &dktypes.Edge{SourceID: sourceID, TargetID: targetID, Kind: dktypes.KindDependsOn, Description: "Admission needs quota", Properties: `{"audit":"original"}`, Provenance: dktypes.ProvenanceHumanCurated, Confidence: .93, SourceRefs: "source-a,source-b", Bidirectional: 1, Cardinality: "1:N", CreatedAt: time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)}
			if err := st.edges.Insert(ctx, edge); err != nil {
				t.Fatal(err)
			}
			before, err := st.edges.GetOutgoing(ctx, sourceID, 0)
			if err != nil {
				t.Fatal(err)
			}
			ao := &alignOutcome{pending: map[string]*pendingNode{sourceID: {UUID: sourceID, Domain: source.Domain, Subdomain: source.Subdomain, NewMembers: []string{"new-member"}}}, unitsByID: map[string]*alignUnit{"new-member": {ID: "new-member", Detail: "New material must not overwrite human text."}}}
			dirty := NewDirtySet()
			dirty.DirtyNodeFused(sourceID, SubdomainKey{source.Domain, source.Subdomain})
			rs := newRunState()
			client := &crossCompatClient{}
			reflower := newReflower(client, 4096, st)
			descriptions := reflower.fuseDescriptions(ctx, dirty, ao, rs)
			if len(descriptions) != 0 || client.calls != 0 {
				t.Fatalf("human text was sent for fusion: descriptions=%v calls=%d", descriptions, client.calls)
			}
			relations, kept := reflower.recomputeRelations(ctx, dirty, ao, descriptions, rs)
			key := SubdomainKey{source.Domain, source.Subdomain}
			if _, ok := relations[key]; !ok || len(kept[key]) != 1 {
				t.Fatalf("crossedge not retained by I7: relations=%v kept=%v", relations, kept)
			}
			if err := applyIncrementalIngest(ctx, st, dirty, ao, &reflowResult{descriptions: descriptions, relations: relations, keptCross: kept}, rs); err != nil {
				t.Fatal(err)
			}
			after, err := st.edges.GetOutgoing(ctx, sourceID, 0)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("crossedge identity/metadata changed: before=%+v after=%+v", before[0], after)
			}
			node, err := st.nodes.GetByID(ctx, sourceID)
			if err != nil || node == nil || node.ID != sourceID || node.Description != source.Description || node.Provenance != dktypes.ProvenanceHumanCurated {
				t.Fatalf("human content or pinned identity changed: %+v %v", node, err)
			}
			// v0.1.4 cannot certify new relations without original sources.
			wantCalls := 0
			if client.calls != wantCalls {
				t.Fatalf("local I7 path not exercised: calls=%d want=%d warnings=%v", client.calls, wantCalls, rs.warningsSnapshot())
			}
		})
	}
}
