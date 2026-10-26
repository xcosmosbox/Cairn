package annotation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
)

type processorReply struct {
	text, finish string
	err          error
	run          func()
}
type processorClient struct {
	t        *testing.T
	replies  []processorReply
	requests []llm.CompleteRequest
}

func (c *processorClient) ProviderName() string { return "v012-core-fixture" }
func (c *processorClient) Complete(ctx context.Context, r llm.CompleteRequest) (*llm.CompleteResponse, error) {
	c.requests = append(c.requests, r)
	i := len(c.requests) - 1
	if i >= len(c.replies) {
		c.t.Fatalf("unexpected model call %d: %s", i+1, r.System)
		return nil, fmt.Errorf("fixture exhausted")
	}
	reply := c.replies[i]
	if reply.run != nil {
		reply.run()
	}
	if reply.err != nil {
		return nil, reply.err
	}
	finish := reply.finish
	if finish == "" {
		finish = "stop"
	}
	return &llm.CompleteResponse{Text: reply.text, FinishReason: finish}, nil
}

const supportedComplete = `{"supported":true,"reason":"every substantive source fact is retained faithfully","unsupported_items":[],"omissions":[]}`

func annotationRows(contents ...string) string {
	rows := []map[string]any{}
	for _, content := range contents {
		rows = append(rows, map[string]any{"tag": "concept", "name": "same name", "content": content, "detail": "", "confidence": .9})
	}
	b, _ := json.Marshal(map[string]any{"items": rows})
	return string(b)
}

// v0.1.2 将 malformed assessment 与事实错误分开：只重试同一候选，不能重标兄弟或整篇。
func TestDocumentProcessorInvalidVerdictReassessesExactCandidate(t *testing.T) {
	t.Parallel()
	bad := []string{
		`{}`, `null`, `{"supported":null,"reason":"unknown","unsupported_items":[],"omissions":[]}`,
		`{"supported":true,"reason":"no coverage supplied","unsupported_items":[]}`,
		`{"supported":true,"reason":"null coverage","unsupported_items":[],"omissions":null}`,
		`{"supported":true,"reason":"null index list","unsupported_items":null,"omissions":[]}`,
		`{"supported":false,"idempotent":true,"reason":"conflict","unsupported_items":[],"omissions":[]}`,
		`{"supported":true,"idempotent":null,"reason":"unknown legacy","unsupported_items":[],"omissions":[]}`,
		`{"supported":true,"reason":"contradiction","unsupported_items":[0],"omissions":[]}`,
		`{"supported":false,"reason":"out of range","unsupported_items":[1],"omissions":[]}`,
		`{"supported":false,"reason":"negative","unsupported_items":[-1],"omissions":[]}`,
		`{"supported":false,"reason":"duplicate index","unsupported_items":[0,0],"omissions":[]}`,
		`{"supported":false,"reason":"null index","unsupported_items":[null],"omissions":[]}`,
		`{"supported":true,"reason":"null omission","unsupported_items":[],"omissions":[null]}`,
		`{"supported":true,"reason":"empty omission","unsupported_items":[],"omissions":[""]}`,
		`{"supported":false,"supported":true,"reason":"last-key hides rejection","unsupported_items":[],"omissions":[]}`,
		`{"supported":true,"reason":"last-key hides omission","unsupported_items":[],"omissions":["missing"],"omissions":[]}`,
		`{"supported":true,"reason":"last-key hides unknown","unsupported_items":null,"unsupported_items":[],"omissions":[]}`,
	}
	for i, reply := range bad {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			c := &processorClient{t: t, replies: []processorReply{{text: annotationRows("source fact")}, {text: reply}, {text: supportedComplete}}}
			r := NewDocumentProcessor(c, 0, 1, 1).Process(context.Background(), "skill", "ref.md", "source fact")
			if r.Outcome != DocumentComplete || r.Exhausted || r.Reason != "" || len(r.Attempts) != 3 || len(c.requests) != 3 {
				t.Fatalf("result%+v calls%d", r, len(c.requests))
			}
			if !reflect.DeepEqual(c.requests[1], c.requests[2]) || r.Attempts[1].CandidateID != r.Attempts[2].CandidateID || r.Attempts[1].CandidateID != r.CandidateID || r.Attempts[1].Step != "assess" || r.Attempts[1].Outcome != "invalid" {
				t.Fatalf("candidate/step changed: %+v", r.Attempts)
			}
		})
	}
}

