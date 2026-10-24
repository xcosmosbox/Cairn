package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/llm"
)

type memoryLog struct {
	bytes.Buffer
	syncs  int
	failAt int
}

func (m *memoryLog) Sync() error {
	m.syncs++
	if m.failAt > 0 && m.syncs >= m.failAt {
		return errors.New("injected persistence failure")
	}
	return nil
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type countFunc func(context.Context, []byte) (tokenCount, error)

func (f countFunc) Count(ctx context.Context, b []byte) (tokenCount, error) { return f(ctx, b) }
func fittingCounter() tokenCounter {
	return countFunc(func(_ context.Context, b []byte) (tokenCount, error) {
		var r wireRequest
		_ = json.Unmarshal(b, &r)
		return tokenCount{Input: 100, Reserved: r.MaxTokens, Context: 1000000, CounterID: "test-counter", RuntimeSHA: "test-runtime"}, nil
	})
}
func wire(t *testing.T, ctx context.Context) *http.Request {
	t.Helper()
	r, err := http.NewRequestWithContext(context.WithValue(ctx, callKey{}, &callInfo{ID: 1, Stage: "annotation"}), "POST", "https://api.deepseek.com/chat/completions", strings.NewReader(`{"model":"deepseek-flash","max_tokens":200,"messages":[{"role":"system","content":"private system"},{"role":"user","content":"private prompt"}],"thinking":{"type":"enabled"},"response_format":{"type":"json_object"}}`))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer test-private-key")
	return r
}
func response(raw string) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Date": []string{"Sat, 24 Oct 2026 13:00:00 GMT"}}, Body: io.NopCloser(strings.NewReader(raw))}
}
func events(t *testing.T, m *memoryLog) []map[string]any {
	t.Helper()
	var result []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(m.String()), "\n") {
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatal(err)
		}
		result = append(result, row)
	}
	return result
}

// Previously the observer recorded only after dispatch and could lose a charged
// attempt on a ledger failure; unchanged bytes plus a synced start are required.
func TestWireObservationPreservesRequestResponseAndAllFailureMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		readError  bool
		usage      string
	}{
		{"complete", `{"id":"cb977f4b-20bd-4411-98b6-58ad42e913d7","model":"deepseek-v4.1-flash-1017","choices":[{"finish_reason":"stop","message":{"content":"private output","reasoning_content":"private reasoning"}}],"usage":{"prompt_tokens":11,"completion_tokens":17,"total_tokens":28}}`, false, "reported"},
		{"length", `{"model":"deepseek-flash","choices":[{"finish_reason":"length"}],"usage":{"prompt_tokens":11,"completion_tokens":17}}`, false, "reported"},
		{"malformed", `{"private":"private output"`, false, "unknown"},
		{"partial", `partial private output`, true, "unknown"},
		{"missing_usage", `{"choices":[]}`, false, "unknown"},
		{"invalid_usage", `{"model":"unexpected-secret-model","choices":[{"finish_reason":"private output"}],"usage":{"prompt_tokens":-1,"completion_tokens":0}}`, false, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &memoryLog{}
			ledger := &durableLedger{file: m}
			req := wire(t, context.Background())
			want, _ := io.ReadAll(req.Body)
			req.Body = io.NopCloser(bytes.NewReader(want))
			observer := &traceTransport{ledger: ledger, counter: fittingCounter(), next: transportFunc(func(r *http.Request) (*http.Response, error) {
				if m.syncs != 1 {
					t.Fatal("request sent before durable start")
				}
				actual, _ := io.ReadAll(r.Body)
				if !bytes.Equal(actual, want) || r.Header.Get("Authorization") != "Bearer test-private-key" {
					t.Fatal("outgoing body or authorization changed")
				}
				resp := response(tc.body)
				if tc.readError {
					resp.Body = &replayedBody{Reader: bytes.NewReader([]byte(tc.body)), readErr: io.ErrUnexpectedEOF}
				}
				return resp, nil
			})}
			resp, err := observer.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			raw, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if string(raw) != tc.body || (tc.readError && readErr != io.ErrUnexpectedEOF) || (!tc.readError && readErr != nil) {
				t.Fatal("response semantics changed")
			}
			rows := events(t, m)
			if len(rows) != 2 || rows[0]["admitted"] != true || rows[1]["usage_status"] != tc.usage {
				t.Fatalf("missing receipt: %#v", rows)
			}
			for _, secret := range []string{"test-private-key", "private system", "private prompt", "private output", "private reasoning", "unexpected-secret-model", "Authorization"} {
				if strings.Contains(m.String(), secret) {
					t.Fatalf("receipt leaked %q", secret)
				}
			}
		})
	}
}

