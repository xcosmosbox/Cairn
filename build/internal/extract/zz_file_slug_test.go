package extract

import (
	"context"
	"errors"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/llm"
)

func TestSlugIncompleteResponseFallsBackWithoutClaimingLLMSuccess(t *testing.T) {
	for _, reason := range []string{"length", "max_tokens"} {
		t.Run(reason, func(t *testing.T) {
			client := llm.NewMockClient()
			// Even syntactically complete content is unusable if the provider
			// reports that the completion exhausted its token budget.
			client.Response = &llm.CompleteResponse{Text: `{"slug":"must-not-be-used"}`, FinishReason: reason}
			pending := []fileSlugNodeRef{{domain: "domain", node: &Node{Name: "source name"}}}
			slugs := generateSlugsConcurrent(context.Background(), client, pending)
			if slugs[0] != "" || len(client.Requests) != fileSlugMaxRetries {
				t.Fatalf("incomplete response counted as LLM success: slugs=%v calls=%d", slugs, len(client.Requests))
			}
			res, _ := repairProvenanceFixture()
			GenerateFileSlugs(context.Background(), res.Domains, client)
			if got := res.Domains[0].Subdomains[0].Entities[0].FileSlug; got != "existing" {
				t.Fatalf("fallback slug = %q, want existing", got)
			}
		})
	}
}

func TestSlugProviderErrorIsNotCountedAsSuccess(t *testing.T) {
	client := llm.NewMockClient()
	client.Err = errors.New(`incomplete response (finish_reason="length")`)
	if got := generateSingleSlug(context.Background(), client, "source name", "domain"); got != "" {
		t.Fatalf("provider failure returned a success slug: %q", got)
	}
}

func TestSlugInheritsModelTokenBudget(t *testing.T) {
	client := llm.NewMockClient()
	client.Response = &llm.CompleteResponse{Text: `{"slug":"source-name"}`, FinishReason: "stop"}
	if got := generateSingleSlug(context.Background(), client, "Source name", "domain"); got != "source-name" {
		t.Fatalf("slug=%q", got)
	}
	if len(client.Requests) != 1 || client.Requests[0].MaxTokens != 0 {
		t.Fatalf("slug request overrides configured model budget: %+v", client.Requests)
	}
}

func TestSlugCancellationStopsPaidCallsAndUsesFallback(t *testing.T) {
	client := llm.NewMockClient()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, _ := repairProvenanceFixture()
	// More nodes than the semaphore capacity also exercises canceled waiters.
	sd := &res.Domains[0].Subdomains[0]
	for i := 0; i < fileSlugMaxConcurrency*2; i++ {
		sd.Entities = append(sd.Entities, Node{Name: "another source"})
	}
	GenerateFileSlugs(ctx, res.Domains, client)
	if len(client.Requests) != 0 {
		t.Fatalf("canceled parent still issued %d LLM calls", len(client.Requests))
	}
	for _, n := range sd.Entities {
		if n.FileSlug == "" {
			t.Fatal("cancellation omitted deterministic fallback")
		}
	}
}
