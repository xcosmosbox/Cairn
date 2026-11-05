package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/llm"
)

type writebackFixtureLLM struct{}

func (writebackFixtureLLM) ProviderName() string { return "fixture" }
func (writebackFixtureLLM) Complete(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
	var text string
	switch {
	case strings.Contains(req.System, "知识结构化标注专家"):
		text = `{"items":[{"tag":"concept","name":"幂等键","content":"通过幂等键标识请求。","detail":"通过幂等键标识请求，服务端识别重试并复用同一结果，避免重复处理造成数据副作用。","confidence":0.95},{"tag":"concept","name":"服务端","content":"服务端识别重试并复用同一结果，避免重复处理造成数据副作用。","detail":"服务端识别重试并复用同一结果，避免重复处理造成数据副作用。","confidence":0.95}]}`
	case strings.Contains(req.System, "语义核验专家"):
		if strings.Contains(req.User, "## Existing machine description / 现有机器详述") {
			text = `{"supported":true,"reason":"node material retained","unsupported_items":[],"omissions":[]}`
		} else {
			b, _ := json.Marshal(entrySubjectVerdict(entrySubjectRow("通过幂等键标识请求", "幂等键", 0, "幂等键", true, true), entrySubjectRow("服务端识别重试并复用同一结果，避免重复处理造成数据副作用。", "服务端", 1, "服务端", true, true)))
			text = string(b)
		}
	case strings.Contains(req.System, "领域知识融合专家"):
		matches := regexp.MustCompile(`(?m)^- id=([^\s]+)`).FindAllStringSubmatch(req.User, -1)
		if len(matches) != 2 {
			return nil, fmt.Errorf("expected key and server carriers, got %d", len(matches))
		}
		concepts := []map[string]any{}
		for i, m := range matches {
			name, id := "幂等键", "idempotency-key"
			if i == 1 {
				name, id = "服务端", "server"
			}
			concepts = append(concepts, map[string]any{"id": id, "label": "Concept", "name": name, "summary": name, "confidence": .95, "members": []string{m[1]}})
		}
		b, _ := json.Marshal(map[string]any{"domains": []any{map[string]any{"name": "工程", "summary": "工程知识", "reusability_rationale": "可复用", "subdomains": []any{map[string]any{"name": "可靠性", "summary": "可靠性方法", "concepts": concepts, "entities": []any{}, "relations": []any{}}}}}})
		text = string(b)
	case strings.Contains(req.System, "知识融合重写专家"):
		return pipelineNodeDescriptionResponse(req, "通过幂等键标识请求，服务端识别重试并复用同一结果，避免重复处理造成数据副作用。")
	default:
		text = "idempotency-key"
	}
	return &llm.CompleteResponse{Text: text, FinishReason: "stop"}, nil
}

// 原先全量 pipeline 只日志告警回写失败并返回 nil，controller 可发布半同步产物。
func TestFullPipelineRejectsPartialWritebackAndPreservesOriginal(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("---\nname: test-skill\ndescription: reliability\n---\n# Skill\n"), 0o644)
	os.MkdirAll(filepath.Join(root, "references"), 0o755)
	doc := filepath.Join(root, "references", "doc.md")
	original := "通过幂等键标识请求，服务端识别重试并复用同一结果，避免重复处理造成数据副作用。\n"
	os.WriteFile(doc, []byte(original), 0o644)
	os.Mkdir(doc+".kg.yaml", 0o755) // deterministic per-document output fault after successful ingest
	orch, err := NewOrchestrator(Options{Client: writebackFixtureLLM{}, MaxRetries: 1, MaxRollbacks: 1, MinConfidence: 0.8, RepositoryIdentity: "local:fixture"})
	if err != nil {
		t.Fatal(err)
	}
	report, err := orch.RunFullRebuild(context.Background(), root, filepath.Join(root, "candidate.db"))
	if err == nil || !strings.Contains(err.Error(), "writeback incomplete") {
		t.Fatalf("partial output accepted: report=%+v err=%v", report, err)
	}
	if report == nil || report.Ingest == nil || report.Ingest.NodesInserted < 4 {
		t.Fatalf("ingest stage not exercised: %+v", report)
	}
	actual, _ := os.ReadFile(doc)
	if string(actual) != original {
		t.Fatal("failed output overwrote source markdown")
	}
}