// 修订不是补丁：补回 B 时丢失 A 的候选必须再次失败；第2次修订重查 A+B。
func TestDocumentProcessorRevisionRechecksEntireCandidate(t *testing.T) {
	t.Parallel()
	original := "A before 2020; B only when ready. UUID 12345678-1234-1234-1234-123456789abc remains literal."
	c := &processorClient{t: t, replies: []processorReply{
		{text: annotationRows("A before 2020")},
		{text: `{"supported":true,"reason":"B is absent","unsupported_items":[],"omissions":["B only when ready; retain original UUID"]}`},
		{text: annotationRows("B only when ready")},
		{text: `{"supported":true,"reason":"revision erased A and UUID","unsupported_items":[],"omissions":["A before 2020 and original UUID"]}`},
		{text: annotationRows("A before 2020", "B only when ready. UUID 12345678-1234-1234-1234-123456789abc remains literal.")},
		{text: supportedComplete},
	}}
	r := NewDocumentProcessor(c, 0, 2, 1).Process(context.Background(), "s", "revision.md", original)
	if r.Outcome != DocumentComplete || len(r.Document.Items) != 2 || len(r.Attempts) != 6 {
		t.Fatalf("result %+v", r)
	}
	expected := []string{"annotate", "assess", "revise", "assess", "revise", "assess"}
	for i, a := range r.Attempts {
		if a.Step != expected[i] || a.Number != i+1 {
			t.Fatalf("attempt %+v", a)
		}
	}
	for _, i := range []int{2, 4} {
		if !strings.Contains(c.requests[i].System, "标注修订") || !strings.Contains(c.requests[i].User, original) || !strings.Contains(c.requests[i].User, "complete candidate") || !strings.Contains(c.requests[i].User, "feedback") {
			t.Fatalf("missing original/candidate/feedback: %s", c.requests[i].User)
		}
	}
	if !strings.Contains(c.requests[3].User, "B only when ready") || strings.Contains(strings.Split(c.requests[3].User, "annotated items\n")[1], "A before 2020") {
		t.Fatal("revision was not rechecked as full candidate")
	}
	if r.Attempts[0].CandidateID == r.Attempts[2].CandidateID || r.Attempts[2].CandidateID == r.Attempts[4].CandidateID || r.CandidateID != r.Attempts[5].CandidateID {
		t.Fatal("candidate revision identities lost")
	}
}

func TestDocumentProcessorUnsupportedClaimRevisedInsteadOfFiltered(t *testing.T) {
	t.Parallel()
	c := &processorClient{t: t, replies: []processorReply{
		{text: annotationRows("real fact", "invented date")},
		{text: `{"supported":false,"reason":"item1 invents the date; source also requires negation","unsupported_items":[1],"omissions":["does not allow refunds"]}`},
		{text: annotationRows("real fact; does not allow refunds")}, {text: supportedComplete},
	}}
	r := NewDocumentProcessor(c, 0, 1, 1).Process(context.Background(), "s", "p", "real fact; does not allow refunds")
	if r.Outcome != DocumentComplete || len(c.requests) != 4 || len(r.Document.Items) != 1 || !strings.Contains(r.Document.Items[0].Content, "does not allow refunds") {
		t.Fatalf("unsupported claim escaped: %+v", r)
	}
	if !strings.Contains(c.requests[2].User, "invented date") || !strings.Contains(c.requests[2].User, "item1 invents") {
		t.Fatal("revision missing rejected complete candidate or critique")
	}
}

func TestDocumentProcessorSharedFiniteAllowanceAndExhaustion(t *testing.T) {
	t.Parallel()
	// Invalid assessment spends the single recovery. A later semantic rejection cannot
	// obtain a fresh revision budget from a nested annotator/gate loop.
	c := &processorClient{t: t, replies: []processorReply{{text: annotationRows("fact")}, {text: `{broken`}, {text: `{"supported":true,"reason":"missing second fact","unsupported_items":[],"omissions":["second fact"]}`}}}
	r := NewDocumentProcessor(c, 0, 1, 0).Process(context.Background(), "s", "p", "fact plus second fact")
	if r.Outcome != DocumentPartial || !r.Exhausted || len(c.requests) != 3 || r.Document == nil || r.Reason == "" {
		t.Fatalf("nested budget or missing exhaustion: %+v", r)
	}
	for _, n := range []int{-1, 0, 2} {
		allowance := n
		if n < 0 {
			allowance = 0
		}
		if n == 0 {
			allowance = 3
		}
		replies := []processorReply{{text: annotationRows("fact")}}
		for i := 0; i <= allowance; i++ {
			replies = append(replies, processorReply{text: `{broken`})
		}
		c := &processorClient{t: t, replies: replies}
		r := NewDocumentProcessor(c, 0, n, 0).Process(context.Background(), "s", "p", "fact")
		if r.Outcome != DocumentUnavailable || !r.Exhausted || len(c.requests) != 2+allowance || len(r.Attempts) != 2+allowance {
			t.Fatalf("n%d result%+v calls%d", n, r, len(c.requests))
		}
	}
}

