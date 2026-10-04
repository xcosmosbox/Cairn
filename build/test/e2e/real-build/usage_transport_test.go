package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type testTransport func(*http.Request) (*http.Response, error)

func (f testTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestUsageObserverCountsTruncationWithoutChangingBodyOrPersistingSecrets(t *testing.T) {
	responseBody := `{"choices":[{"finish_reason":"length","message":{"content":"private response"}}],"usage":{"prompt_tokens":11,"completion_tokens":17,"total_tokens":28}}`
	file, err := os.Create(filepath.Join(t.TempDir(), "usage.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	observer := &usageTransport{file: file, next: testTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Fatal("authorization changed")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(responseBody))}, nil
	})}
	request, err := http.NewRequestWithContext(context.Background(), "POST", "https://api.deepseek.com/chat/completions", strings.NewReader(`{"model":"deepseek-flash","max_tokens":500,"messages":[{"content":"private prompt"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer test-secret")
	response, err := observer.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || string(body) != responseBody {
		t.Fatalf("response was changed: %q / %v", body, err)
	}
	summary := observer.summary()
	if summary["length_responses"] != 1 || summary["input_tokens"] != 11 || summary["output_tokens"] != 17 {
		t.Fatalf("truncated usage missing: %#v", summary)
	}
	ledger, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"test-secret", "private prompt", "private response", "Authorization"} {
		if strings.Contains(string(ledger), forbidden) {
			t.Fatalf("ledger retained %q", forbidden)
		}
	}
}

func TestUsageObserverPreservesResponseReadFailure(t *testing.T) {
	file, err := os.Create(filepath.Join(t.TempDir(), "usage.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	observer := &usageTransport{file: file, next: testTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: &replayedBody{Reader: bytes.NewReader([]byte("partial")), readErr: io.ErrUnexpectedEOF}}, nil
	})}
	request, _ := http.NewRequest("POST", "https://api.deepseek.com/chat/completions", nil)
	response, err := observer.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	if string(body) != "partial" || err != io.ErrUnexpectedEOF {
		t.Fatalf("read failure hidden: %q / %v", body, err)
	}
	if observer.summary()["attempts_without_usage"] != 1 {
		t.Fatal("missing usage should be explicit")
	}
}
