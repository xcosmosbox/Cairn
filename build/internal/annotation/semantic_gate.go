package annotation

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
)

// DefaultSemanticGateMaxRetries 是语义门 LLM 核验调用的默认原地重试上限。
// DefaultSemanticGateMaxRetries is the default in-place retry cap for the Gate B call.
const DefaultSemanticGateMaxRetries = 3

// SemanticGate 是双门校验的第二道门（Gate B，LLM 驱动）：核验「原始文档」与
// 「标注出的 entity/concept」在意思层面是否幂等——即标注是否忠实、无损、无臆造地
// 表达了原文知识。语义等价无法用纯规则判定，只能交由 LLM 核验。
//
// 重试语义（对齐契约第 5 点）：
//   - 核验调用本身失败 / 返回非法 JSON → 以全新 session「原地重试」（这是 LLM 调用）。
//   - 核验判定「不幂等」→ 返回包裹 ErrRollbackToAnnotate 的错误，回退标注。
//   - 原地重试耗尽仍无法核验 → 保守回退标注（无法确认忠实性时，宁可重标注）。
//
// SemanticGate is the second gate (Gate B, LLM-driven): it verifies that the
// annotated entity/concept items are semantically idempotent with the original
// document — faithful, lossless, and free of fabrication. A call/JSON failure is
// retried in place; a "not idempotent" verdict (or exhausted retries) rolls back to
// annotation via ErrRollbackToAnnotate.
type SemanticGate struct {
	client     llm.Client
	maxTokens  int
	maxRetries int
}

// NewSemanticGate 创建一个 SemanticGate。client 不可为 nil；maxRetries ≤0 取默认值。
// NewSemanticGate creates a SemanticGate; maxRetries ≤ 0 falls back to the default.
func NewSemanticGate(client llm.Client, maxTokens, maxRetries int) *SemanticGate {
	if maxRetries <= 0 {
		maxRetries = DefaultSemanticGateMaxRetries
	}
	return &SemanticGate{client: client, maxTokens: maxTokens, maxRetries: maxRetries}
}

// Check 核验标注文档与原文是否语义幂等。
//   - 通过 → 返回 nil。
//   - 不幂等 / 无法核验 → 返回包裹 ErrRollbackToAnnotate 的错误。
//   - 无 items（纯叙述文档）→ 无需核验，直接通过。
//
// Check verifies semantic idempotency; returns nil on pass, or an error wrapping
// ErrRollbackToAnnotate when not idempotent or unverifiable. An empty document passes.
func (g *SemanticGate) Check(ctx context.Context, originalContent string, doc *dktypes.AnnotatedDocument) error {
	if g.client == nil {
		return fmt.Errorf("semantic gate: llm client is nil")
	}
	if doc == nil || len(doc.Items) == 0 {
		return nil
	}

	user, err := buildSemanticUserMessage(originalContent, doc)
	if err != nil {
		return fmt.Errorf("semantic gate: 组装核验消息: %w", err)
	}

	var lastErr error
	for attempt := 1; attempt <= g.maxRetries; attempt++ {
		resp, cerr := g.client.Complete(ctx, llm.CompleteRequest{
			System:    semanticSystemPrompt,
			User:      user,
			MaxTokens: g.maxTokens,
		})
		if cerr != nil {
			lastErr = fmt.Errorf("第 %d 次核验调用: %w", attempt, cerr)
			log.Printf("[semantic-gate] %s 第 %d/%d 次调用失败，原地重试: %v", doc.FilePath, attempt, g.maxRetries, cerr)
			continue
		}

		verdict, perr := parseSemanticResponse(resp.Text)
		if perr != nil {
			lastErr = fmt.Errorf("第 %d 次核验解析: %w", attempt, perr)
			log.Printf("[semantic-gate] %s 第 %d/%d 次解析失败，原地重试: %v", doc.FilePath, attempt, g.maxRetries, perr)
			continue
		}

		if !verdict.Idempotent {
			// 语义不幂等 → 回退标注。
			return fmt.Errorf("semantic gate: %s 标注与原文语义不幂等: %s: %w",
				doc.FilePath, verdict.Reason, ErrRollbackToAnnotate)
		}
		return nil // 通过 / pass
	}

	// 原地重试耗尽仍无法核验 → 保守回退重标注。
	return fmt.Errorf("semantic gate: %s 原地重试 %d 次仍无法核验(%v)，保守回退: %w",
		doc.FilePath, g.maxRetries, lastErr, ErrRollbackToAnnotate)
}