func TestDocumentProcessorMalformedRowsAreNotFiltered(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{
		`{"items":[{"tag":"concept","name":"fact","content":"valid fact","confidence":0.9},{"tag":"invalid","name":"second","content":"source fact","confidence":0.9}]}`,
		`{"items":[{"tag":"concept","name":"fact","content":"valid fact","confidence":0.9},{"tag":"concept","name":"second","content":"source fact","confidence":null}]}`,
		`{"items":[{"tag":"concept","name":"fact","content":"valid fact","confidence":0.9},{"tag":"concept","name":"second","content":"source fact","confidence":2}]}`,
		`{"items":[{"tag":"concept","name":"fact","content":"valid fact","confidence":0.9,"content":"overwritten"}]}`,
		`{"items":[],"items":[{"tag":"concept","name":"fact","content":"valid fact","confidence":0.9}]}`,
	} {
		c := &processorClient{t: t, replies: []processorReply{{text: bad}, {text: annotationRows("valid fact", "source fact")}, {text: supportedComplete}}}
		r := NewDocumentProcessor(c, 0, 1, 1).Process(context.Background(), "s", "p", "valid fact; source fact")
		if r.Outcome != DocumentComplete || len(r.Document.Items) != 2 || len(c.requests) != 3 || r.Attempts[0].Outcome != "invalid" || r.Attempts[1].Step != "annotate" {
			t.Fatalf("filtered malformed row: %+v", r)
		}
	}
	doc := &dktypes.AnnotatedDocument{FilePath: "p", Items: []dktypes.AnnotatedItem{{Tag: dktypes.TagConcept, Name: "fact", Content: "valid", Confidence: .9}, {Tag: dktypes.AnnotationTag("invalid"), Name: "other", Content: "knowledge", Confidence: .9}}}
	before, _ := json.Marshal(doc)
	if out, err := NewFormatGate(1).Check(doc); err == nil || out != nil {
		t.Fatal("format gate silently filtered invalid row")
	}
	after, _ := json.Marshal(doc)
	if string(before) != string(after) {
		t.Fatal("format gate mutated original")
	}
}

func TestDocumentProcessorEmptySourceAndCandidateRequireTruthfulCoverage(t *testing.T) {
	t.Parallel()
	c := &processorClient{t: t}
	r := NewDocumentProcessor(c, 0, -1, 1).Process(context.Background(), "s", "empty.md", "")
	if r.Outcome != DocumentComplete || len(r.Attempts) != 0 || len(c.requests) != 0 || r.Document == nil || r.CandidateID == "" {
		t.Fatalf("legitimate empty source %+v", r)
	}
	// Nonempty boilerplate can be explicitly found non-substantive; it cannot bypass
	// assessment merely because annotation returned no rows.
	c = &processorClient{t: t, replies: []processorReply{{text: annotationRows()}, {text: supportedComplete}}}
	r = NewDocumentProcessor(c, 0, -1, 1).Process(context.Background(), "s", "boilerplate.md", "Contents")
	if r.Outcome != DocumentComplete || len(c.requests) != 2 || !strings.HasSuffix(c.requests[1].User, "[]") {
		t.Fatalf("empty output bypassed assessment: %+v", r)
	}
	c = &processorClient{t: t, replies: []processorReply{{text: annotationRows()}, {text: `{"supported":true,"reason":"substantive source absent","unsupported_items":[],"omissions":["all source knowledge"]}`}, {text: annotationRows("all source knowledge")}, {text: supportedComplete}}}
	r = NewDocumentProcessor(c, 0, 1, 1).Process(context.Background(), "s", "substantive.md", "all source knowledge")
	if r.Outcome != DocumentComplete || len(c.requests) != 4 || len(r.Document.Items) != 1 {
		t.Fatalf("empty candidate not repaired %+v", r)
	}
}

