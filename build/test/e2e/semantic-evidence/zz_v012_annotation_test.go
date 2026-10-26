package semanticevidence

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
)

type v012SourceOnlyClient struct {
	t        *testing.T
	source   sourceFixture
	rows     []dktypes.AnnotatedItem
	requests []llm.CompleteRequest
}

func (c *v012SourceOnlyClient) ProviderName() string { return "source-only-annotation-fixture" }
func (c *v012SourceOnlyClient) Complete(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.requests = append(c.requests, req)
	if !strings.Contains(req.User, c.source.Content) {
		c.t.Fatal("authoritative source absent from annotation/assessment input")
	}
	for _, forbidden := range []string{"\"cases\"", "\"expected\"", "\"source_id\"", "\"owner\"", "original_bytes", "expected_inventory_ref"} {
		if strings.Contains(req.User, forbidden) {
			c.t.Fatalf("acceptance material entered model prompt: %s", forbidden)
		}
	}
	if len(c.requests) == 1 {
		b, _ := json.Marshal(map[string]any{"items": c.rows})
		return &llm.CompleteResponse{Text: string(b), FinishReason: "stop"}, nil
	}
	if len(c.requests) != 2 {
		c.t.Fatal("accepted sibling was unexpectedly rerun")
	}
	return &llm.CompleteResponse{Text: `{"supported":true,"reason":"fixture retains complete original","unsupported_items":[],"omissions":[]}`, FinishReason: "stop"}, nil
}

// 使用原来的八份补充原文检验 helper 的无损传递；期望和清单绝不作为原文提示。
// Exercise unchanged supplement originals through the helper, keeping expectations outside prompts.
// This deterministic client tests helper preservation, not a model's ability to judge semantics.
func TestV012AnnotationSupplementSourceBoundaryAndPreservation(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile("testdata/v011/supplement.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture supplement
	if err := json.Unmarshal(b, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Sources) != 8 || len(fixture.Cases) != 19 {
		t.Fatal("original finite denominator changed")
	}
	for _, source := range fixture.Sources {
		t.Run(source.SourceID, func(t *testing.T) {
			texts := []string{strings.TrimSpace(source.Content)}
			if source.SourceID == "F02" {
				// 原先按名称取胜者可能删除同名的不同事实；分开保留两组原文。
				// Name-based winner selection must not collapse these different source facts.
				parts := strings.SplitN(source.Content, "In this document", 2)
				if len(parts) != 2 {
					t.Fatal("immutable F02 source structure changed")
				}
				texts = []string{strings.TrimSpace(parts[0]), "In this document " + strings.TrimSpace(parts[1])}
			}
			if strings.Join(texts, " ") != strings.TrimSpace(source.Content) {
				t.Fatal("candidate fragments do not reconstruct the immutable original")
			}
			rows := make([]dktypes.AnnotatedItem, len(texts))
			for i, content := range texts {
				rows[i] = dktypes.AnnotatedItem{Tag: dktypes.TagConcept, Name: "Atlas", Content: content, Detail: content, Confidence: .9}
			}
			client := &v012SourceOnlyClient{t: t, source: source, rows: rows}
			result := annotation.NewDocumentProcessor(client, 0, -1, 0).Process(context.Background(), "fixtures", source.Path, source.Content)
			if result.Outcome != annotation.DocumentComplete || result.Document == nil || result.FilePath != source.Path || result.Document.FilePath != source.Path || result.CandidateID == "" || len(result.Document.Items) != len(rows) || len(client.requests) != 2 {
				t.Fatalf("lost source/candidate identity or facts: %+v", result)
			}
			for i, item := range result.Document.Items {
				if item.Content != rows[i].Content || item.Detail != rows[i].Detail {
					t.Fatalf("source distinction %d changed", i)
				}
			}
			if source.SourceID == "F01" && !strings.Contains(result.Document.Items[0].Detail, "123e4567-e89b-12d3-a456-426614174000") {
				t.Fatal("source UUID literal lost")
			}
		})
	}
}
