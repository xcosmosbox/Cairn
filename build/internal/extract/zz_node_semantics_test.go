package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
)

type nodeReply struct {
	text, finish string
	err          error
	run          func()
}
type nodeSequenceClient struct {
	t        *testing.T
	replies  []nodeReply
	requests []llm.CompleteRequest
}

func (c *nodeSequenceClient) ProviderName() string { return "offline-node-semantics" }
func (c *nodeSequenceClient) Complete(_ context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
	c.requests = append(c.requests, req)
	i := len(c.requests) - 1
	if i >= len(c.replies) {
		c.t.Fatalf("unexpected node request %d: %s", i+1, req.System)
		return nil, fmt.Errorf("fixture exhausted")
	}
	r := c.replies[i]
	if r.run != nil {
		r.run()
	}
	if r.err != nil {
		return nil, r.err
	}
	finish := r.finish
	if finish == "" {
		finish = "stop"
	}
	return &llm.CompleteResponse{Text: r.text, FinishReason: finish}, nil
}

const nodeSupported = `{"supported":true,"reason":"all source substance and candidate assertions checked","unsupported_items":[],"omissions":[]}`
const nodeUnknown = `{"status":"unknown","reason":"assessment could not be completed"}`
const nodeQualifiedSource = "Atlas is a pump at Cedar Plant. It may run only when inlet pressure exceeds 2 bar, except during maintenance. It must not run during maintenance. On 2025-01-01 its limit was 8 bar; from 2025-06-01 its limit is 6 bar. The calibration result is uncertain. Report A estimates that calibration error on 2025-07-01 was 3 percent, with low confidence. Report B estimates 5 percent on that date, with low confidence. The recorded literal is 123e4567-e89b-12d3-a456-426614174000."

func nodeWire(alias, summary, description string) string {
	b, _ := json.Marshal(map[string]any{"descriptions": []map[string]string{{"node_id": alias, "summary": summary, "description": description}}})
	return string(b)
}
func nodeFixture() NodeDescriptionInput {
	return NodeDescriptionInput{ID: "stable-node-uuid", Label: "Entity", Name: "Atlas pump", Summary: "Atlas pump", Material: nodeQualifiedSource}
}

// 原先非空即通过；修复摘要时也必须重查整份候选，不能以补回一项为由丢掉阈值/分歧。
func TestNodeDescriptionsRepairSummaryAndRecheckWholeCandidate(t *testing.T) {
	t.Parallel()
	badSummary := "Atlas always runs with a certain 4 percent calibration error."
	c := &nodeSequenceClient{t: t, replies: []nodeReply{
		{text: nodeWire("pumps#1", badSummary, "Atlas may run above 2 bar.")},
		{text: `{"supported":false,"reason":"summary invents unconditional operation and settled error","unsupported_items":[0],"omissions":["maintenance prohibition, dates, attributed disagreement, uncertainty and literal UUID"]}`},
		{text: nodeWire("pumps#1", "Atlas pump with source-qualified operation and disputed calibration.", "Atlas must not run during maintenance.")},
		{text: `{"supported":true,"reason":"repair lost prior threshold and history","unsupported_items":[],"omissions":["pressure exceeds 2 bar; 8 bar before change, 6 bar from 2025-06-01; Report A 3 percent and Report B 5 percent on 2025-07-01, both low confidence; literal UUID"]}`},
		{text: nodeWire("pumps#1", "Atlas pump with source-qualified operation and disputed calibration.", nodeQualifiedSource)},
		{text: nodeSupported},
	}}
	out := FuseNodeDescriptions(context.Background(), c, 4096, 2, "operations", "pumps", []NodeDescriptionInput{nodeFixture()}).Nodes[0]
	if out.Outcome != annotation.DocumentComplete || out.Summary == badSummary || out.Description != nodeQualifiedSource || len(out.Attempts) != 6 || out.CandidateID == "" || out.MaterialID == "" {
		t.Fatalf("result %+v", out)
	}
	want := []string{"generate", "assess", "revise", "assess", "revise", "assess"}
	for i, a := range out.Attempts {
		if a.Step != want[i] || a.Number != i+1 {
			t.Fatalf("attempt %+v", a)
		}
	}
	for _, i := range []int{2, 4} {
		if !strings.Contains(c.requests[i].User, nodeQualifiedSource) || !strings.Contains(c.requests[i].User, "Complete candidate") || !strings.Contains(c.requests[i].User, "Concrete semantic feedback") {
			t.Fatal("revision omitted original, full candidate or concrete feedback")
		}
	}
	if !strings.Contains(c.requests[3].User, "Atlas must not run during maintenance.") || !strings.Contains(c.requests[5].User, nodeQualifiedSource) {
		t.Fatal("revised whole candidate not assessed")
	}
	for _, req := range c.requests {
		if strings.Contains(req.User, "stable-node-uuid") {
			t.Fatal("machine node identity leaked into model prompt")
		}
	}
	if out.Attempts[0].CandidateID == out.Attempts[2].CandidateID || out.Attempts[2].CandidateID == out.Attempts[4].CandidateID {
		t.Fatal("candidate identities did not bind revisions")
	}
}

