package extract

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/build/internal/llm"
)

// 原故障被 mock 客户端掩盖：JSON mode 在模型生成前就拒绝缺少 JSON 指令的请求。
// Exercise the real HTTP client and helper; isolate coverage so generation cannot hide its defect.
func TestV016RelationJSONProviderContract(t *testing.T) {
	t.Parallel()
	for _, coverageOnly := range []bool{false, true} {
		name := "generation-and-coverage"
		if coverageOnly {
			name = "coverage-after-accepted-generation"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			stages := make(chan string, 16)
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var wire struct {
					Messages       []struct{ Role, Content string }
					ResponseFormat struct{ Type string } `json:"response_format"`
				}
				if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" {
					t.Errorf("unexpected provider route: %s %s", r.Method, r.URL.Path)
					http.Error(w, "unexpected route", http.StatusBadRequest)
					return
				}
				if err := json.NewDecoder(r.Body).Decode(&wire); err != nil || len(wire.Messages) != 2 || wire.Messages[0].Role != "system" || wire.Messages[1].Role != "user" || wire.ResponseFormat.Type != "json_object" {
					t.Errorf("invalid production request contract: decode=%v messages=%d format=%q", err, len(wire.Messages), wire.ResponseFormat.Type)
					http.Error(w, "invalid request", http.StatusBadRequest)
					return
				}
				req := llm.CompleteRequest{System: wire.Messages[0].Content, User: wire.Messages[1].Content}
				stage := "verify"
				if strings.HasPrefix(req.System, "SOURCE-RELATION GENERATE") {
					stage = "generate"
				} else if strings.HasPrefix(req.System, "SOURCE-RELATION COVERAGE") {
					stage = "coverage"
				}
				stages <- stage
				// 只检查消息内容；response_format 字段自身的 json 不满足提供方协议。
				// Coverage-only admission deliberately lets generation through to expose the second prompt.
				hasJSONInstruction := strings.Contains(strings.ToLower(req.System+"\n"+req.User), "json")
				if (!coverageOnly || stage == "coverage") && !hasJSONInstruction {
					http.Error(w, "Prompt must contain the word json in some form to use response_format of type json_object", http.StatusBadRequest)
					return
				}
				reply, err := v014DefaultReply(r.Context(), req)
				if err != nil {
					t.Errorf("synthetic provider reply: %v", err)
					http.Error(w, "invalid fixture", http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": reply.Text}}}})
			}))
			defer provider.Close()
			client, err := llm.NewOpenAICompatClientWithTransport(llm.Config{
				Endpoint: provider.URL + "/chat/completions", APIKeyEnv: "CAIRN_V016_LOOPBACK_UNUSED_KEY", MaxRetries: -1, Timeout: "5s",
			}, provider.Client().Transport)
			if err != nil {
				t.Fatal(err)
			}
			res, docs, originals := v014Fixture(1)
			report := NewExtractor(client, 4096).ProcessRelations(context.Background(), res, docs, originals, RelationProcessOptions{MaxRetries: -1})
			if len(report.Scopes) != 1 || report.Scopes[0].Outcome != annotation.DocumentComplete || report.Scopes[0].Exhausted || len(report.Selected) != 1 {
				t.Fatalf("provider-admitted relation must finish with one selected edge: %+v", report)
			}
			var seen []string
			for len(stages) > 0 {
				seen = append(seen, <-stages)
			}
			if !reflect.DeepEqual(seen, []string{"generate", "coverage", "verify"}) || len(report.Scopes[0].Attempts) != 3 {
				t.Fatalf("unexpected helper calls/recoveries: stages=%v attempts=%+v", seen, report.Scopes[0].Attempts)
			}
			selected := report.Selected[0]
			if selected.Classification != "explicit" || len(selected.Evidence) != 2 || len(selected.Sources) != 1 || selected.Sources[0].SHA256 != originals[0].SHA256 {
				t.Fatalf("accepted relation lost explicit source binding: %+v", selected)
			}
			relations := res.Domains[0].Subdomains[0].Relations
			if len(relations) != 1 || relations[0].Source != "a1" || relations[0].Target != "b1" || relations[0].Kind != "depends_on" || relations[0].Description != "only when bypass is off; when on no dependency" {
				t.Fatalf("accepted JSON did not reach the candidate graph faithfully: %+v", relations)
			}
		})
	}
}
