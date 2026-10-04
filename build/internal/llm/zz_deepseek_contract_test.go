package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/core/dkconfig"
)

// 原实现默认 Pro、发送预算各不相同，且把中断后仍合法的 JSON 当作完整成功。
// Exercise the serialized HTTP contract and reject interrupted but valid JSON.
func TestDeepSeekFlashHTTPContract(t *testing.T) {
	t.Setenv("CAIRN_TEST_LLM_KEY", "synthetic-contract-key")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Error("expected JSON POST")
		}
		if r.Header.Get("Authorization") != "Bearer synthetic-contract-key" {
			t.Error("expected configured bearer authentication")
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		for key, expected := range map[string]string{
			"model": `"deepseek-flash"`, "max_tokens": "65536",
			"thinking": `{"type":"enabled"}`, "response_format": `{"type":"json_object"}`,
		} {
			if string(body[key]) != expected {
				t.Errorf("%s = %s, want %s", key, body[key], expected)
			}
		}
		for _, key := range []string{"temperature", "top_p", "presence_penalty", "frequency_penalty", "reasoning_effort"} {
			if _, exists := body[key]; exists {
				t.Errorf("unexpected override %s", key)
			}
		}
		var messages []openAIMsg
		if err := json.Unmarshal(body["messages"], &messages); err != nil || len(messages) != 2 {
			t.Errorf("messages = %s, err %v", body["messages"], err)
		} else if messages[0] != (openAIMsg{Role: "system", Content: "Return JSON: {\"ok\":true}"}) || messages[1] != (openAIMsg{Role: "user", Content: "Please check"}) {
			t.Error("system and user prompts were not preserved")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"{\"ok\":true}","reasoning_content":"reasoning must stay out of content"},"finish_reason":"stop"}],"usage":{"prompt_tokens":17,"completion_tokens":23}}`)
	}))
	defer server.Close()
	cfg := dkconfig.Defaults().LLM
	client, err := NewOpenAICompatClient(Config{Endpoint: server.URL, Model: cfg.Model, MaxTokens: cfg.MaxTokens, APIKeyEnv: "CAIRN_TEST_LLM_KEY"})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Complete(context.Background(), CompleteRequest{System: "Return JSON: {\"ok\":true}", User: "Please check", Temperature: 0.8})
	if err != nil {
		t.Fatal(err)
	}
	if response.Text != `{"ok":true}` || response.InputTokens != 17 || response.OutputTokens != 23 || response.FinishReason != "stop" {
		t.Fatalf("unexpected response: %+v", response)
	}
}

func TestOpenAIRejectsIncompleteFinishReasons(t *testing.T) {
	t.Parallel()
	for _, reason := range []string{"length", "content_filter", "tool_calls", "insufficient_system_resource", "aborted", ""} {
		t.Run(reason, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// 这是合法且形状完整的 JSON，旧实现会直接交给知识抽取器。
				// Valid JSON alone cannot prove generation completed.
				fmt.Fprintf(w, `{"choices":[{"message":{"content":"{\"domains\":[]}"},"finish_reason":%q}]}`, reason)
			}))
			defer server.Close()
			client, err := NewOpenAICompatClient(Config{Endpoint: server.URL, Model: "deepseek-flash"})
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.Complete(context.Background(), CompleteRequest{User: "Return JSON"})
			if err == nil || response != nil || !strings.Contains(err.Error(), "finish_reason") {
				t.Fatalf("incomplete response accepted: response=%+v err=%v", response, err)
			}
		})
	}
}

type contractTransport func(*http.Request) (*http.Response, error)

func (fn contractTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestDeepSeekBudgetsBeforeHTTP(t *testing.T) {
	t.Parallel()
	if _, err := NewOpenAICompatClient(Config{MaxTokens: dkconfig.DeepSeekMaxOutputTokens + 1}); err == nil {
		t.Fatal("configuration above official limit accepted")
	}
	client, err := NewOpenAICompatClient(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if client.config.Model != "deepseek-flash" || client.config.MaxTokens != 65536 {
		t.Fatal("direct client defaults disagree with controller defaults")
	}
	calls := 0
	client.httpClient.Transport = contractTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		var request openAIReq
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.MaxTokens != 393216 {
			t.Errorf("boundary request budget = %d", request.MaxTokens)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"{}"},"finish_reason":"stop"}]}`)), Header: make(http.Header)}, nil
	})
	if _, err := client.Complete(context.Background(), CompleteRequest{User: "JSON", MaxTokens: 393216}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Complete(context.Background(), CompleteRequest{User: "JSON", MaxTokens: 393217}); err == nil {
		t.Fatal("per-request override above official limit accepted")
	}
	if _, err := client.Complete(context.Background(), CompleteRequest{User: "JSON", StopSequences: make([]string, 17)}); err == nil {
		t.Fatal("too many stop sequences accepted")
	}
	if calls != 1 {
		t.Fatalf("invalid requests reached transport; calls=%d", calls)
	}
	// 替换后端可有其他输出上限；不得用 DeepSeek 限制全体兼容服务。
	// Preserve other compatible providers' independent output limits.
	if _, err := NewOpenAICompatClient(Config{Endpoint: "https://other.example/chat/completions", MaxTokens: 500000}); err != nil {
		t.Fatal(err)
	}
}

func TestOpenAITimeoutMustBePositive(t *testing.T) {
	t.Parallel()
	for _, timeout := range []string{"invalid", "0s", "-1s"} {
		if _, err := NewOpenAICompatClient(Config{Timeout: timeout}); err == nil {
			t.Errorf("timeout %q was silently ignored", timeout)
		}
	}
}
