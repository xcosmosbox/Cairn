package annotation

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
)

type quoteRefClient struct {
	requests []llm.CompleteRequest
	reply    func(llm.CompleteRequest, int) string
}

func (*quoteRefClient) ProviderName() string { return "offline-source-range-controls" }
func (c *quoteRefClient) Complete(_ context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
	c.requests = append(c.requests, req)
	return &llm.CompleteResponse{Text: c.reply(req, len(c.requests)), FinishReason: "stop"}, nil
}
func quoteJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
func requestQuoteIndex(t *testing.T, req llm.CompleteRequest) sourceQuoteIndex {
	t.Helper()
	at := strings.LastIndex(req.User, sourceQuoteIndexMarker)
	if at < 0 {
		t.Fatal("whole source index absent")
	}
	text := req.User[at+len(sourceQuoteIndexMarker):]
	text = strings.SplitN(text, "\n## 标注单元（数组下标从0开始） / annotated items\n", 2)[0]
	var index sourceQuoteIndex
	if err := json.Unmarshal([]byte(text), &index); err != nil {
		t.Fatal(err)
	}
	return index
}
func quotePositive(scope string, ref any) (map[string]any, map[string]any, map[string]any) {
	check := map[string]any{"item_index": 0, "item_referent": "Owner", "same_referent": true, "preserves_assertion": true, "reason": "controlled exact source/subject result"}
	row := map[string]any{"source_quote_ref": ref, "subject_referent": "Owner", "checks": []any{check}}
	response := map[string]any{"supported": true, "reason": "controlled complete source", "unsupported_items": []int{}, "omissions": []string{}, "source_quote_scope": scope, "subject_coverage": []any{row}}
	return response, row, check
}

func TestV016QuoteIndexCoversEveryOriginalByteAndOneContinuousRange(t *testing.T) {
	original := "前文 ‘quote’ 🙂\r\n\nINTERVENING DATA\nlast\r\n"
	doc := &dktypes.AnnotatedDocument{Skill: "canonical", FilePath: "references/original.md"}
	index := makeSourceQuoteIndex(original, doc)
	joined := ""
	for i, line := range index.Lines {
		if line.ID != i+1 {
			t.Fatal("unstable line identity")
		}
		joined += line.Text
	}
	if joined != original {
		t.Fatal("index filtered or normalized original bytes")
	}
	if !reflect.DeepEqual(index, makeSourceQuoteIndex(original, doc)) {
		t.Fatal("index is not deterministic")
	}
	fields := map[string]json.RawMessage{"source_quote_ref": json.RawMessage(`{"start":1,"end":3}`)}
	quote, err := subjectQuote(fields, json.RawMessage(quoteJSON(index.Scope)), index)
	if err != nil || quote != "前文 ‘quote’ 🙂\r\n\nINTERVENING DATA\n" {
		t.Fatalf("range was joined/expanded/normalized: %q %v", quote, err)
	}
	other := *doc
	other.FilePath = "references/other.md"
	if index.Scope == makeSourceQuoteIndex(original, &other).Scope || index.Scope == makeSourceQuoteIndex(original+" ", doc).Scope {
		t.Fatal("source identity/bytes not bound")
	}
}