func TestDocumentProcessorTruncationAndProviderTimeoutRetryOnlyFailedStep(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		first processorReply
	}{
		{"truncated", processorReply{text: annotationRows("fact"), finish: "length"}},
		{"provider timeout", processorReply{err: fmt.Errorf("provider timeout: %w", context.DeadlineExceeded)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &processorClient{t: t, replies: []processorReply{tc.first, {text: annotationRows("fact")}, {text: supportedComplete}}}
			r := NewDocumentProcessor(c, 0, 1, 0).Process(context.Background(), "s", "p", "fact")
			if r.Outcome != DocumentComplete || len(c.requests) != 3 || r.Attempts[1].Step != "annotate" {
				t.Fatalf("generation recovery %+v", r)
			}
		})
	}
	c := &processorClient{t: t, replies: []processorReply{{text: annotationRows("fact")}, {err: fmt.Errorf("provider timeout: %w", context.DeadlineExceeded)}, {text: supportedComplete}}}
	r := NewDocumentProcessor(c, 0, 1, 0).Process(context.Background(), "s", "p", "fact")
	if r.Outcome != DocumentComplete || !reflect.DeepEqual(c.requests[1], c.requests[2]) || r.Attempts[1].Outcome != "unavailable" {
		t.Fatalf("timeout did not reassess unchanged candidate %+v", r)
	}
}

// 503/429 错误正文可能提及密钥或参数；正文不是永久配置错误的证据。
func TestDocumentProcessorServiceErrorBodyCannotOverrideStatus(t *testing.T) {
	t.Parallel()
	for _, message := range []string{
		"openai_compatible: status 503: API key service temporarily unavailable",
		"openai_compatible: status 503: max_tokens subsystem unavailable",
		"anthropic: status 500: upstream status 401 while rotating API key",
		"openai_compatible: status 429: missing API key cache",
	} {
		c := &processorClient{t: t, replies: []processorReply{{text: annotationRows("fact")}, {err: errors.New(message)}, {text: supportedComplete}}}
		r := NewDocumentProcessor(c, 0, 1, 1).Process(context.Background(), "s", "p", "fact")
		if r.Outcome != DocumentComplete || len(c.requests) != 3 || !reflect.DeepEqual(c.requests[1], c.requests[2]) || r.Attempts[1].Outcome != "unavailable" {
			t.Fatalf("service status overridden: %+v", r)
		}
	}
}

