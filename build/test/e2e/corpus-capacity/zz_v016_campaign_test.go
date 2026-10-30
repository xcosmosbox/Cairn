package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/llm"
)

func TestV016UnavailablePredecessorCannotRunDependentPhase(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "fixed")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "original.md"), []byte("retained original"), 0600); err != nil {
		t.Fatal(err)
	}
	oldPhase, oldTree, oldPrevious := *v016Phase, *v016SourceTree, *v016Previous
	defer func() { *v016Phase, *v016SourceTree, *v016Previous = oldPhase, oldTree, oldPrevious }()
	*v016Phase, *v016SourceTree, *v016Previous = "unchanged", source, filepath.Join(root, "missing-full")
	out := filepath.Join(root, "dependent")
	if err := runV016Phase(out, 32768, time.Second, 1); err == nil {
		t.Fatal("missing baseline became a completed in-place check")
	}
	raw, err := os.ReadFile(filepath.Join(out, "phase-finish.json"))
	if err != nil {
		t.Fatal(err)
	}
	var receipt struct {
		Status           string `json:"status"`
		ProviderAttempts int    `json:"provider_attempts"`
	}
	if err = json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Status != "unavailable" || receipt.ProviderAttempts != 0 {
		t.Fatalf("dependent receipt=%s", raw)
	}
	if _, err = os.Stat(filepath.Join(out, "requests.jsonl")); !os.IsNotExist(err) {
		t.Fatal("dependent phase initialized provider transport")
	}
	var roster []v016SourceIdentity
	if err = readJSON(filepath.Join(out, "source-roster.json"), &roster); err != nil || len(roster) != 2 {
		t.Fatalf("denominator lost: %+v %v", roster, err)
	}
}

// An unavailable shared identity used to invite one identical failing request
// per source. Only one actual denial may be sent; all sources keep local errors.
func TestV016AuthenticationDenialStopsSiblingRequests(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	client := &v016AuthClient{next: clientFunc(func(context.Context, llm.CompleteRequest) (*llm.CompleteResponse, error) {
		calls.Add(1)
		return nil, fmt.Errorf("openai_compatible: status 401: Authentication Fails")
	})}
	var workers sync.WaitGroup
	for i := 0; i < 14; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if response, err := client.Complete(context.Background(), llm.CompleteRequest{}); response != nil || err == nil || !strings.Contains(err.Error(), "status 401:") {
				t.Errorf("denial became semantic/provider success: response=%v err=%v", response, err)
			}
		}()
	}
	workers.Wait()
	if calls.Load() != 1 {
		t.Fatalf("provider calls=%d, want one authentic denial", calls.Load())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Complete(ctx, llm.CompleteRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled sibling=%v", err)
	}
}

func TestV016NonAuthenticationFailureDoesNotStopIndependentSources(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	client := &v016AuthClient{next: clientFunc(func(context.Context, llm.CompleteRequest) (*llm.CompleteResponse, error) {
		if calls.Add(1) == 1 {
			return nil, fmt.Errorf("openai_compatible: status 503: unavailable")
		}
		return &llm.CompleteResponse{Text: "actual next response"}, nil
	})}
	if _, err := client.Complete(context.Background(), llm.CompleteRequest{}); err == nil {
		t.Fatal("first failure disappeared")
	}
	if response, err := client.Complete(context.Background(), llm.CompleteRequest{}); err != nil || response == nil || calls.Load() != 2 {
		t.Fatalf("independent source stopped: response=%v err=%v calls=%d", response, err, calls.Load())
	}
}

// Builder preparation must copy exact original bytes and empty legitimate
// markers, without accepting a neighboring inventory/expectation JSON file.
func TestV016SourceTreeCopiesExactBytesAndRejectsInventoryOrLinks(t *testing.T) {
	t.Parallel()
	for _, invalid := range []string{"inventory", "link"} {
		t.Run(invalid, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "sources")
			if err := os.Mkdir(source, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(source, "SKILL.md"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			original := []byte("qualified fact\r\nUUID 123e4567-e89b-12d3-a456-426614174000\n")
			if err := os.WriteFile(filepath.Join(source, "original.md"), original, 0600); err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(root, "prepared")
			roster, err := copyV016Tree(source, out, true)
			if err != nil || len(roster) != 2 {
				t.Fatalf("roster=%v err=%v", roster, err)
			}
			copied, err := os.ReadFile(filepath.Join(out, "original.md"))
			if err != nil || string(copied) != string(original) {
				t.Fatal("authoritative bytes changed")
			}
			if invalid == "inventory" {
				err = os.WriteFile(filepath.Join(source, "inventory.json"), []byte(`{"expected":"secret judge material"}`), 0600)
			} else {
				err = os.Symlink(filepath.Join(source, "original.md"), filepath.Join(source, "linked.md"))
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = copyV016Tree(source, filepath.Join(root, "rejected"), true); err == nil {
				t.Fatal("non-source evidence/link accepted")
			}
		})
	}
}