func TestContextAndStickyLedgerFailuresPreventFurtherSends(t *testing.T) {
	for _, kind := range []string{"unknown_counter", "overflow", "prewrite", "postwrite"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			m := &memoryLog{}
			counter := fittingCounter()
			switch kind {
			case "unknown_counter":
				counter = nil
			case "overflow":
				counter = countFunc(func(context.Context, []byte) (tokenCount, error) {
					return tokenCount{Input: 999999, Reserved: 200, Context: 1000000, CounterID: "test", RuntimeSHA: "test"}, nil
				})
			case "prewrite":
				m.failAt = 1
			case "postwrite":
				m.failAt = 2
			}
			ledger := &durableLedger{file: m, cancel: cancel}
			sends := 0
			observer := &traceTransport{ledger: ledger, counter: counter, next: transportFunc(func(*http.Request) (*http.Response, error) {
				sends++
				return response(`{"choices":[{"finish_reason":"stop"}]}`), nil
			})}
			_, _ = observer.RoundTrip(wire(t, ctx))
			_, _ = observer.RoundTrip(wire(t, ctx))
			want := 0
			if kind == "postwrite" {
				want = 1
			}
			if sends != want {
				t.Fatalf("admitted %d sends; want%d", sends, want)
			}
			if strings.Contains(kind, "write") && ctx.Err() == nil {
				t.Fatal("ledger failure did not cancel run")
			}
		})
	}
}

// Observe actual nested provider retries through the real client rather than
// simulating retry counters in the audit layer.
func TestProductionRetryAttemptsAndPayloadAreObserved(t *testing.T) {
	t.Setenv("CAIRN_LLM_API_KEY", "test-private-key")
	old := http.DefaultTransport
	defer func() { http.DefaultTransport = old }()
	m := &memoryLog{}
	ledger := &durableLedger{file: m}
	var payloads [][]byte
	observer := &traceTransport{ledger: ledger, counter: fittingCounter(), next: transportFunc(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		payloads = append(payloads, b)
		if len(payloads) == 1 {
			return &http.Response{StatusCode: 429, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"private output"}`))}, nil
		}
		return response(`{"model":"deepseek-flash","choices":[{"finish_reason":"stop","message":{"content":"{}"}}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`), nil
	})}
	http.DefaultTransport = observer
	client, err := llm.NewOpenAICompatClient(llm.Config{Model: "deepseek-flash", Endpoint: "https://api.deepseek.com/chat/completions", MaxTokens: 200, MaxRetries: 1})
	if err != nil {
		t.Fatal(err)
	}
	bounded := &boundedClient{next: client, sem: make(chan struct{}, 8), ledger: ledger}
	if _, err = bounded.Complete(context.Background(), llm.CompleteRequest{System: "private system", User: "private prompt"}); err != nil {
		t.Fatal(err)
	}
	if len(payloads) != 2 || !bytes.Equal(payloads[0], payloads[1]) {
		t.Fatal("production retry changed request or receipt missing")
	}
	rows := events(t, m)
	if len(rows) != 6 || rows[1]["provider_attempt"] != float64(1) || rows[3]["provider_attempt"] != float64(2) {
		t.Fatalf("retry receipt identity missing: %#v", rows)
	}
}

type blockingClient struct {
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int64
}

func (*blockingClient) ProviderName() string { return "test" }
func (c *blockingClient) Complete(ctx context.Context, _ llm.CompleteRequest) (*llm.CompleteResponse, error) {
	c.calls.Add(1)
	c.entered <- struct{}{}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.release:
		return &llm.CompleteResponse{}, nil
	}
}
func TestSemaphoreIsGlobalAndCancellationStopsQueuedCalls(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	next := &blockingClient{entered: make(chan struct{}, 20), release: make(chan struct{})}
	m := &memoryLog{}
	c := &boundedClient{next: next, sem: make(chan struct{}, 8), ledger: &durableLedger{file: m}}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = c.Complete(ctx, llm.CompleteRequest{}) }()
	}
	for i := 0; i < 8; i++ {
		select {
		case <-next.entered:
		case <-time.After(3 * time.Second):
			t.Fatal("bounded calls not admitted")
		}
	}
	if c.peak.Load() != 8 || next.calls.Load() != 8 {
		t.Fatal("concurrency cap violated")
	}
	cancel()
	wg.Wait()
	if next.calls.Load() != 8 {
		t.Fatal("queued request sent after cancellation")
	}
}

func TestOfficialCounterMissingRuntimeFailsWithoutEchoingInput(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python unavailable")
	}
	adapter := filepath.Join(rootPath(), "e2e/benchmarks/multihop_public/capacity/token_count.py")
	command := exec.Command(python, "-I", adapter, "--runtime-receipt", filepath.Join(t.TempDir(), "missing"), "--runtime-receipt-sha256", "bad")
	command.Stdin = strings.NewReader("private prompt test-private-key")
	raw, err := command.CombinedOutput()
	if err == nil || strings.Contains(string(raw), "private") {
		t.Fatalf("counter fallback/leak: %s", raw)
	}
	if _, err = os.Stat(adapter); err != nil {
		t.Fatal(err)
	}
}

func TestProductionAnnotationPromptDocumentIdentity(t *testing.T) {
	t.Parallel()
	id := strings.Repeat("a", 64)
	prompt := "# 待标注文档\n\n文档路径 / path: references/" + id + ".md\n\n文档内容 / content:\noriginal body\n"
	if documentIdentity(prompt) != id {
		t.Fatal("real production annotation path was not bound")
	}
}
