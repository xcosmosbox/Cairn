package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/build/internal/llm"
)

type nodeWireMetadata struct {
	NodeID string `json:"node_id"`
	Label  string `json:"label"`
	Name   string `json:"name"`
}

func readNodeWireMetadata(user string) (nodeWireMetadata, error) {
	_, data, ok := strings.Cut(user, "## Node metadata / 节点元数据\n")
	var metadata nodeWireMetadata
	if !ok || json.NewDecoder(strings.NewReader(data)).Decode(&metadata) != nil || metadata.NodeID == "" {
		return metadata, fmt.Errorf("missing JSON node metadata")
	}
	return metadata, nil
}

func nodeAliasWireClient(t *testing.T, respond func(llm.CompleteRequest) string) llm.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var wire struct {
			Messages []struct{ Role, Content string }
			Format   struct{ Type string } `json:"response_format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&wire); err != nil || len(wire.Messages) != 2 || wire.Messages[0].Role != "system" || wire.Messages[1].Role != "user" || wire.Format.Type != "json_object" {
			t.Error("production client did not send the expected JSON message envelope")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		content := respond(llm.CompleteRequest{System: wire.Messages[0].Content, User: wire.Messages[1].Content})
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}, "finish_reason": "stop"}}})
	}))
	t.Cleanup(server.Close)
	client, err := llm.NewOpenAICompatClient(llm.Config{Endpoint: server.URL, Model: "deepseek-flash", APIKeyEnv: "CAIRN_NODE_ALIAS_WIRE_UNUSED_KEY", MaxRetries: -1})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// 旧标题把 [Entity]/[Concept] 接在 alias 后，模型照抄后始终过不了严格身份检查。
// Exercise the actual helper/client wire, not a keyword check or a permissive ID parser.
func TestNodeAliasWireRevisionPreservesCritiqueAndCandidate(t *testing.T) {
	t.Parallel()
	for _, label := range []string{"Entity", "Concept"} {
		t.Run(label, func(t *testing.T) {
			subdomain := " pump group [Concept] \"雪\"\nsecond line "
			name := "Valve [Entity] \"蓝\"\nsource name"
			alias := subdomain + "#1"
			input := NodeDescriptionInput{ID: "persistent-identity-must-stay-local", Label: label, Name: name, Summary: "First fact.", Description: "First fact.", Material: "First fact. Maintenance requires annual checks."}
			generations, assessments := 0, 0
			client := nodeAliasWireClient(t, func(req llm.CompleteRequest) string {
				if strings.Contains(req.User, input.ID) {
					t.Error("persistent node identity entered model input")
				}
				if strings.Contains(req.System, "语义核验专家") {
					assessments++
					item := v013SemanticCandidate(t, req)
					if assessments == 1 {
						if item.Content != input.Summary || item.Detail != input.Description {
							t.Error("initial whole candidate was changed")
						}
						return `{"supported":true,"reason":"annual check omitted","unsupported_items":[],"omissions":["Maintenance requires annual checks."]}`
					}
					if item.Content != "Both source rules." || item.Detail != input.Material {
						t.Error("malformed revision was assessed or source material was lost")
					}
					return nodeSupported
				}
				generations++
				metadata, err := readNodeWireMetadata(req.User)
				if err != nil {
					t.Error(err)
					return nodeWire("unrecoverable legacy heading ["+label+"]", "invalid", "invalid")
				}
				if metadata.NodeID != alias || metadata.Label != label || metadata.Name != name {
					t.Errorf("metadata did not survive JSON escaping: %+v", metadata)
				}
				_, candidateText, ok := strings.Cut(req.User, "## Complete candidate / 完整候选\n")
				var candidate struct{ Name, Summary, Description string }
				if !ok || json.NewDecoder(strings.NewReader(candidateText)).Decode(&candidate) != nil || candidate.Name != name || candidate.Summary != input.Summary || candidate.Description != input.Description {
					t.Error("rejected generation replaced the last complete candidate")
				}
				_, feedback, hasFeedback := strings.Cut(req.User, "## Concrete semantic feedback / 具体反馈\n")
				if !hasFeedback || !strings.Contains(feedback, `"reason":"annual check omitted"`) {
					t.Error("semantic critique disappeared during malformed revision recovery")
				}
				if generations == 1 {
					return nodeWire(metadata.NodeID+" ["+label+"]", "invalid candidate", "must never be assessed")
				}
				if !strings.Contains(feedback, "node description: invalid alias/summary/description") {
					t.Error("actual format failure was not fed back to the failed generation")
				}
				return nodeWire(metadata.NodeID, "Both source rules.", input.Material)
			})
			out := FuseNodeDescriptions(context.Background(), client, 4096, 2, "operations", subdomain, []NodeDescriptionInput{input}).Nodes[0]
			if out.Outcome != annotation.DocumentComplete || out.Exhausted || generations != 2 || assessments != 2 || len(out.Attempts) != 4 || out.Description != input.Material {
				t.Fatalf("bounded revision did not complete: %+v; generations=%d assessments=%d", out, generations, assessments)
			}
			for i, step := range []string{"assess", "revise", "revise", "assess"} {
				if out.Attempts[i].Step != step {
					t.Fatalf("unexpected step sequence: %+v", out.Attempts)
				}
			}
			if out.Attempts[1].Outcome != "unavailable" || out.Attempts[1].CandidateID != out.Attempts[0].CandidateID || out.Attempts[2].CandidateID == out.Attempts[0].CandidateID {
				t.Fatalf("wrong ID was accepted or candidate identity changed before valid generation: %+v", out.Attempts)
			}
		})
	}
}

// 错误 ID 只能消耗失败节点自己的有限恢复，不能进入核验或重跑已完成兄弟。
// Wrong aliases exhaust locally; an accepted sibling is retained without regeneration.
func TestNodeAliasWireMalformedExhaustionKeepsSibling(t *testing.T) {
	t.Parallel()
	inputs := []NodeDescriptionInput{
		{ID: "stable-good", Label: "Entity", Name: "Good [Concept] \"绿\"", Material: "Good preserves an independent fact."},
		{ID: "stable-bad", Label: "Concept", Name: "Bad [Entity] \"红\"", Material: "Bad has its own source fact."},
	}
	counts := map[string]int{}
	assessments := 0
	client := nodeAliasWireClient(t, func(req llm.CompleteRequest) string {
		if strings.Contains(req.System, "语义核验专家") {
			assessments++
			if v013SemanticCandidate(t, req).Name != inputs[0].Name {
				t.Error("malformed node reached assessment")
			}
			return nodeSupported
		}
		metadata, err := readNodeWireMetadata(req.User)
		if err != nil {
			t.Error(err)
			return nodeWire("legacy heading [Entity]", "invalid", "invalid")
		}
		counts[metadata.Name]++
		if metadata.Name == inputs[1].Name {
			return nodeWire(metadata.NodeID+" ["+metadata.Label+"]", "invalid", "invalid")
		}
		return nodeWire(metadata.NodeID, "Independent fact.", inputs[0].Material)
	})
	out := FuseNodeDescriptions(context.Background(), client, 4096, 1, "operations", " space [Entity] \"Ω\" ", inputs)
	if len(out.Nodes) != 2 || out.Nodes[0].Outcome != annotation.DocumentComplete || out.Nodes[1].Outcome != annotation.DocumentUnavailable || !out.Nodes[1].Exhausted || counts[inputs[0].Name] != 1 || counts[inputs[1].Name] != 2 || assessments != 1 {
		t.Fatalf("local exhaustion or sibling retention failed: %+v; counts=%v assessments=%d", out, counts, assessments)
	}
	if len(out.Nodes[1].Attempts) != 2 || out.Nodes[1].CandidateID != "" || out.Nodes[1].Description != "" {
		t.Fatal("wrong-ID candidate was admitted or recovery allowance reset")
	}
	for _, attempt := range out.Nodes[1].Attempts {
		if attempt.Step != "generate" || attempt.CandidateID != "" || attempt.Outcome != "unavailable" {
			t.Fatalf("invalid candidate crossed generation boundary: %+v", attempt)
		}
	}
}
