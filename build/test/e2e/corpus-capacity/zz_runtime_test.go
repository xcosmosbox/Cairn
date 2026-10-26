package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/llm"
)

type clientFunc func(context.Context, llm.CompleteRequest) (*llm.CompleteResponse, error)

func (f clientFunc) Complete(c context.Context, r llm.CompleteRequest) (*llm.CompleteResponse, error) {
	return f(c, r)
}
func (f clientFunc) ProviderName() string { return "test" }

func TestCallerCancellationStopsQueuedLogicalCall(t *testing.T) {
	var calls atomic.Int32
	client := &boundedClient{next: clientFunc(func(context.Context, llm.CompleteRequest) (*llm.CompleteResponse, error) {
		calls.Add(1)
		return &llm.CompleteResponse{Text: "ok"}, nil
	}), sem: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Complete(ctx, llm.CompleteRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("cancelled call sent")
	}
}

// The diagnostic roster need not contain12/609 rows or meet a coverage floor.
// A malformed row and an unavailable original cannot discard the valid sibling.
func TestArbitraryRosterKeepsInvalidAndMissingDocumentOutcomes(t *testing.T) {
	root := t.TempDir()
	corpus := filepath.Join(root, "sources.json")
	out := filepath.Join(root, "attempt")
	huge := strings.Repeat("material ", 40000) + " UUID 12345678-1234-1234-1234-123456789abc"
	raw, _ := json.Marshal([]any{corpusDoc{URL: "https://example.org/one", Body: huge}, map[string]any{"url": 3}, corpusDoc{URL: "https://example.org/two", Body: "other source"}})
	if err := os.WriteFile(corpus, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := prepare(corpus, out); err != nil {
		t.Fatal(err)
	}
	var initial selection
	if err := readJSON(filepath.Join(out, "selection.json"), &initial); err != nil {
		t.Fatal(err)
	}
	if initial.IntendedDocuments != 3 || len(initial.Documents) != 2 {
		t.Fatalf("lost source denominator: %+v", initial)
	}
	if err := os.Remove(filepath.Join(out, "original", initial.Documents[1].Path)); err != nil {
		t.Fatal(err)
	}
	bound, err := bindPrepared(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(bound.Documents) != 1 || bound.IntendedDocuments != 3 {
		t.Fatal("valid subset or denominator lost")
	}
	if _, err := os.Stat(filepath.Join(out, "source", initial.Documents[1].Path)); !os.IsNotExist(err) {
		t.Fatal("unavailable source remained consumable")
	}
	var outcomes []sourceOutcome
	if err := readJSON(filepath.Join(out, "preparation-outcomes.json"), &outcomes); err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 3 || outcomes[1].Status != "invalid" || len(outcomes[1].Raw) == 0 {
		t.Fatal("invalid source outcome disappeared")
	}
}
