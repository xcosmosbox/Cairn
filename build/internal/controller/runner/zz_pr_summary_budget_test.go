package runner

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/llm"
)

// A short final summary still needs the configured model's thinking budget.
func TestPRSummaryUsesConfiguredClientBudget(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			MaxTokens int `json:"max_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.MaxTokens != 12000 {
			t.Errorf("summary budget=%d, want configured 12000", body.MaxTokens)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"{\"title\":\"title\",\"description\":\"summary\"}"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	client, err := llm.NewOpenAICompatClient(llm.Config{Endpoint: server.URL, Model: "deepseek-flash", MaxTokens: 12000})
	if err != nil {
		t.Fatal(err)
	}
	summarizer := NewLLMSummarizer(client)
	for _, catalog := range []bool{false, true} {
		summary, err := summarizer.GeneratePRSummary(context.Background(), PRSummaryInput{KGGroup: "test", SourceCommit: "0123456789abcdef", IsCatalog: catalog})
		if err != nil || summary.Title != "title" || summary.Description != "summary" {
			t.Fatalf("catalog=%v summary=%+v err=%v", catalog, summary, err)
		}
	}
}
