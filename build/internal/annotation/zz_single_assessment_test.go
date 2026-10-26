package annotation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
)

type assessmentClient struct {
	calls   int
	reply   string
	err     error
	request llm.CompleteRequest
}

func (c *assessmentClient) ProviderName() string { return "assessment-fixture" }
func (c *assessmentClient) Complete(ctx context.Context, r llm.CompleteRequest) (*llm.CompleteResponse, error) {
	c.calls++
	c.request = r
	if c.err != nil {
		return nil, c.err
	}
	return &llm.CompleteResponse{Text: c.reply, FinishReason: "stop"}, nil
}

// 旧的名称去重和长度限制会删除同名但不同时间/类别的事实。
// Large same-name facts and source UUID literals must survive normalization.
func TestAnnotationPreservesLargeSameNameIncidencesAndUUIDSource(t *testing.T) {
	t.Parallel()
	name := strings.Repeat("long-name", 30)
	body := strings.Repeat("source detail ", 22000)
	wire, _ := json.Marshal(map[string]any{"items": []any{
		map[string]any{"tag": "entity", "name": name, "content": "date 2020", "detail": body, "confidence": .69},
		map[string]any{"tag": "entity", "name": name, "content": "date 2024", "detail": "", "confidence": .1},
		map[string]any{"tag": "concept", "name": name, "content": "another fact UUID 12345678-1234-1234-1234-123456789abc", "detail": "", "confidence": .9},
	}})
	c := &assessmentClient{reply: string(wire)}
	source := "Public ID 12345678-1234-1234-1234-123456789abc\n" + body
	doc, err := NewLLMAnnotator(c, 0, -1).Annotate(context.Background(), "s", "ref.md", source)
	if err != nil {
		t.Fatal(err)
	}
	cleaned, err := NewFormatGate(1).Check(doc)
	if err != nil {
		t.Fatal(err)
	}
	if len(cleaned.Items) != 3 || cleaned.Items[0].Detail != strings.TrimSpace(body) || cleaned.Items[1].Content != "date 2024" || c.calls != 1 || !strings.Contains(c.request.User, source) || !strings.Contains(cleaned.Items[2].Content, "12345678-1234-1234-1234-123456789abc") {
		t.Fatalf("lost valid source material: items=%d calls=%d", len(cleaned.Items), c.calls)
	}
}

// 旧门删除无依据的行后返回成功，且遗漏只告警；现在返回具体修订反馈且不改候选。
func TestAssessmentReturnsFeedbackWithoutDeletingItems(t *testing.T) {
	t.Parallel()
	c := &assessmentClient{reply: `{"supported":false,"reason":"item1 invented the date","unsupported_items":[1],"omissions":["unmapped fact"]}`}
	doc := &dktypes.AnnotatedDocument{Items: []dktypes.AnnotatedItem{{Name: "supported"}, {Name: "invented"}}}
	err := NewSemanticGate(c, 0, 3).Check(context.Background(), "exact original", doc)
	var feedback *SemanticFeedback
	if !errors.As(err, &feedback) || len(doc.Items) != 2 || c.calls != 1 || len(feedback.Omissions) != 1 {
		t.Fatalf("wrong feedback: %v doc%+v calls%d", err, doc, c.calls)
	}
}

func TestAssessmentUnknownNeverPassesAndRespectsRecoveryAllowance(t *testing.T) {
	t.Parallel()
	for _, reply := range []string{`{}`, `{"supported":null,"reason":"unknown"}`, `{"supported":false,"reason":"bad alias","unsupported_items":[99],"omissions":[]}`, `{"supported":true,"reason":"conflicting","unsupported_items":[0],"omissions":[]}`, `{broken`} {
		c := &assessmentClient{reply: reply}
		doc := &dktypes.AnnotatedDocument{Items: []dktypes.AnnotatedItem{{Name: "fact"}}}
		if err := NewSemanticGate(c, 0, 1).Check(context.Background(), "original", doc); err == nil {
			t.Fatalf("unknown passed: %s", reply)
		}
		if c.calls != 2 || len(doc.Items) != 1 {
			t.Fatalf("wrong recovery calls%d", c.calls)
		}
	}
	c := &assessmentClient{err: errors.New("transport unavailable")}
	if _, err := NewLLMAnnotator(c, 0, -1).Annotate(context.Background(), "s", "p", "original"); err == nil || c.calls != 1 {
		t.Fatalf("disabled retry passed: calls%d err%v", c.calls, err)
	}
}

func TestAnnotationEmptyLegitimateAndCallerCancellation(t *testing.T) {
	t.Parallel()
	c := &assessmentClient{reply: `{"items":[]}`}
	doc, err := NewLLMAnnotator(c, 0, -1).Annotate(context.Background(), "s", "p", "narrative")
	if err != nil || len(doc.Items) != 0 {
		t.Fatal(err)
	}
	if err := NewSemanticGate(c, 0, -1).Check(context.Background(), "narrative", doc); err == nil || c.calls != 2 {
		t.Fatal("nonempty source empty candidate passed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewLLMAnnotator(c, 0, -1).Annotate(ctx, "s", "p", "source"); !errors.Is(err, context.Canceled) || c.calls != 2 {
		t.Fatalf("caller cancellation lost: %v", err)
	}
	for _, bad := range []string{`{}`, `{"items":null}`, `{"items":{}}`} {
		if _, err := parseAnnotateResponse("s", "p", bad); err == nil {
			t.Fatalf("unknown output passed as empty: %s", bad)
		}
	}
}