// ——————————————————————————————————————————————————————————————————————————————
// 输入组装 / Input assembly
// ——————————————————————————————————————————————————————————————————————————————

// buildSemanticUserMessage 组装核验用的 user 消息：原始文档全文 + 标注出的 items（JSON）。
// buildSemanticUserMessage assembles the verification message: the original document
// plus the annotated items (as JSON).
func buildSemanticUserMessage(originalContent string, doc *dktypes.AnnotatedDocument) (string, error) {
	itemsJSON, err := json.MarshalIndent(doc.Items, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal items: %w", err)
	}

	var sb strings.Builder
	sb.WriteString("# 语义幂等核验任务\n\n")
	sb.WriteString("## 原始文档 / original document\n")
	sb.WriteString(originalContent)
	if !strings.HasSuffix(originalContent, "\n") {
		sb.WriteString("\n")
	}
	sb.WriteString("\n## 标注产出的 entity/concept 单元 / annotated items (JSON)\n")
	sb.Write(itemsJSON)
	sb.WriteString("\n")
	return sb.String(), nil
}

// semanticSystemPrompt 是语义门的 system 提示。要求 LLM 判断标注是否忠实、无损、
// 无臆造地表达了原文知识，并严格返回 JSON（含 "JSON" 字样以满足 JSON mode）。
//
// semanticSystemPrompt is the Gate B system instruction, asking whether the
// annotation faithfully and losslessly represents the source without fabrication,
// returning strict JSON.
const semanticSystemPrompt = `你是知识标注的语义核验专家。用户会给你一篇「原始文档」，` +
	`以及从该文档标注出的一组 entity/concept 单元（JSON）。请判断这组标注与原始文档在意思层面是否「幂等」。

「语义幂等」意味着同时满足：
1. 忠实：每个单元的 name/content 都能在原文中找到依据，没有臆造或曲解。
2. 无损：原文中重要的 entity / concept 都已被覆盖，没有明显遗漏。
3. 分类合理：entity（有边界的具体事物）与 concept（抽象概念/规则/方法）的划分基本正确。

严格要求：
1. 只返回一个 JSON 对象（JSON 格式），不要输出任何解释性文字、Markdown 代码围栏或前后缀。
2. idempotent 为布尔值：完全满足上述三条时为 true，否则为 false。
3. reason 用简短中文说明判断依据；若为 false，请指出具体的臆造 / 遗漏 / 分类错误。

返回的 JSON 必须严格符合以下样例结构（字段名保持一致）：
{
  "idempotent": true,
  "reason": "标注忠实覆盖了原文的实体与概念，分类合理。"
}`

// ——————————————————————————————————————————————————————————————————————————————
// 解析 / Parsing
// ——————————————————————————————————————————————————————————————————————————————

// semanticVerdict 是语义门 LLM 返回 JSON 的结构。
// semanticVerdict is the structure of the Gate B LLM JSON.
type semanticVerdict struct {
	Idempotent bool   `json:"idempotent"`
	Reason     string `json:"reason"`
}

// parseSemanticResponse 解析核验 LLM 的 JSON 响应。JSON 非法 / 空响应 → 返回 error（触发原地重试）。
// parseSemanticResponse parses the Gate B JSON response; invalid JSON triggers a retry.
func parseSemanticResponse(text string) (*semanticVerdict, error) {
	trimmed := stripJSONFence(strings.TrimSpace(text))
	if trimmed == "" {
		return nil, fmt.Errorf("空响应")
	}
	var v semanticVerdict
	if err := json.Unmarshal([]byte(trimmed), &v); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return &v, nil
}
