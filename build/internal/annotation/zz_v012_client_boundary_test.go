package annotation

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/llm"
)

func v012BoundaryReply(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
		"message": map[string]string{"content": text}, "finish_reason": "stop",
	}}})
}

func v012BoundaryClient(t *testing.T, endpoint, timeout string) llm.Client {
	t.Helper()
	client, err := llm.NewOpenAICompatClient(llm.Config{
		Endpoint: endpoint, Timeout: timeout, MaxRetries: -1,
		APIKeyEnv: "CAIRN_V012_BOUNDARY_UNUSED_KEY",
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// 配置错误修复不能把实际服务响应误判为永久错误，也不能重跑成功生成。
// Actual provider status/rate-limit errors must retry the identical assessment candidate.
func TestV012ProductionClientServiceResponseRetriesAssessment(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()
			var mu sync.Mutex
			var requests [][]byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read local request: %v", err)
				}
				mu.Lock()
				requests = append(requests, body)
				n := len(requests)
				mu.Unlock()
				switch n {
				case 1:
					v012BoundaryReply(w, `{"items":[{"tag":"concept","name":"Orbit","content":"Orbit preserves one fact.","detail":"","confidence":0.9}]}`)
				case 2:
					w.WriteHeader(status)
					_, _ = io.WriteString(w, `unsupported protocol scheme "ftp"; http: no Host in request URL; missing API key; max_tokens`)
				default:
					v012BoundaryReply(w, `{"supported":true,"reason":"the one fact is preserved","unsupported_items":[],"omissions":[]}`)
				}
			}))
			defer server.Close()
			result := NewDocumentProcessor(v012BoundaryClient(t, server.URL, "2s"), 0, 1, 0).
				Process(context.Background(), "scope", "fact.md", "Orbit preserves one fact.")
			if result.Outcome != DocumentComplete || result.Exhausted || len(result.Attempts) != 3 {
				t.Fatalf("service failure did not recover: %+v", result)
			}
			wantSteps, wantOutcomes := []string{"annotate", "assess", "assess"}, []string{"generated", "unavailable", "complete"}
			for i, attempt := range result.Attempts {
				if attempt.Step != wantSteps[i] || attempt.Outcome != wantOutcomes[i] || attempt.CandidateID != result.CandidateID {
					t.Fatalf("attempt %d changed candidate/routing: %+v", i, attempt)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if len(requests) != 3 || !reflect.DeepEqual(requests[1], requests[2]) {
				t.Fatalf("wire requests=%d; assessment was not repeated identically", len(requests))
			}
			if result.Attempts[1].Reason == "" {
				t.Fatal("service failure reason disappeared")
			}
		})
	}
}

// url.Error 也包装客户端超时；一概永久化会误删仍可重试的暂时故障。
// A real client timeout under a live caller consumes only the finite failed-step allowance.
func TestV012ProductionClientTimeoutRemainsUnavailable(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		<-release
	}))
	defer server.Close()
	defer close(release)
	ctx := context.Background()
	result := NewDocumentProcessor(v012BoundaryClient(t, server.URL, "30ms"), 0, 1, 0).
		Process(ctx, "scope", "fact.md", "Orbit preserves one fact.")
	if ctx.Err() != nil || result.Outcome != DocumentUnavailable || !result.Exhausted || len(result.Attempts) != 2 || calls.Load() != 2 {
		t.Fatalf("client timeout became terminal configuration/cancellation: calls=%d result=%+v", calls.Load(), result)
	}
	for _, attempt := range result.Attempts {
		if attempt.Step != "annotate" || attempt.Outcome != "unavailable" || !strings.Contains(attempt.Reason, "timeout") {
			t.Fatalf("timeout lost its failed step/reason: %+v", attempt)
		}
	}
}

// caller 取消必须直接终止，不能与客户端超时一起消耗恢复预算。
// Cancellation during actual HTTP work stops dependent work after its single attempt.
func TestV012ProductionClientCallerCancellationStops(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		cancel()
		<-release
	}))
	defer server.Close()
	defer close(release)
	result := NewDocumentProcessor(v012BoundaryClient(t, server.URL, "2s"), 0, 2, 0).
		Process(ctx, "scope", "fact.md", "Orbit preserves one fact.")
	if result.Outcome != DocumentCancelled || result.Exhausted || len(result.Attempts) != 1 || calls.Load() != 1 || result.Attempts[0].Outcome != "cancelled" {
		t.Fatalf("caller cancellation retried/lost: calls=%d result=%+v", calls.Load(), result)
	}
}
