// Package annotation produces one source-bound annotation per document.
package annotation

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"log"
	"strings"
)

// Constructors retain their previous signature; automatic model retries are retired.
const DefaultAnnotateMaxRetries = 1

type LLMAnnotator struct {
	client    llm.Client
	maxTokens int
}

func NewLLMAnnotator(client llm.Client, maxTokens, _ int) *LLMAnnotator {
	return &LLMAnnotator{client: client, maxTokens: maxTokens}
}
func (a *LLMAnnotator) Annotate(ctx context.Context, skill, path, content string) (*dktypes.AnnotatedDocument, error) {
	if a.client == nil {
		return nil, fmt.Errorf("annotate: llm client is nil")
	}
	if strings.TrimSpace(content) == "" {
		return &dktypes.AnnotatedDocument{Skill: skill, FilePath: path}, nil
	}

	user := buildAnnotateUserMessage(path, content)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resp, err := a.client.Complete(ctx, llm.CompleteRequest{System: annotateSystemPrompt, User: user, MaxTokens: a.maxTokens})
	if err != nil {
		return nil, fmt.Errorf("annotate %s: %w", path, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if resp == nil || resp.FinishReason == "length" || resp.FinishReason == "max_tokens" {
		return nil, fmt.Errorf("annotate %s: missing or incomplete response", path)
	}
	return parseAnnotateResponse(skill, path, resp.Text)
}

// ——————————————————————————————————————————————————————————————————————————————
// 输入组装 / Input assembly
// ——————————————————————————————————————————————————————————————————————————————

// buildAnnotateUserMessage 把单篇文档拼装为 user 消息。刻意只带本文档内容与其路径，
// 不注入任何跨文档上下文，保证标注阶段的文档隔离。
//
// buildAnnotateUserMessage assembles a single document into the user message,
// carrying only this document's content and path to keep documents isolated.
func buildAnnotateUserMessage(path, content string) string {
	var sb strings.Builder
	sb.WriteString("# 待标注文档\n\n")
	fmt.Fprintf(&sb, "文档路径 / path: %s\n\n", path)
	sb.WriteString("文档内容 / content:\n")
	sb.WriteString(content)
	if !strings.HasSuffix(content, "\n") {
		sb.WriteString("\n")
	}
	return sb.String()
}

// annotateSystemPrompt 是标注任务的 system 提示。
// 要点：只输出 entity/concept；不判断 domain/subdomain；不区分 public/private；
// content 要求「意思层面幂等」；强制严格 JSON（含 "JSON" 字样与样例，满足 JSON mode）。
//
// annotateSystemPrompt is the annotation system instruction: emit only
// entity/concept, no domain/subdomain, no public/private, idempotent content,
// strict JSON (the word "JSON" and a sample are present for JSON mode).
const annotateSystemPrompt = `你是知识结构化标注专家。用户会给你「单篇」reference 文档，` +
	`请把其中离散的自然语言表述，梳理为标准化的知识单元。每个单元只能是以下两类之一：

- entity（实体）：文档描述的、有明确边界的具体事物，如对象、组件、系统、角色、表、接口等。
- concept（概念）：文档描述的抽象概念、规则、方法、约束、流程等。

严格要求：
1. 只输出一个 JSON 对象（JSON 格式），不要输出任何解释性文字、Markdown 代码围栏或前后缀。
2. 只区分 entity 与 concept 两类。绝对不要判断它们属于什么领域（domain）或子域（subdomain）——那不是本阶段的职责。
3. 不要区分公开 / 私有（public / private）。
4. 每个单元的 name 使用标准化、具体、无歧义的名称。每个单元必须同时给出 content 与 detail 两个字段：
   - content：提炼性摘要，简短（通常 1-2 句），供后续领域归纳阶段快速理解使用。
   - detail：详述，在语义幂等前提下尽可能保留原文的长度与细节，不得大幅缩短，主要供后续查询端消费。
     detail 可补充有原文依据的条件和细节，不设长度或完整覆盖要求；没有补充时可为空。
   两者都要求「意思层面幂等」：对同样意思的原文，应产出语义等价的 content 与 detail。
5. 只依据本文档内容进行梳理，不要臆测文档之外的信息，也不要跨文档关联。
6. confidence 取 0.0~1.0，表示你对该单元判定的把握程度。

返回的 JSON 必须严格符合以下样例结构（字段名保持一致）：
{
  "items": [
    {
      "tag": "entity",
      "name": "订单聚合根",
      "content": "管理订单生命周期的聚合根，负责订单状态流转与一致性边界。",
      "detail": "订单聚合根是管理订单完整生命周期的领域聚合根，负责订单创建、状态流转、取消与完成等操作的一致性维护。它聚合订单行、收货地址、支付信息等值对象，作为事务一致性边界，通过领域事件对外通知状态变更，确保同一事务内的数据一致。",
      "confidence": 0.9
    },
    {
      "tag": "concept",
      "name": "幂等键",
      "content": "用于保证同一请求重复提交时只被处理一次的唯一标识。",
      "detail": "幂等键是一种用于保证接口幂等性的唯一标识：客户端为每次业务请求生成唯一键值，服务端据此识别重复提交，对同一幂等键的多次请求只执行一次实际处理并返回相同结果，从而在网络重试、超时重发等场景下避免重复扣款、重复下单等副作用。",
      "confidence": 0.85
    }
  ]
}`

// ——————————————————————————————————————————————————————————————————————————————
// 解析与校验 / Parsing and validation
// ——————————————————————————————————————————————————————————————————————————————

// parseAnnotateResponse 解析 LLM 返回的 JSON 为 AnnotatedDocument。
//   - JSON 非法 / 空响应 → 返回 error（触发原地重试）。
//   - 单条 tag 非法或缺 name/content → 跳过该条并记录日志（不整体失败）。
//
// parseAnnotateResponse parses the LLM JSON into an AnnotatedDocument. Invalid JSON
// triggers a retry; structurally invalid items are dropped with a log line.
func parseAnnotateResponse(skill, path, text string) (*dktypes.AnnotatedDocument, error) {
	trimmed := stripJSONFence(strings.TrimSpace(text))
	if trimmed == "" {
		return nil, fmt.Errorf("空响应")
	}

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &envelope); err != nil {
		return nil, fmt.Errorf("annotation JSON: %w", err)
	}
	rawItems, ok := envelope["items"]
	if !ok || len(rawItems) == 0 || rawItems[0] != '[' {
		return nil, fmt.Errorf("annotation: missing items array")
	}
	var rows []json.RawMessage
	if err := json.Unmarshal(rawItems, &rows); err != nil {
		return nil, err
	}
	doc := &dktypes.AnnotatedDocument{Skill: skill, FilePath: path}
	for i, raw := range rows {
		var it struct {
			Tag        string   `json:"tag"`
			Name       string   `json:"name"`
			Content    string   `json:"content"`
			Detail     string   `json:"detail"`
			Confidence *float64 `json:"confidence"`
		}
		if err := json.Unmarshal(raw, &it); err != nil || it.Confidence == nil {
			log.Printf("[annotate] rejected malformed item %d in %s", i, path)
			continue
		}
		tag := dktypes.AnnotationTag(strings.ToLower(strings.TrimSpace(it.Tag)))
		name := strings.TrimSpace(it.Name)
		content := strings.TrimSpace(it.Content)
		detail := strings.TrimSpace(it.Detail)

		if !tag.IsValid() {
			log.Printf("[annotate] %s 跳过非法 tag=%q（name=%q）", path, it.Tag, name)
			continue
		}
		if name == "" || content == "" {
			log.Printf("[annotate] %s 跳过缺 name/content 的条目（tag=%s name=%q）", path, tag, name)
			continue
		}
		// detail 不在此兜底：为空的 detail 会被 Gate A 捕获并触发整篇回退重标注（不做向后兼容）。
		doc.Items = append(doc.Items, dktypes.AnnotatedItem{
			Tag:        tag,
			Name:       name,
			Content:    content,
			Detail:     detail,
			Confidence: *it.Confidence,
		})
	}
	if len(rows) > 0 && len(doc.Items) == 0 {
		return nil, fmt.Errorf("annotation: no valid items in nonempty output")
	}
	return doc, nil
}

// stripJSONFence 去除 LLM 偶尔包裹的 ```json ... ``` 代码围栏（JSON mode 下通常没有，
// 作为容错保留）。仅当整体被围栏包裹时才剥离。
//
// stripJSONFence removes an optional ```json fence the LLM may wrap around output.
func stripJSONFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = s[idx+1:]
	}
	s = strings.TrimSuffix(strings.TrimRight(s, "\n"), "```")
	return strings.TrimSpace(s)
}
