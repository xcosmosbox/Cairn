package incremental

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

// 原缺陷把调用方取消的既有节点读取报为 unavailable；独立验收保留完整混合分母。
// Test the observable outcome contract through SQLite, without replacing its read path.
func TestV013CancellationAcceptanceMixedIntendedNodes(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"cancel", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			f := v013CancellationFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			if mode == "deadline" {
				cancel()
				ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			}
			cancel()
			calls := 0
			client := v013IncrementalAcceptanceClient(func(context.Context, llm.CompleteRequest) (*llm.CompleteResponse, error) {
				calls++
				return nil, context.Canceled
			})
			r := newReflower(client, 4096, f.st)
			written := r.fuseDescriptions(ctx, f.dirty, f.ao, newRunState())
			outcomes := r.nodeDescriptionOutcomes()
			if calls != 0 || len(written) != 0 || len(outcomes) != 3 {
				t.Fatalf("cancelled intended scope lost or executed: calls=%d writes=%v outcomes=%+v", calls, written, outcomes)
			}
			for i, out := range outcomes {
				if out.ID != f.ids[i] || out.Domain != "operations" || out.Subdomain != "pumps" || out.Outcome != annotation.DocumentCancelled || !strings.Contains(out.Reason, ctx.Err().Error()) || len(out.Attempts) != 0 {
					t.Errorf("cancelled intended node lacks factual identity/reason: %+v; want %s", out, ctx.Err())
				}
			}
			f.assertStoredPair(t)
			if f.ao.pending[f.ids[1]].Summary != f.summary {
				t.Fatal("cancelled new candidate summary was replaced")
			}
		})
	}
}

// 成功兄弟仍可应用；之后取消只终止剩余节点，不重跑或吞掉任何节点。
// Cancellation during later generation must retain the already completed sibling.
func TestV013CancellationAcceptanceRetainsCompletedSibling(t *testing.T) {
	t.Parallel()
	f := v013CancellationFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	client := v013IncrementalAcceptanceClient(func(_ context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
		calls++
		if calls == 3 {
			cancel()
			return nil, ctx.Err()
		}
		return f.response(t, req), nil
	})
	r := newReflower(client, 4096, f.st)
	written := r.fuseDescriptions(ctx, f.dirty, f.ao, newRunState())
	outcomes := r.nodeDescriptionOutcomes()
	if calls != 3 || len(written) != 1 || written[f.ids[0]] != f.description || len(outcomes) != 3 {
		t.Fatalf("completed sibling lost or replayed: calls=%d writes=%v outcomes=%+v", calls, written, outcomes)
	}
	for i, out := range outcomes {
		want := annotation.DocumentCancelled
		if i == 0 {
			want = annotation.DocumentComplete
		}
		if out.ID != f.ids[i] || out.Outcome != want || (i > 0 && !strings.Contains(out.Reason, context.Canceled.Error())) {
			t.Errorf("incorrect completed/cancelled scope: %+v", out)
		}
	}
	f.assertStoredPair(t)
}