// 未知与坏反馈重试同一候选；不能把它们当成修订指令或隐式通过。
func TestNodeDescriptionsUnknownReassessesExactCandidate(t *testing.T) {
	t.Parallel()
	for _, feedback := range []string{nodeUnknown, `{"supported":true,"reason":"missing omissions","unsupported_items":[]}`, `{"supported":false,"reason":"out of range","unsupported_items":[1],"omissions":[]}`} {
		c := &nodeSequenceClient{t: t, replies: []nodeReply{{text: nodeWire("pumps#1", "Atlas pump", nodeQualifiedSource)}, {text: feedback}, {text: nodeSupported}}}
		out := FuseNodeDescriptions(context.Background(), c, 0, 1, "operations", "pumps", []NodeDescriptionInput{nodeFixture()}).Nodes[0]
		if out.Outcome != annotation.DocumentComplete || len(out.Attempts) != 3 || !reflect.DeepEqual(c.requests[1], c.requests[2]) || out.Attempts[1].CandidateID != out.Attempts[2].CandidateID {
			t.Fatalf("assessment replaced or accepted unknown: %+v", out)
		}
	}
}

func TestNodeDescriptionsNewUnassessedRevisionIsUnavailable(t *testing.T) {
	t.Parallel()
	c := &nodeSequenceClient{t: t, replies: []nodeReply{
		{text: nodeWire("pumps#1", "Atlas pump", "Atlas pump.")},
		{text: `{"supported":true,"reason":"missing all qualifications","unsupported_items":[],"omissions":["complete qualified source"]}`},
		{text: nodeWire("pumps#1", "Atlas pump", nodeQualifiedSource)},
		{text: nodeUnknown},
	}}
	out := FuseNodeDescriptions(context.Background(), c, 0, 1, "operations", "pumps", []NodeDescriptionInput{nodeFixture()}).Nodes[0]
	if out.Outcome != annotation.DocumentUnavailable || !out.Exhausted || out.Description != nodeQualifiedSource || out.Attempts[1].Outcome != "rejected" {
		t.Fatalf("unknown latest candidate borrowed old verdict: %+v", out)
	}
}

// 生成正文的无用 metadata 不应消耗恢复预算；真正核验仍严格区分 unknown 与冲突反馈。
func TestNodeDescriptionsGenerationMetadataDoesNotChangeAssessmentRules(t *testing.T) {
	t.Parallel()
	wire, _ := json.Marshal(map[string]any{
		"note": "optional provider metadata", "confidence": .9,
		"descriptions": []map[string]any{{"node_id": "pumps#1", "summary": "Atlas pump", "description": nodeQualifiedSource, "confidence": .8, "note": "optional row metadata"}},
	})
	for _, feedback := range []string{nodeSupported, nodeUnknown, `{"status":"unknown","reason":"cannot assess","supported":true,"unsupported_items":[],"omissions":[]}`} {
		replies := []nodeReply{{text: string(wire)}, {text: feedback}}
		if feedback != nodeSupported {
			replies = append(replies, nodeReply{text: nodeSupported})
		}
		c := &nodeSequenceClient{t: t, replies: replies}
		out := FuseNodeDescriptions(context.Background(), c, 0, 1, "operations", "pumps", []NodeDescriptionInput{nodeFixture()}).Nodes[0]
		if out.Outcome != annotation.DocumentComplete || out.Description != nodeQualifiedSource || len(out.Attempts) != len(replies) || out.Attempts[1].Step != "assess" {
			t.Fatalf("benign generation metadata rejected: %+v", out)
		}
		if feedback != nodeSupported && (!reflect.DeepEqual(c.requests[1], c.requests[2]) || out.Attempts[1].Outcome == "complete") {
			t.Fatalf("unknown/conflicting assessment weakened: %+v", out)
		}
	}
}

