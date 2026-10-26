package annotation

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"strings"
	"testing"
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

// Old cosmetic caps and confidence-winner dedup lost valid source facts.
func TestAnnotationPreservesLargeSameNameIncidencesAndUUIDSource(t *testing.T) {
	name := strings.Repeat("long-name", 30)
	body := strings.Repeat("source detail ", 22000)
	wire, _ := json.Marshal(map[string]any{"items": []any{
		map[string]any{"tag": "entity", "name": name, "content": "date 2020", "detail": body, "confidence": .69},
		map[string]any{"tag": "entity", "name": name, "content": "date 2024", "detail": "", "confidence": .1},
		map[string]any{"tag": "concept", "name": name, "content": "another fact", "detail": "", "confidence": .9},
		map[string]any{"tag": "entity", "name": "bad", "content": "invalid numeric type", "confidence": "unknown"},
	}})
	c := &assessmentClient{reply: string(wire)}
	source := "Public ID 12345678-1234-1234-1234-123456789abc\n" + body
	doc, err := NewLLMAnnotator(c, 0, 99).Annotate(context.Background(), "s", "ref.md", source)
	if err != nil {
		t.Fatal(err)
	}
	cleaned, err := NewFormatGate(1).Check(doc)
	if err != nil {
		t.Fatal(err)
	}
	if len(cleaned.Items) != 3 || cleaned.Items[0].Detail != strings.TrimSpace(body) || cleaned.Items[1].Content != "date 2024" || c.calls != 1 || !strings.Contains(c.request.User, source) {
		t.Fatalf("lost valid source material: items=%d calls=%d", len(cleaned.Items), c.calls)
	}
}
func TestAssessmentOmitsUnsupportedItemOnce(t *testing.T) {
	c := &assessmentClient{reply: `{"supported":false,"reason":"item1 invented the date","unsupported_items":[1],"omissions":["unmapped fact"]}`}
	doc := &dktypes.AnnotatedDocument{Items: []dktypes.AnnotatedItem{{Name: "supported"}, {Name: "invented"}}}
	if err := NewSemanticGate(c, 0, 99).Check(context.Background(), "exact original", doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Items) != 1 || doc.Items[0].Name != "supported" || c.calls != 1 {
		t.Fatalf("wrong assessment: %+v calls%d", doc, c.calls)
	}
}
func TestAssessmentUnknownAndUnisolatableFalseNeverPassOrRetry(t *testing.T) {
	for _, reply := range []string{`{}`, `{"supported":null,"reason":"unknown"}`, `{"supported":false,"reason":"contamination cannot be isolated"}`, `{"supported":false,"reason":"bad alias","unsupported_items":[99]}`, `{"supported":true,"reason":"conflicting","unsupported_items":[0]}`, `{broken`} {
		c := &assessmentClient{reply: reply}
		doc := &dktypes.AnnotatedDocument{Items: []dktypes.AnnotatedItem{{Name: "fact"}}}
		if err := NewSemanticGate(c, 0, 99).Check(context.Background(), "original", doc); err == nil {
			t.Fatalf("unknown passed: %s", reply)
		}
		if c.calls != 1 || len(doc.Items) != 1 {
			t.Fatal("retried or mutated unknown assessment")
		}
	}
	c := &assessmentClient{err: errors.New("transport unavailable")}
	if _, err := NewLLMAnnotator(c, 0, 99).Annotate(context.Background(), "s", "p", "original"); err == nil || c.calls != 1 {
		t.Fatalf("failed annotation retried/passed: calls%d err%v", c.calls, err)
	}
}
func TestAnnotationEmptyLegitimateAndCallerCancellation(t *testing.T) {
	c := &assessmentClient{reply: `{"items":[]}`}
	doc, err := NewLLMAnnotator(c, 0, 99).Annotate(context.Background(), "s", "p", "narrative")
	if err != nil || len(doc.Items) != 0 {
		t.Fatalf("empty valid document rejected: %v", err)
	}
	if err := NewSemanticGate(c, 0, 99).Check(context.Background(), "narrative", doc); err != nil || c.calls != 1 {
		t.Fatal("empty annotation invoked assessment")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewLLMAnnotator(c, 0, 99).Annotate(ctx, "s", "p", "source"); !errors.Is(err, context.Canceled) || c.calls != 1 {
		t.Fatalf("caller cancellation lost: %v", err)
	}
	for _, bad := range []string{`{}`, `{"items":null}`, `{"items":{}}`} {
		if _, err := parseAnnotateResponse("s", "p", bad); err == nil {
			t.Fatalf("unknown output passed as empty: %s", bad)
		}
	}
}