func TestV013CancellationAcceptanceHealthyAndRealStoreFailures(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"healthy", "closed-store", "missing-node"} {
		t.Run(mode, func(t *testing.T) {
			f := v013CancellationFixture(t)
			if mode == "closed-store" {
				if err := f.db.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "missing-node" {
				if _, err := f.st.nodes.Delete(context.Background(), f.ids[0]); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			client := v013IncrementalAcceptanceClient(func(_ context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
				calls++
				return f.response(t, req), nil
			})
			r := newReflower(client, 4096, f.st)
			written := r.fuseDescriptions(context.Background(), f.dirty, f.ao, newRunState())
			outcomes := r.nodeDescriptionOutcomes()
			if len(outcomes) != 3 {
				t.Fatalf("intended denominator lost: %+v", outcomes)
			}
			complete := 0
			for i, out := range outcomes {
				want := annotation.DocumentComplete
				if (mode == "closed-store" && i != 1) || (mode == "missing-node" && i == 0) {
					want = annotation.DocumentUnavailable
				}
				if out.ID != f.ids[i] || out.Outcome != want || (want == annotation.DocumentUnavailable && out.Reason == "") {
					t.Errorf("healthy/store failure misclassified: %+v want %s", out, want)
				}
				if want == annotation.DocumentComplete {
					complete++
					if written[out.ID] != f.description {
						t.Errorf("healthy sibling replacement absent: %+v", out)
					}
				} else if _, ok := written[out.ID]; ok {
					t.Error("unavailable existing node authorized replacement")
				}
			}
			if calls != 2*complete || len(written) != complete {
				t.Fatalf("unavailable nodes retried or healthy siblings lost: calls=%d writes=%v", calls, written)
			}
			if mode == "healthy" {
				f.assertStoredPair(t)
			}
		})
	}
}

type v013CancellationCase struct {
	db                   *storage.DB
	st                   *stores
	ao                   *alignOutcome
	dirty                *DirtySet
	ids                  []string
	summary, description string
}

func v013CancellationFixture(t *testing.T) *v013CancellationCase {
	t.Helper()
	db, err := storage.NewDB(storage.DBOptions{Path: filepath.Join(t.TempDir(), "graph.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	f := &v013CancellationCase{db: db, st: newStores(db), ids: []string{"a-existing", "b-new", "c-existing"}, summary: "Qualified pump operation", description: "Do not operate during maintenance. Operate only above 2 bar."}
	f.ao = &alignOutcome{pending: map[string]*pendingNode{}, unitsByID: map[string]*alignUnit{"member": {ID: "member", Skill: "ops", FilePath: "new.md", Content: "Operate only above 2 bar."}}}
	f.dirty = NewDirtySet()
	for i, id := range f.ids {
		pn := &pendingNode{UUID: id, IsNew: i == 1, Label: "Entity", Name: "Pump", Summary: f.summary, Domain: "operations", Subdomain: "pumps", NewMembers: []string{"member"}}
		f.ao.pending[id] = pn
		f.dirty.DirtyNodeFused(id, SubdomainKey{pn.Domain, pn.Subdomain})
		if !pn.IsNew {
			n := &dktypes.Node{ID: id, Label: dktypes.LabelEntity, Name: pn.Name, Summary: f.summary, Description: "Do not operate during maintenance.", Domain: pn.Domain, Subdomain: pn.Subdomain, Provenance: dktypes.ProvenanceExtraction, Confidence: 1}
			if err := f.st.nodes.Insert(context.Background(), n); err != nil {
				t.Fatal(err)
			}
		}
	}
	return f
}

func (f *v013CancellationCase) response(t *testing.T, req llm.CompleteRequest) *llm.CompleteResponse {
	t.Helper()
	if strings.Contains(req.System, "语义核验专家") {
		return &llm.CompleteResponse{Text: `{"supported":true,"reason":"source conditions and prohibition retained","unsupported_items":[],"omissions":[]}`, FinishReason: "stop"}
	}
	alias := regexp.MustCompile(`Node alias=([^\s]+)`).FindStringSubmatch(req.User)
	if len(alias) != 2 {
		t.Fatal("generation request lacks call-scoped alias")
	}
	b, err := json.Marshal(map[string]any{"descriptions": []map[string]string{{"node_id": alias[1], "summary": f.summary, "description": f.description}}})
	if err != nil {
		t.Fatal(err)
	}
	return &llm.CompleteResponse{Text: string(b), FinishReason: "stop"}
}

func (f *v013CancellationCase) assertStoredPair(t *testing.T) {
	t.Helper()
	for _, id := range []string{f.ids[0], f.ids[2]} {
		n, err := f.st.nodes.GetByID(context.Background(), id)
		if err != nil || n == nil || n.ID != id || n.Summary != f.summary || n.Description != "Do not operate during maintenance." {
			t.Fatalf("existing pair/identity changed: %+v err=%v", n, err)
		}
	}
}