// 既有截断/重复 alias/重复 JSON 字段曾覆盖正文；现在只重试失败生成，不评估坏候选。
func TestNodeDescriptionsMalformedGenerationRecoversWithoutAssessingIt(t *testing.T) {
	t.Parallel()
	valid := nodeWire("pumps#1", "Atlas pump", nodeQualifiedSource)
	for _, bad := range []nodeReply{
		{text: valid, finish: "length"},
		{text: nodeWire("invented-uuid", "Atlas", nodeQualifiedSource)},
		{text: `{"descriptions":[{"node_id":"pumps#1","summary":"first","description":"one"},{"node_id":"pumps#1","summary":"second","description":"two"}]}`},
		{text: `{"descriptions":[{"node_id":"pumps#1","summary":"one","description":"first","description":"last"}]}`},
		{text: `{"descriptions":[{"node_id":"pumps#1","description":"missing required summary","note":"metadata is allowed"}]}`},
		{text: `{"descriptions":null}`},
	} {
		c := &nodeSequenceClient{t: t, replies: []nodeReply{bad, {text: valid}, {text: nodeSupported}}}
		out := FuseNodeDescriptions(context.Background(), c, 0, 1, "operations", "pumps", []NodeDescriptionInput{nodeFixture()}).Nodes[0]
		if out.Outcome != annotation.DocumentComplete || len(out.Attempts) != 3 || out.Attempts[0].Step != "generate" || out.Attempts[1].Step != "generate" || strings.Contains(c.requests[1].System, "语义核验专家") {
			t.Fatalf("malformed generation flow %+v", out)
		}
	}
}

func TestNodeDescriptionsPermanentAndInvalidInputDoNotRetry(t *testing.T) {
	t.Parallel()
	c := &nodeSequenceClient{t: t, replies: []nodeReply{{err: fmt.Errorf("openai_compatible: status 401: authentication failed")}}}
	out := FuseNodeDescriptions(context.Background(), c, 0, 3, "d", "pumps", []NodeDescriptionInput{nodeFixture()}).Nodes[0]
	if len(c.requests) != 1 || out.Exhausted || out.Outcome != annotation.DocumentUnavailable || out.Attempts[0].Outcome != "permanent" {
		t.Fatalf("permanent failure retried: %+v", out)
	}
	var nilClient *nodeSequenceClient
	for _, tc := range []struct {
		client llm.Client
		tokens int
		input  NodeDescriptionInput
	}{
		{nilClient, 0, nodeFixture()},
		{c, -1, nodeFixture()},
		{c, 0, NodeDescriptionInput{ID: "node", Name: "Atlas", Material: " "}},
		{c, 0, NodeDescriptionInput{ID: " ", Name: "Atlas", Material: "fact"}},
	} {
		r := FuseNodeDescriptions(context.Background(), tc.client, tc.tokens, 0, "d", "s", []NodeDescriptionInput{tc.input}).Nodes[0]
		if r.Outcome != annotation.DocumentUnavailable || len(r.Attempts) != 0 || r.Exhausted {
			t.Fatalf("invalid input made attempts: %+v", r)
		}
	}
}

