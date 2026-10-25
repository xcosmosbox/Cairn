package incremental

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

type humanEdgeReflowClient struct{ text string }

func (c humanEdgeReflowClient) ProviderName() string { return "offline-human-edge-reflow" }
func (c humanEdgeReflowClient) Complete(context.Context, llm.CompleteRequest) (*llm.CompleteResponse, error) {
	return &llm.CompleteResponse{Text: c.text, FinishReason: "stop"}, nil
}

func humanEdgeReflowFixture(t *testing.T) (*storage.DB, *stores, SubdomainKey, string, string) {
	t.Helper()
	db, err := storage.NewDB(storage.DBOptions{Path: filepath.Join(t.TempDir(), "graph.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st := newStores(db)
	key := SubdomainKey{"engineering", "admission"}
	a, b := "11111111-1111-5111-8111-111111111111", "22222222-2222-5222-8222-222222222222"
	for _, id := range []string{a, b} {
		if err := st.nodes.Insert(context.Background(), &dktypes.Node{ID: id, Label: dktypes.LabelConcept, Name: "synthetic", Summary: "synthetic", Description: "Human-authored node", Domain: key.Domain, Subdomain: key.Subdomain, Provenance: dktypes.ProvenanceHumanCurated, Confidence: 1}); err != nil {
			t.Fatal(err)
		}
	}
	return db, st, key, a, b
}

// 回归独立 reviewer 的真实 I7→I8 复现：端点被标脏后，空输出删人工边；
// 返回同身份则把机器文本/置信度写在 human_curated 标志下。不能仅断言标志未变。
// Exercise actual dirty-subdomain recompute/ingest and compare every persisted field.
func TestTouchedHumanEdgesSurviveActualRecomputeAndIngest(t *testing.T) {
	t.Parallel()
	kinds := []dktypes.RelationKind{dktypes.KindTriggers, dktypes.KindDependsOn, dktypes.KindReferences, dktypes.KindGeneralizes, dktypes.KindComposes, dktypes.KindContradicts}
	for _, updated := range []bool{false, true} {
		for _, touched := range []string{"source", "target", "both"} {
			for _, mixedDuplicate := range []bool{false, true} {
				t.Run(fmt.Sprintf("updated=%t/touched=%s/mixed=%t", updated, touched, mixedDuplicate), func(t *testing.T) {
					_, st, key, a, b := humanEdgeReflowFixture(t)
					ctx := context.Background()
					if mixedDuplicate {
						if err := st.edges.Insert(ctx, &dktypes.Edge{SourceID: a, TargetID: b, Kind: dktypes.KindDependsOn, Description: "Old machine duplicate precedes human", Provenance: dktypes.ProvenanceLLMInferred, Confidence: .4}); err != nil {
							t.Fatal(err)
						}
					}
					var returned []map[string]any
					for _, kind := range kinds {
						edge := &dktypes.Edge{SourceID: a, TargetID: b, Kind: kind, Description: "Human-authored " + string(kind), Confidence: .93, Properties: `{"owner":"human"}`, Provenance: dktypes.ProvenanceHumanCurated, SourceRefs: "human-source-a,human-source-b", Bidirectional: 1, Cardinality: "M:N", CreatedAt: time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)}
						if err := st.edges.Insert(ctx, edge); err != nil {
							t.Fatal(err)
						}
						if updated {
							returned = append(returned, map[string]any{"source": "admission#1", "target": "admission#2", "kind": string(kind), "description": "LLM replacement condition", "confidence": .2})
						}
					}
					before, err := st.edges.ListAll(ctx)
					if err != nil {
						t.Fatal(err)
					}
					text := `{"relations":[]}`
					if updated {
						raw, err := json.Marshal(map[string]any{"relations": returned})
						if err != nil {
							t.Fatal(err)
						}
						text = string(raw)
					}
					dirty := NewDirtySet()
					if touched != "target" {
						dirty.DirtyNodeFused(a, key)
					}
					if touched != "source" {
						dirty.DirtyNodeFused(b, key)
					}
					rs := newRunState()
					rels, kept := newReflower(humanEdgeReflowClient{text}, 4096, st).recomputeRelations(ctx, dirty, nil, nil, rs)
					if generated, ok := rels[key]; !ok || len(generated) != 0 || len(kept[key]) != len(kinds) {
						t.Fatalf("I7 treated human identity as machine replacement: rels=%+v kept=%+v", rels, kept)
					}
					if err := applyIncrementalIngest(ctx, st, dirty, nil, &reflowResult{relations: rels, keptCross: kept}, rs); err != nil {
						t.Fatal(err)
					}
					after, err := st.edges.ListAll(ctx)
					if err != nil || !reflect.DeepEqual(before, after) {
						t.Fatalf("human payload changed: before=%+v after=%+v err=%v", before, after, err)
					}
				})
			}
		}
	}
}

// 人工节点不等于所有相关机器边也被冻结；保持 touched machine-only 的原有删除/更新语义。
func TestTouchedMachineEdgeRetainsAutomaticRecomputeBehavior(t *testing.T) {
	t.Parallel()
	for _, updated := range []bool{false, true} {
		t.Run(fmt.Sprint(updated), func(t *testing.T) {
			_, st, key, a, b := humanEdgeReflowFixture(t)
			ctx := context.Background()
			old := &dktypes.Edge{SourceID: a, TargetID: b, Kind: dktypes.KindDependsOn, Description: "Old machine condition", Confidence: .93, Properties: `{"keep":"metadata"}`, Provenance: dktypes.ProvenanceLLMInferred, SourceRefs: "machine-source", Bidirectional: 1, Cardinality: "1:N", CreatedAt: time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)}
			if err := st.edges.Insert(ctx, old); err != nil {
				t.Fatal(err)
			}
			before, err := st.edges.ListAll(ctx)
			if err != nil {
				t.Fatal(err)
			}
			text := `{"relations":[]}`
			if updated {
				text = `{"relations":[{"source":"admission#1","target":"admission#2","kind":"depends_on","description":"Updated machine condition","confidence":0.2}]}`
			}
			dirty := NewDirtySet()
			dirty.DirtyNodeFused(a, key)
			rs := newRunState()
			rels, kept := newReflower(humanEdgeReflowClient{text}, 4096, st).recomputeRelations(ctx, dirty, nil, nil, rs)
			if err := applyIncrementalIngest(ctx, st, dirty, nil, &reflowResult{relations: rels, keptCross: kept}, rs); err != nil {
				t.Fatal(err)
			}
			after, err := st.edges.ListAll(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if !updated {
				if len(after) != 0 {
					t.Fatalf("machine omission became protected: %+v", after)
				}
				return
			}
			want := *before[0]
			want.Description, want.Confidence = "Updated machine condition", .2
			if len(after) != 1 || !reflect.DeepEqual(after[0], &want) {
				t.Fatalf("machine update/metadata behavior changed: after=%+v want=%+v", after, want)
			}
		})
	}
}
