package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// 原风险：另写预检序列化器与实际doComplete产生不同的转义、默认值或请求预算。
// Pure rendering and the injected real doComplete transport must see exactly the same bytes.
func TestOpenAIRenderMatchesProductionTransportAcrossProfiles(t *testing.T) {
	for _, tc := range []struct {
		name    string
		config  Config
		request CompleteRequest
		tokens  int
	}{
		{"defaults", Config{}, CompleteRequest{System: "Return JSON <>&中文", User: "full \"quoted\" \\ body\n"}, 65536},
		{"request-override", Config{MaxTokens: 1024}, CompleteRequest{System: "system", User: "user", MaxTokens: 65536, Temperature: .7, StopSequences: []string{"\"stop\"", "中文"}}, 65536},
		{"other-provider-fallback", Config{Endpoint: "https://other.example/completions", Model: "other-model"}, CompleteRequest{User: "JSON"}, 4096},
		{"other-provider-large", Config{Endpoint: "https://other.example/completions", Model: "other-model", MaxTokens: 500000}, CompleteRequest{User: "JSON"}, 500000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			var sent []byte
			var endpoint string
			transport := contractTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != "POST" {
					t.Error("wrong actual method")
				}
				endpoint = r.URL.String()
				sent, _ = io.ReadAll(r.Body)
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"choices":[{"finish_reason":"stop","message":{"content":"{}"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))}, nil
			})
			client, err := NewOpenAICompatClientWithTransport(tc.config, transport)
			if err != nil {
				t.Fatal(err)
			}
			wantEndpoint, want, err := client.RenderRequest(tc.request)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 0 {
				t.Fatal("pure render performed HTTP")
			}
			var body map[string]any
			if json.Unmarshal(want, &body) != nil || int(body["max_tokens"].(float64)) != tc.tokens {
				t.Fatal("effective defaults missing")
			}
			if _, exists := body["temperature"]; exists {
				t.Fatal("render introduced sampling override")
			}
			copyBody := append([]byte(nil), want...)
			want[0] = '!'
			againEndpoint, again, err := client.RenderRequest(tc.request)
			if err != nil || againEndpoint != wantEndpoint || !bytes.Equal(again, copyBody) {
				t.Fatal("rendered body aliases caller storage")
			}
			if _, err := client.Complete(context.Background(), tc.request); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || endpoint != wantEndpoint || !bytes.Equal(sent, copyBody) {
				t.Fatal("production transport differs from pure renderer")
			}
		})
	}
}

func TestOpenAIRenderPreservesConstructorSignatureAndDefaultNilTransport(t *testing.T) {
	t.Parallel()
	var oldConstructor func(Config) (*OpenAICompatClient, error) = NewOpenAICompatClient
	old, err := oldConstructor(Config{})
	if err != nil {
		t.Fatal(err)
	}
	injected, err := NewOpenAICompatClientWithTransport(Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if old.httpClient.Transport != nil || injected.httpClient.Transport != nil || old.endpoint != injected.endpoint || !reflect.DeepEqual(old.config, injected.config) {
		t.Fatal("nil/default constructor behavior changed")
	}
	r := CompleteRequest{System: "system", User: "user"}
	a, x, e := old.RenderRequest(r)
	b, y, f := injected.RenderRequest(r)
	if e != nil || f != nil || a != b || !bytes.Equal(x, y) {
		t.Fatal("default render parity changed")
	}
}

func TestOpenAIRenderRejectsInvalidProfileWithoutTransport(t *testing.T) {
	t.Parallel()
	calls := 0
	client, err := NewOpenAICompatClientWithTransport(Config{}, contractTransport(func(*http.Request) (*http.Response, error) { calls++; return nil, io.ErrUnexpectedEOF }))
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []CompleteRequest{{MaxTokens: 393217}, {StopSequences: make([]string, 17)}} {
		if _, _, err := client.RenderRequest(request); err == nil {
			t.Fatal("invalid pure render accepted")
		}
		if _, err := client.Complete(context.Background(), request); err == nil {
			t.Fatal("invalid production request accepted")
		}
	}
	if calls != 0 {
		t.Fatal("invalid render reached injected transport")
	}
}