func TestNodeDescriptionsRetainSuccessSiblingAndCancellationDenominator(t *testing.T) {
	t.Parallel()
	one, two := nodeFixture(), nodeFixture()
	two.ID = "second"
	c := &nodeSequenceClient{t: t, replies: []nodeReply{
		{text: nodeWire("pumps#1", "Atlas pump", nodeQualifiedSource)}, {text: nodeSupported},
		{text: nodeWire("pumps#2", "Atlas pump", "Atlas.")},
		{text: `{"supported":true,"reason":"missing qualifications","unsupported_items":[],"omissions":["all qualified facts"]}`},
		{text: nodeWire("pumps#2", "Atlas pump", "Atlas.")},
		{text: `{"supported":true,"reason":"still missing qualifications","unsupported_items":[],"omissions":["all qualified facts"]}`},
	}}
	r := FuseNodeDescriptions(context.Background(), c, 0, 1, "d", "pumps", []NodeDescriptionInput{one, two})
	if len(r.Nodes) != 2 || r.Nodes[0].Outcome != annotation.DocumentComplete || len(r.Nodes[0].Attempts) != 2 || r.Nodes[1].Outcome != annotation.DocumentPartial || !r.Nodes[1].Exhausted || len(c.requests) != 6 {
		t.Fatalf("sibling replayed or omitted: %+v", r)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cc := &nodeSequenceClient{t: t, replies: []nodeReply{{text: nodeWire("pumps#1", "Atlas", nodeQualifiedSource), run: cancel}}}
	r = FuseNodeDescriptions(ctx, cc, 0, 3, "d", "pumps", []NodeDescriptionInput{one, two})
	if len(r.Nodes) != 2 || r.Nodes[0].Outcome != annotation.DocumentCancelled || r.Nodes[1].Outcome != annotation.DocumentCancelled || len(cc.requests) != 1 || len(r.Nodes[1].Attempts) != 0 {
		t.Fatalf("cancelled scope lost: %+v", r)
	}
}

// 来源 Content 的阈值不能被 Detail 的禁令遮住；空 marker shell 不算素材。
func TestDescriptionWrapperUsesAllContentDetailAndReportsMissingMembers(t *testing.T) {
	t.Parallel()
	docs := []*dktypes.AnnotatedDocument{{Skill: "ops", FilePath: "pump.md", Items: []dktypes.AnnotatedItem{{ID: "m", Name: "Atlas pump", Content: "Cooling starts only above 30 degrees Celsius.", Detail: "Cooling is prohibited during maintenance."}}}}
	r := &Result{Domains: []Domain{{Name: "d", Subdomains: []Subdomain{{Name: "pumps", Entities: []Node{{ID: "pump", Name: "Atlas pump", Summary: "bad old summary", Members: []string{"m"}}}}}}}}
	detail := "Cooling starts only above 30 degrees Celsius. Cooling is prohibited during maintenance."
	c := &nodeSequenceClient{t: t, replies: []nodeReply{{text: nodeWire("pumps#1", "Qualified pump cooling", detail)}, {text: nodeSupported}}}
	rpt := NewExtractor(c, 4096).FuseDescriptions(context.Background(), r, docs)
	n := r.Domains[0].Subdomains[0].Entities[0]
	if rpt.SubdomainsOK != 1 || rpt.NodesRewritten != 1 || len(rpt.Outcomes) != 1 || n.Summary != "Qualified pump cooling" || n.Description != detail {
		t.Fatalf("wrapper did not apply verified pair: %+v %+v", rpt, n)
	}
	for _, req := range c.requests {
		if !strings.Contains(req.User, docs[0].Items[0].Content) || !strings.Contains(req.User, docs[0].Items[0].Detail) || !strings.Contains(req.User, "pump.md") {
			t.Fatal("full bound source material not passed")
		}
	}
	for _, members := range [][]string{{"m", "missing"}, {"blank"}} {
		r.Domains[0].Subdomains[0].Entities = []Node{{ID: "pump", Name: "Atlas", Summary: "old", Description: "old detail", Members: members}}
		docs[0].Items = append(docs[0].Items, dktypes.AnnotatedItem{ID: "blank"})
		rpt = NewExtractor(c, 4096).FuseDescriptions(context.Background(), r, docs)
		if rpt.SubdomainsOK != 0 || rpt.NodesRewritten != 0 || rpt.NodesWithDetail != 0 || len(rpt.Outcomes) != 1 || r.Domains[0].Subdomains[0].Entities[0].Description != "old detail" || len(c.requests) != 2 {
			t.Fatalf("missing source accepted or old content erased: %+v", rpt)
		}
	}
	for _, member := range []string{" ", "m"} {
		r.Domains[0].Subdomains[0].Entities = []Node{{ID: "pump", Name: "Atlas", Summary: "old", Description: "old detail", Members: []string{member}}}
		docs[0].FilePath = ""
		rpt = NewExtractor(c, 4096).FuseDescriptions(context.Background(), r, docs)
		if rpt.SubdomainsOK != 0 || rpt.NodesRewritten != 0 || len(c.requests) != 2 {
			t.Fatalf("unknown source identity certified: %+v", rpt)
		}
	}
}