func TestV016QuoteReferenceActualGatePositiveAndNegativeControls(t *testing.T) {
	original := "Owner runs only if pressure exceeds 2 bar.\n\nThe author says Owner may pause.\r\n"
	doc := &dktypes.AnnotatedDocument{Skill: "canonical", FilePath: "references/original.md", Items: []dktypes.AnnotatedItem{{Tag: dktypes.TagConcept, Name: "Owner", Content: original}}}
	for _, mode := range []string{"valid", "wrong-source", "wrong-bytes", "missing-scope", "null-scope", "unknown-id", "negative-id", "reversed", "fraction", "string-id", "null-end", "extra-ref-field", "disjoint", "dual", "dual-null", "null-ref", "blank-only", "literal", "literal-near", "wrong-carrier", "lost-assertion", "bad-item", "null-check"} {
		t.Run(mode, func(t *testing.T) {
			client := &quoteRefClient{}
			client.reply = func(req llm.CompleteRequest, _ int) string {
				index := requestQuoteIndex(t, req)
				response, row, check := quotePositive(index.Scope, map[string]any{"start": 1, "end": len(index.Lines)})
				switch mode {
				case "wrong-source":
					other := *doc
					other.Skill = "other"
					response["source_quote_scope"] = makeSourceQuoteIndex(original, &other).Scope
				case "wrong-bytes":
					response["source_quote_scope"] = makeSourceQuoteIndex(strings.ReplaceAll(original, "\n", " "), doc).Scope
				case "missing-scope":
					delete(response, "source_quote_scope")
				case "null-scope":
					response["source_quote_scope"] = nil
				case "unknown-id":
					row["source_quote_ref"] = map[string]any{"start": 1, "end": len(index.Lines) + 1}
				case "negative-id":
					row["source_quote_ref"] = map[string]any{"start": -1, "end": 1}
				case "reversed":
					row["source_quote_ref"] = map[string]any{"start": 3, "end": 1}
				case "fraction":
					row["source_quote_ref"] = map[string]any{"start": 1.5, "end": 3}
				case "string-id":
					row["source_quote_ref"] = map[string]any{"start": "1", "end": 3}
				case "null-end":
					row["source_quote_ref"] = map[string]any{"start": 1, "end": nil}
				case "extra-ref-field":
					row["source_quote_ref"] = map[string]any{"start": 1, "end": 3, "text": "cannot alter source"}
				case "disjoint":
					row["source_quote_ref"] = []any{map[string]int{"start": 1, "end": 1}, map[string]int{"start": 3, "end": 3}}
				case "dual":
					row["source_quote"] = original
				case "dual-null":
					row["source_quote"] = nil
				case "null-ref":
					row["source_quote_ref"] = nil
				case "blank-only":
					row["source_quote_ref"] = map[string]any{"start": 2, "end": 2}
				case "literal", "literal-near":
					delete(row, "source_quote_ref")
					delete(response, "source_quote_scope")
					row["source_quote"] = original
					if mode == "literal-near" {
						row["source_quote"] = strings.ReplaceAll(original, "\n", " ")
					}
				case "wrong-carrier":
					check["same_referent"] = false
				case "lost-assertion":
					check["preserves_assertion"] = false
				case "bad-item":
					check["item_index"] = 1
				case "null-check":
					check["same_referent"] = nil
				}
				return quoteJSON(response)
			}
			err := NewSemanticGate(client, 65536, -1).Check(context.Background(), original, doc)
			wantPass := mode == "valid" || mode == "literal"
			if (err == nil) != wantPass || len(client.requests) != 1 {
				t.Fatalf("wrong gate outcome mode=%s err=%v calls=%d", mode, err, len(client.requests))
			}
			if mode == "wrong-carrier" || mode == "lost-assertion" {
				var feedback *SemanticFeedback
				if !errors.As(err, &feedback) || len(feedback.Omissions) == 0 {
					t.Fatal("locator was promoted over actual missing carrier/meaning")
				}
			}
		})
	}
}

func TestV016QuoteReferenceMalformedReassessesSameCandidate(t *testing.T) {
	original := "Owner may run only above 2 bar.\n"
	doc := &dktypes.AnnotatedDocument{Skill: "canonical", FilePath: "references/source.md", Items: []dktypes.AnnotatedItem{{Tag: dktypes.TagConcept, Name: "Owner", Content: original}}}
	before := quoteJSON(doc)
	client := &quoteRefClient{}
	client.reply = func(req llm.CompleteRequest, n int) string {
		index := requestQuoteIndex(t, req)
		response, _, _ := quotePositive(index.Scope, map[string]int{"start": 1, "end": 1})
		if n == 1 {
			response["source_quote_scope"] = "wrong-source"
		}
		return quoteJSON(response)
	}
	if err := NewSemanticGate(client, 65536, 1).Check(context.Background(), original, doc); err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 2 || !reflect.DeepEqual(client.requests[0], client.requests[1]) || before != quoteJSON(doc) {
		t.Fatal("malformed evidence rewrote candidate, request or budget")
	}
}

func TestV016QuoteIndexDoesNotExpandTrustedNodeScope(t *testing.T) {
	client := &quoteRefClient{reply: func(req llm.CompleteRequest, _ int) string {
		if strings.Contains(req.User, sourceQuoteIndexMarker) || strings.Contains(req.System, "source_quote_ref") {
			t.Fatal("whole-source protocol leaked into trusted node scope")
		}
		return `{"supported":true,"reason":"given node members only","unsupported_items":[],"omissions":[]}`
	}}
	doc := &dktypes.AnnotatedDocument{Items: []dktypes.AnnotatedItem{{Name: "Owner", Content: "Supplied node fact."}}}
	if err := NewNodeSemanticGate(client, 65536, -1).Check(context.Background(), "Supplied node fact.", doc); err != nil {
		t.Fatal(err)
	}
}
