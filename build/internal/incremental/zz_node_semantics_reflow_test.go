package incremental

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

type nodeReflowClient struct {
	requests             []llm.CompleteRequest
	summary, description string
	bad                  string
	finish               string
}

func (c *nodeReflowClient) ProviderName() string { return "offline-node-reflow" }
func (c *nodeReflowClient) Complete(_ context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
	c.requests = append(c.requests, req)
	if c.bad != "" {
		return &llm.CompleteResponse{Text: c.bad, FinishReason: c.finish}, nil
	}
	if strings.Contains(req.System, "语义核验专家") {
		return &llm.CompleteResponse{Text: `{"supported":true,"reason":"complete source-qualified node","unsupported_items":[],"omissions":[]}`, FinishReason: "stop"}, nil
	}
	b, _ := json.Marshal(map[string]any{"descriptions": []map[string]string{{"node_id": "pumps#1", "summary": c.summary, "description": c.description}}})
	return &llm.CompleteResponse{Text: string(b), FinishReason: "stop"}, nil
}

func nodeReflowStore(t *testing.T, provenance dktypes.Provenance) (*stores, *dktypes.Node) {
	t.Helper()
	db, err := storage.NewDB(storage.DBOptions{Path: filepath.Join(t.TempDir(), "graph.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st := newStores(db)
	n := &dktypes.Node{ID: "11111111-1111-5111-8111-111111111111", Label: dktypes.LabelEntity, Name: "Atlas pump", Summary: "Qualified Atlas pump", Description: "The pump must not run during maintenance.", Domain: "operations", Subdomain: "pumps", Provenance: provenance, Confidence: .9}
	if err := st.nodes.Insert(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	return st, n
}

// 既有 I-8 只能更新 description；修正摘要不能冒充可完整落地的结果，也不能破坏原内容。
func TestNodeReflowExistingSummaryRepairIsExplicitPartialAndRetainsOldPair(t *testing.T) {
	t.Parallel()
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprint(changed), func(t *testing.T) {
			st, n := nodeReflowStore(t, dktypes.ProvenanceLLMInferred)
			summary := n.Summary
			if changed {
				summary = "Atlas pump with qualified operation and uncertain calibration"
			}
			body := n.Description + " It may run only above 2 bar. Calibration is uncertain."
			c := &nodeReflowClient{summary: summary, description: body}
			r := newReflower(c, 4096, st)
			ao := &alignOutcome{pending: map[string]*pendingNode{n.ID: {UUID: n.ID, Domain: n.Domain, Subdomain: n.Subdomain, NewMembers: []string{"member"}}}, unitsByID: map[string]*alignUnit{"member": {ID: "member", Content: "It may run only above 2 bar.", Detail: "Calibration is uncertain.", FilePath: "new.md"}}}
			dirty := NewDirtySet()
			dirty.DirtyNodeFused(n.ID, SubdomainKey{n.Domain, n.Subdomain})
			written := r.fuseDescriptions(context.Background(), dirty, ao, newRunState())
			outcomes := r.nodeDescriptionOutcomes()
			if len(outcomes) != 1 || outcomes[0].Summary != summary || outcomes[0].Description != body || len(c.requests) != 2 {
				t.Fatalf("missing factual correction: %+v", outcomes)
			}
			if changed {
				if len(written) != 0 || outcomes[0].Outcome != annotation.DocumentPartial || !strings.Contains(outcomes[0].Reason, "persistence hook") {
					t.Fatalf("unapplied summary certified: %v %+v", written, outcomes)
				}
			} else if written[n.ID] != body || outcomes[0].Outcome != annotation.DocumentComplete {
				t.Fatalf("valid unchanged-summary pair unavailable: %v %+v", written, outcomes)
			}
			stored, err := st.nodes.GetByID(context.Background(), n.ID)
			if err != nil || stored.Summary != n.Summary || stored.Description != n.Description || stored.ID != n.ID {
				t.Fatalf("helper changed existing node: %+v %v", stored, err)
			}
			for _, req := range c.requests {
				if strings.Contains(req.User, n.ID) || !strings.Contains(req.User, n.Description) || !strings.Contains(req.User, "only above 2 bar") || !strings.Contains(req.User, "uncertain") {
					t.Fatal("machine UUID leaked or full old/new source absent")
				}
			}
		})
	}
}

// 多篇同 member 的素材必须逐来源进入新节点；重用 reflower 不得重放上轮成功摘要。
func TestNodeReflowNewSummaryAndAllSourcesAreCurrentRunOnly(t *testing.T) {
	t.Parallel()
	c := &nodeReflowClient{summary: "Source-qualified pump", description: "Only above 2 bar; prohibited during maintenance; calibration uncertain. Recorded literal 123e4567-e89b-12d3-a456-426614174000."}
	r := newReflower(c, 4096, nil)
	key := SubdomainKey{"operations", "pumps"}
	dirty := NewDirtySet()
	dirty.DirtyNodeFused("new-uuid", key)
	a := &alignUnit{ID: "member", Name: "Atlas pump", Skill: "ops", FilePath: "a.md", Content: "Only above 2 bar.", Detail: "Prohibited during maintenance."}
	b := &alignUnit{ID: "member", Name: "Atlas pump", Skill: "ops", FilePath: "b.md", Content: "Calibration uncertain.", Detail: "Recorded literal 123e4567-e89b-12d3-a456-426614174000."}
	ao := &alignOutcome{pending: map[string]*pendingNode{"new-uuid": {UUID: "new-uuid", IsNew: true, Name: "Atlas pump", Label: "Entity", Summary: "old candidate", Domain: key.Domain, Subdomain: key.Subdomain, NewMembers: []string{"member"}}}, unitsByID: map[string]*alignUnit{"member": a}, sourcesByID: map[string][]*alignUnit{"member": {a, b}}}
	written := r.fuseDescriptions(context.Background(), dirty, ao, newRunState())
	if written["new-uuid"] != c.description || ao.pending["new-uuid"].Summary != c.summary || len(c.requests) != 2 {
		t.Fatalf("verified new pair not applied: %v %+v", written, ao.pending)
	}
	for _, req := range c.requests {
		for _, material := range []string{a.FilePath, b.FilePath, a.Content, a.Detail, b.Content, b.Detail} {
			if !strings.Contains(req.User, material) {
				t.Fatalf("source-bound contribution omitted %q", material)
			}
		}
	}
	ao.pending["new-uuid"].Summary = "second unresolved candidate"
	ao.sourcesByID = map[string][]*alignUnit{"member": {{ID: "member"}}}
	written = r.fuseDescriptions(context.Background(), dirty, ao, newRunState())
	outcomes := r.nodeDescriptionOutcomes()
	if len(written) != 0 || ao.pending["new-uuid"].Summary != "second unresolved candidate" || len(outcomes) != 1 || outcomes[0].Outcome != annotation.DocumentUnavailable || len(c.requests) != 2 {
		t.Fatalf("previous run replayed: %v %+v %+v", written, ao.pending, outcomes)
	}
	ao.sourcesByID = map[string][]*alignUnit{"member": {{ID: "member", Content: "Only above 2 bar.", Detail: "Prohibited during maintenance."}}}
	written = r.fuseDescriptions(context.Background(), dirty, ao, newRunState())
	if len(written) != 0 || len(c.requests) != 2 || r.nodeDescriptionOutcomes()[0].Outcome != annotation.DocumentUnavailable {
		t.Fatal("unknown source path certified as a bound source contribution")
	}
}

func TestNodeReflowHumanProtectionDoesNotCertifyNewMaterial(t *testing.T) {
	t.Parallel()
	st, n := nodeReflowStore(t, dktypes.ProvenanceHumanCurated)
	c := &nodeReflowClient{}
	r := newReflower(c, 4096, st)
	ao := &alignOutcome{pending: map[string]*pendingNode{n.ID: {UUID: n.ID, NewMembers: []string{"member"}}}, unitsByID: map[string]*alignUnit{"member": {ID: "member", Content: "New machine material must not overwrite human content."}}}
	dirty := NewDirtySet()
	dirty.DirtyNodeFused(n.ID, SubdomainKey{n.Domain, n.Subdomain})
	written := r.fuseDescriptions(context.Background(), dirty, ao, newRunState())
	outcomes := r.nodeDescriptionOutcomes()
	if len(written) != 0 || len(c.requests) != 0 || len(outcomes) != 1 || outcomes[0].Outcome != annotation.DocumentPartial || outcomes[0].Description != n.Description || !strings.Contains(outcomes[0].Reason, "human_curated") {
		t.Fatalf("human overwrite or false completeness: %v %+v", written, outcomes)
	}
}

// 原先 length 和同 alias 最后覆盖都被接受；共享 helper 的失败结果不能进入更新 map。
func TestNodeReflowRejectsTruncatedDuplicateAndMissingMaterial(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ text, finish, material string }{
		{`{"descriptions":[{"node_id":"pumps#1","summary":"Atlas","description":"Incomplete"}]}`, "length", "Only above 2 bar"},
		{`{"descriptions":[{"node_id":"pumps#1","summary":"first","description":"First"},{"node_id":"pumps#1","summary":"last","description":"Last"}]}`, "stop", "Only above 2 bar"},
		{"unused", "stop", ""},
	} {
		c := &nodeReflowClient{bad: tc.text, finish: tc.finish}
		r := newReflower(c, 4096, nil)
		out := r.fuseSubdomainBatch(context.Background(), SubdomainKey{"operations", "pumps"}, []*reflowNode{{uuid: "stable-uuid", name: "Atlas pump", summary: "Atlas", existing: "No operation during maintenance", newMaterial: tc.material}}, newRunState())
		outcomes := r.nodeDescriptionOutcomes()
		if len(out) != 0 || len(outcomes) != 1 || outcomes[0].Outcome != annotation.DocumentUnavailable {
			t.Fatalf("bad replacement applied: %v %+v", out, outcomes)
		}
		want := 4
		if tc.material == "" {
			want = 0
		}
		if len(c.requests) != want {
			t.Fatalf("wrong finite allowance: got %d want %d", len(c.requests), want)
		}
	}
}
