package annotation

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
)

type v012AcceptanceClient struct {
	t       *testing.T
	replies []string
	calls   []llm.CompleteRequest
}

func (c *v012AcceptanceClient) ProviderName() string { return "v012-acceptance-fixture" }
func (c *v012AcceptanceClient) Complete(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	n := len(c.calls)
	c.calls = append(c.calls, req)
	if n >= len(c.replies) {
		c.t.Fatalf("unexpected dependent model call %d", n+1)
	}
	return &llm.CompleteResponse{Text: c.replies[n], FinishReason: "stop"}, nil
}

func v012AcceptanceItems(contents ...string) string {
	items := make([]dktypes.AnnotatedItem, len(contents))
	for i, content := range contents {
		items[i] = dktypes.AnnotatedItem{Tag: dktypes.TagConcept, Name: "shared name", Content: content, Detail: content, Confidence: .9}
	}
	b, err := json.Marshal(map[string]any{"items": items})
	if err != nil {
		panic(err)
	}
	return string(b)
}

const v012AcceptanceSupported = `{"supported":true,"reason":"source faithful and complete","unsupported_items":[],"omissions":[]}`

// 历史 S02 把 29 个条目的下标 29 当成事实拒绝；无效核验只可重核同一候选。
// Historical S02 had 29 items but index 29; malformed feedback must reassess the identical candidate.
func TestV012AcceptanceHistorical29ItemAssessment(t *testing.T) {
	t.Parallel()
	facts := make([]string, 29)
	for i := range facts {
		facts[i] = fmt.Sprintf("Source rule %02d applies only before maintenance.", i)
	}
	client := &v012AcceptanceClient{t: t, replies: []string{
		v012AcceptanceItems(facts...),
		`{"supported":false,"reason":"bad index","unsupported_items":[29],"omissions":[]}`,
		v012AcceptanceSupported,
	}}
	result := NewDocumentProcessor(client, 0, 1, 0).Process(context.Background(), "history", "references/S02.md", strings.Join(facts, "\n"))
	if result.Outcome != DocumentComplete || result.Document == nil || len(result.Document.Items) != 29 {
		t.Fatalf("29-item candidate not recovered: %+v", result)
	}
	if len(client.calls) != 3 || client.calls[1].User != client.calls[2].User || client.calls[1].System != client.calls[2].System {
		t.Fatal("malformed assessment regenerated/changed candidate or retried wrong step")
	}
	if len(result.Attempts) != 3 || result.Attempts[1].Step != "assess" || result.Attempts[2].Step != "assess" || result.Attempts[1].CandidateID != result.Attempts[2].CandidateID {
		t.Fatalf("missing same-candidate assessment history: %+v", result.Attempts)
	}
	for i, item := range result.Document.Items {
		if item.Content != facts[i] {
			t.Fatalf("item %d changed during reassessment", i)
		}
	}
}

// 单次遗漏修订可能删掉之前正确的事实；必须重核整篇而非只核验新补的句子。
// Fixing one omission can erase a formerly correct fact; every revision requires a whole-candidate check.
func TestV012AcceptanceRevisionRechecksPreviouslyCoveredFact(t *testing.T) {
	t.Parallel()
	threshold := "Cooling starts only above 30 degrees Celsius."
	prohibition := "Cooling is prohibited during maintenance."
	original := threshold + " " + prohibition
	client := &v012AcceptanceClient{t: t, replies: []string{
		v012AcceptanceItems(threshold),
		`{"supported":true,"reason":"missing prohibition","unsupported_items":[],"omissions":["Cooling is prohibited during maintenance."]}`,
		v012AcceptanceItems(prohibition),
		`{"supported":true,"reason":"revision erased threshold","unsupported_items":[],"omissions":["Cooling starts only above 30 degrees Celsius."]}`,
		v012AcceptanceItems(threshold, prohibition),
		v012AcceptanceSupported,
	}}
	result := NewDocumentProcessor(client, 0, 2, 0).Process(context.Background(), "operations", "references/cooling.md", original)
	if result.Outcome != DocumentComplete || result.Document == nil || len(result.Document.Items) != 2 || len(client.calls) != 6 {
		t.Fatalf("full recheck did not recover both facts: %+v, calls=%d", result, len(client.calls))
	}
	if len(result.Attempts) != 6 || result.Attempts[2].Step != "revise" || result.Attempts[4].Step != "revise" {
		t.Fatalf("semantic revisions missing from actual attempts: %+v", result.Attempts)
	}
	for _, n := range []int{2, 4} {
		if !strings.Contains(client.calls[n].User, original) || !strings.Contains(client.calls[n].User, "shared name") {
			t.Fatalf("revision %d lost authoritative original or candidate", n)
		}
	}
	if !strings.Contains(client.calls[4].User, "revision erased threshold") || !strings.Contains(client.calls[5].User, threshold) || !strings.Contains(client.calls[5].User, prohibition) {
		t.Fatal("concrete critique or complete revised candidate omitted")
	}
}

// 历史三条删除均混有真内容；修复无依据断言时不能整行删除真实知识。
// Sanitized equivalents of the three historical removals require repairing false details without deleting true substance.
func TestV012AcceptanceMixedTruthRowsAreRevisedNotDeleted(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, original, bad, feedback string }{
		{"former_officer_not_founder", "Company A focuses on privacy and was founded to counter larger competitors. Person B is its former vice-president. The author's privacy tradeoff favors Company A for some users despite its worse test performance and readability. In the author's test it ignored the green query qualifier. Person B admits possible misinterpretation but maintains an allegation that the alternative denies.", "Person B founded Company A. Company A focuses on privacy and was founded to counter larger competitors. Person B is its former vice-president. The author's privacy tradeoff favors Company A for some users despite its worse test performance and readability. In the author's test it ignored the green query qualifier. Person B admits possible misinterpretation but maintains an allegation that the alternative denies.", "Original identifies a former vice-president, never a founder; retain privacy/counter-competitor mission, test performance/readability, query qualifier and unresolved allegation."},
		{"pilot_price_not_monthly", "Platform C tests an ad-free tier for some users at $4.99 with no other strings attached. It says the current pilot is in one English-speaking market outside Country D. No imminent broad rollout is claimed.", "Platform C tests an ad-free tier for some users at $4.99 per month with no other strings attached. It says the current pilot is in one English-speaking market outside Country D. No imminent broad rollout is claimed.", "Original gives $4.99 but no billing interval; retain no-other-strings offer, some-user pilot, attributed geographic limit and timing caveat."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verdict, _ := json.Marshal(map[string]any{"supported": false, "reason": tc.feedback, "unsupported_items": []int{0}, "omissions": []string{}})
			client := &v012AcceptanceClient{t: t, replies: []string{v012AcceptanceItems(tc.bad), string(verdict), v012AcceptanceItems(tc.original), v012AcceptanceSupported}}
			result := NewDocumentProcessor(client, 0, 1, 0).Process(context.Background(), "history", "references/"+tc.name+".md", tc.original)
			if result.Outcome != DocumentComplete || result.Document == nil || len(result.Document.Items) != 1 || result.Document.Items[0].Content != tc.original {
				t.Fatalf("supported meaning lost instead of revised: %+v", result)
			}
			if len(client.calls) != 4 || !strings.Contains(client.calls[2].User, tc.feedback) || !strings.Contains(client.calls[2].User, tc.original) || !strings.Contains(client.calls[2].User, tc.bad) || !strings.Contains(client.calls[3].User, tc.original) {
				t.Fatal("repair did not use original + candidate + critique and recheck the entire replacement")
			}
		})
	}
}
