package incremental

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

type v013IncrementalAcceptanceClient func(context.Context, llm.CompleteRequest) (*llm.CompleteResponse, error)

func (f v013IncrementalAcceptanceClient) Complete(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
	return f(ctx, req)
}
func (v013IncrementalAcceptanceClient) ProviderName() string {
	return "v013-offline-incremental-acceptance"
}

func v013IncrementalWire(t *testing.T, v any) string {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}

func v013IncrementalNodeAlias(user string) (string, bool) {
	_, data, ok := strings.Cut(user, "## Node metadata / 节点元数据\n")
	var metadata struct {
		NodeID string `json:"node_id"`
	}
	if !ok || json.NewDecoder(strings.NewReader(data)).Decode(&metadata) != nil {
		return "", false
	}
	return metadata.NodeID, metadata.NodeID != ""
}

// Exercise real SQLite recall -> alias judge -> materialization -> shared node
// reflow. Scripted semantic feedback is a control, not new model/source acceptance.
func TestV013AcceptanceExistingIdentitySourcesAndProtectedBoundary(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := storage.NewDB(storage.DBOptions{Path: filepath.Join(t.TempDir(), "graph.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st := newStores(db)
	machine := &dktypes.Node{ID: "11111111-1111-5111-8111-111111111111", Label: dktypes.LabelEntity, Name: "Atlas pump", Summary: "Pump always runs.", Description: "The Atlas pump must not run during maintenance.", Domain: "operations", Subdomain: "pumps", Provenance: dktypes.ProvenanceExtraction, Confidence: 1}
	human := &dktypes.Node{ID: "22222222-2222-5222-8222-222222222222", Label: dktypes.LabelEntity, Name: "Curated pump", Summary: "Human curated summary.", Description: "PRIVATE HUMAN CONTENT MUST STAY OUTSIDE MODEL INPUT.", Domain: "operations", Subdomain: "pumps", Provenance: dktypes.ProvenanceHumanCurated, Confidence: 1}
	for _, n := range []*dktypes.Node{machine, human, {ID: "domain", Label: dktypes.LabelDomain, Name: "Operations", Domain: "operations", Provenance: dktypes.ProvenanceExtraction}, {ID: "subdomain", Label: dktypes.LabelSubdomain, Name: "Pumps", Domain: "operations", Subdomain: "pumps", Provenance: dktypes.ProvenanceExtraction}} {
		if err := st.nodes.Insert(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.fts.RebuildAll(ctx); err != nil {
		t.Fatal(err)
	}
	item := dktypes.AnnotatedItem{Tag: dktypes.TagEntity, Name: "Atlas pump", Content: "The Atlas pump may run only above 2 bar.", Detail: "Calibration is uncertain. Literal 123e4567-e89b-12d3-a456-426614174000.", Confidence: 1}
	docs := extract.AssignIDs([]*dktypes.AnnotatedDocument{
		{Skill: "operations", FilePath: "references/report-a.md", Items: []dktypes.AnnotatedItem{item}},
		{Skill: "operations", FilePath: "references/report-b.md", Items: []dktypes.AnnotatedItem{item}},
		{Skill: "operations", FilePath: "references/human-addition.md", Items: []dktypes.AnnotatedItem{{Tag: dktypes.TagEntity, Name: "Curated pump", Content: "Curated pump machine addition requires assessment.", Detail: "New material must remain unresolved while human content stays protected.", Confidence: 1}}},
	})
	member, humanMember := docs[0].Items[0].ID, docs[2].Items[0].ID
	alignCalls := 0
	alignClient := v013IncrementalAcceptanceClient(func(_ context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
		alignCalls++
		for _, secret := range []string{machine.ID, human.ID, human.Description} {
			if strings.Contains(req.User, secret) {
				t.Fatal("persistent identity or human prose leaked into placement")
			}
		}
		aliasFor := func(name string) string {
			pattern := `alias=([^\s]+) \[Entity\] ` + regexp.QuoteMeta(name) + `（`
			if name == human.Name {
				pattern = `alias=([^\s]+) \[Entity\] \(protected human object; content unavailable\)`
			}
			m := regexp.MustCompile(pattern).FindStringSubmatch(req.User)
			if len(m) != 2 {
				t.Fatalf("existing target %q missing from real recall: %s", name, req.User)
			}
			return m[1]
		}
		decisions := []alignDecision{}
		if alignCalls == 1 {
			for _, source := range []string{docs[0].FilePath, docs[1].FilePath, item.Content, item.Detail} {
				if !strings.Contains(req.User, source) {
					t.Fatal("placement omitted a bound duplicate-source contribution")
				}
			}
			decisions = append(decisions, alignDecision{Unit: member, Action: "merge_into", Target: aliasFor("Atlas pump")}, alignDecision{Unit: humanMember, Action: "merge_into", Target: human.ID})
		} else {
			if alignCalls != 2 || strings.Contains(req.User, "unit_id="+member+" ") {
				t.Fatal("alignment replayed successful sibling")
			}
			decisions = append(decisions, alignDecision{Unit: humanMember, Action: "merge_into", Target: aliasFor("Curated pump")})
		}
		return &llm.CompleteResponse{Text: v013IncrementalWire(t, alignLLMOutput{Decisions: decisions}), FinishReason: "stop"}, nil
	})
	a := newAligner(alignClient, 65536, st, 15)
	a.maxRetries = 1
	ao, err := a.align(ctx, docs, newRunState())
	if err != nil || ao.report.Outcome != "complete" || alignCalls != 2 || len(ao.report.Attempts) != 2 || ao.report.CoveredIncidences != 3 || len(ao.sourceRows) != 3 || len(ao.pending) != 2 {
		t.Fatalf("real alignment lost identity/sources: err=%v calls=%d", err, alignCalls)
	}
	if ao.pending[machine.ID] == nil || ao.pending[machine.ID].IsNew || ao.pending[human.ID] == nil || ao.pending[human.ID].IsNew {
		t.Fatal("existing target UUID was rederived")
	}
	if len(ao.sourcesByID[member]) != 2 {
		t.Fatal("duplicate member lost a distinct original")
	}
	correctSummary := "Qualified pump operation and uncertain calibration."
	correctDetail := machine.Description + " " + item.Content + " " + item.Detail
	reflowCalls := 0
	reflowClient := v013IncrementalAcceptanceClient(func(_ context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
		reflowCalls++
		for _, secret := range []string{machine.ID, human.ID, human.Description} {
			if strings.Contains(req.User, secret) {
				t.Fatal("persistent identity or curated text entered fusion model input")
			}
		}
		for _, source := range []string{machine.Description, item.Content, item.Detail, docs[0].FilePath, docs[1].FilePath} {
			if !strings.Contains(req.User, source) {
				t.Fatal("shared reflow omitted a bound source contribution")
			}
		}
		marker := "## 标注单元（数组下标从0开始） / annotated items\n"
		if strings.Contains(req.User, marker) {
			var items []dktypes.AnnotatedItem
			if json.Unmarshal([]byte(strings.Split(req.User, marker)[1]), &items) != nil || len(items) != 1 || items[0].Name != machine.Name || items[0].Content != correctSummary || items[0].Detail != correctDetail {
				t.Fatal("shared gate did not check corrected full candidate")
			}
			return &llm.CompleteResponse{Text: `{"supported":true,"reason":"old prohibition plus both new source-bound fields retained","unsupported_items":[],"omissions":[]}`, FinishReason: "stop"}, nil
		}
		alias, ok := v013IncrementalNodeAlias(req.User)
		if !ok {
			t.Fatal("no call-scoped fusion alias")
		}
		return &llm.CompleteResponse{Text: v013IncrementalWire(t, map[string]any{"descriptions": []map[string]string{{"node_id": alias, "summary": correctSummary, "description": correctDetail}}}), FinishReason: "stop"}, nil
	})
	dirty := NewDirtySet()
	key := SubdomainKey{"operations", "pumps"}
	dirty.DirtyNodeFused(machine.ID, key)
	dirty.DirtyNodeFused(human.ID, key)
	r := newReflower(reflowClient, 65536, st)
	written := r.fuseDescriptions(ctx, dirty, ao, newRunState())
	outcomes := r.nodeDescriptionOutcomes()
	if len(written) != 0 || reflowCalls != 2 || len(outcomes) != 2 {
		t.Fatal("unapplied summary repair/human protection appeared complete")
	}
	for _, out := range outcomes {
		if out.Outcome != annotation.DocumentPartial || out.Reason == "" {
			t.Fatal("legacy/protected boundary has no explicit unresolved scope")
		}
		if out.ID == machine.ID && (out.Summary != correctSummary || out.Description != correctDetail || out.CandidateID == "" || out.MaterialID == "" || len(out.Attempts) != 2 || !strings.Contains(out.Reason, "persistence hook")) {
			t.Fatal("verified correction absent from factual handoff")
		}
		if out.ID == human.ID && (!strings.Contains(out.Reason, "human_curated") || len(out.Attempts) != 0) {
			t.Fatal("human protection falsely certifies machine additions")
		}
	}
	for _, original := range []*dktypes.Node{machine, human} {
		stored, err := st.nodes.GetByID(ctx, original.ID)
		if err != nil || stored.ID != original.ID || stored.Summary != original.Summary || stored.Description != original.Description {
			t.Fatal("unresolved work overwrote pinned existing content")
		}
	}
	r.fuseDescriptions(ctx, NewDirtySet(), ao, newRunState())
	if len(r.nodeDescriptionOutcomes()) != 0 || reflowCalls != 2 {
		t.Fatal("a later empty run retained stale semantic outcomes")
	}
}