func TestDocumentProcessorCancellationAndPermanentFailuresStop(t *testing.T) {
	t.Parallel()
	for _, message := range []string{"openai_compatible: status 401: unauthorized", "openai_compatible: status 400: bad model", "openai_compatible: max_tokens 999999 exceeds DeepSeek maximum 65536", "openai_compatible: build request: invalid URL", "missing API key"} {
		c := &processorClient{t: t, replies: []processorReply{{err: errors.New(message)}}}
		r := NewDocumentProcessor(c, 0, 5, 1).Process(context.Background(), "s", "p", "fact")
		if r.Outcome != DocumentUnavailable || r.Exhausted || len(c.requests) != 1 || r.Attempts[0].Outcome != "permanent" {
			t.Fatalf("permanent error retried %+v", r)
		}
	}
	for _, source := range []string{"bad\x00source", string([]byte{0xff})} {
		c := &processorClient{t: t}
		r := NewDocumentProcessor(c, 0, 5, 1).Process(context.Background(), "s", "p", source)
		if r.Outcome != DocumentUnavailable || r.Exhausted || len(c.requests) != 0 {
			t.Fatalf("permanent source retried %+v", r)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := &processorClient{t: t}
	r := NewDocumentProcessor(c, 0, 3, 1).Process(ctx, "s", "p", "fact")
	if r.Outcome != DocumentCancelled || r.Exhausted || len(c.requests) != 0 {
		t.Fatalf("pre-cancelled %+v", r)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	c = &processorClient{t: t, replies: []processorReply{{text: annotationRows("fact")}, {text: supportedComplete, run: cancel}}}
	r = NewDocumentProcessor(c, 0, 3, 1).Process(ctx, "s", "p", "fact")
	if r.Outcome != DocumentCancelled || len(c.requests) != 2 || r.Exhausted || r.Document == nil || r.CandidateID == "" || r.Attempts[1].Outcome != "cancelled" {
		t.Fatalf("cancellation lost candidate/started dependent revision %+v", r)
	}
}

func TestStandaloneGateAndAnnotatorDefaultRecoverySemantics(t *testing.T) {
	t.Parallel()
	for _, n := range []int{-1, 0, 2} {
		count := n + 1
		if n < 0 {
			count = 1
		}
		if n == 0 {
			count = 4
		}
		c := &assessmentClient{reply: `{broken`}
		_, err := NewLLMAnnotator(c, 0, n).Annotate(context.Background(), "s", "p", "fact")
		if err == nil || c.calls != count {
			t.Fatalf("annotator n%d calls%d err%v", n, c.calls, err)
		}
		c = &assessmentClient{reply: `{broken`}
		doc := &dktypes.AnnotatedDocument{Items: []dktypes.AnnotatedItem{{Name: "fact"}}}
		before := append([]dktypes.AnnotatedItem(nil), doc.Items...)
		err = NewSemanticGate(c, 0, n).Check(context.Background(), "fact", doc)
		if err == nil || c.calls != count || !reflect.DeepEqual(before, doc.Items) {
			t.Fatalf("gate n%d calls%d err%v", n, c.calls, err)
		}
	}
}

// verifier 已明确无法核验与 JSON 损坏不同，保留真实原因并只重试同一核验。
func TestDocumentProcessorDeclaredUnknownAssessment(t *testing.T) {
	t.Parallel()
	unknown := `{"status":"unknown","reason":"source table is ambiguous and cannot be assessed"}`
	t.Run("recovers same candidate", func(t *testing.T) {
		c := &processorClient{t: t, replies: []processorReply{{text: annotationRows("fact")}, {text: unknown}, {text: supportedComplete}}}
		r := NewDocumentProcessor(c, 0, 1, 1).Process(context.Background(), "s", "p", "fact")
		if r.Outcome != DocumentComplete || r.Exhausted || len(c.requests) != 3 || len(r.Attempts) != 3 {
			t.Fatalf("unknown did not recover: %+v", r)
		}
		if r.Attempts[1].Outcome != "unknown" || !strings.Contains(r.Attempts[1].Reason, "source table is ambiguous") || !reflect.DeepEqual(c.requests[1], c.requests[2]) || r.Attempts[1].CandidateID != r.Attempts[2].CandidateID {
			t.Fatalf("unknown reason/candidate lost: %+v", r)
		}
	})
	t.Run("exhausted reason retained", func(t *testing.T) {
		finalUnknown := `{"status":"unknown","reason":"the source remains impossible to disambiguate"}`
		c := &processorClient{t: t, replies: []processorReply{{text: annotationRows("fact")}, {text: unknown}, {text: finalUnknown}}}
		r := NewDocumentProcessor(c, 0, 1, 1).Process(context.Background(), "s", "p", "fact")
		if r.Outcome != DocumentUnavailable || !r.Exhausted || len(c.requests) != 3 || len(r.Attempts) != 3 || r.Attempts[2].Outcome != "unknown" || !strings.Contains(r.Reason, "the source remains impossible to disambiguate") || !strings.Contains(r.Attempts[1].Reason, "source table is ambiguous") || !reflect.DeepEqual(c.requests[1], c.requests[2]) {
			t.Fatalf("unknown exhausted as malformed/semantic rejection: %+v", r)
		}
	})
	for _, bad := range []string{
		`{"status":"unknown","reason":"cannot assess","supported":true}`,
		`{"status":"unknown","reason":"cannot assess","idempotent":null}`,
		`{"status":"unknown","reason":"cannot assess","unsupported_items":[]}`,
		`{"status":"unknown","reason":"cannot assess","omissions":[]}`,
		`{"status":"unknown","reason":"cannot assess","coverage_complete":true}`,
		`{"status":"unknown","reason":""}`,
		`{"status":"unknown","reason":null}`,
		`{"status":"unknown"}`,
		`{"status":null,"reason":"cannot assess"}`,
		`{"status":"known","reason":"cannot assess"}`,
		`{"supported":null,"reason":"cannot assess","unsupported_items":[],"omissions":[]}`,
	} {
		c := &processorClient{t: t, replies: []processorReply{{text: annotationRows("fact")}, {text: bad}, {text: supportedComplete}}}
		r := NewDocumentProcessor(c, 0, 1, 1).Process(context.Background(), "s", "p", "fact")
		if r.Outcome != DocumentComplete || len(c.requests) != 3 || r.Attempts[1].Outcome != "invalid" || !reflect.DeepEqual(c.requests[1], c.requests[2]) {
			t.Fatalf("invalid unknown declaration passed/revised: %s result%+v", bad, r)
		}
	}
}
