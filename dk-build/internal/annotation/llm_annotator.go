// Package annotation 提供 reference 文档的标注能力。
//
// 本文件实现「LLM 驱动的 per-doc 标注器」（LLMAnnotator），取代旧的规则匹配标注器
// （annotator.go 的启发式正则）。它是新 LLM 流水线「标注阶段」的核心：
//
//	对每一篇 reference/*.md 文档，独立发起一次 LLM 调用，要求 LLM 把人类离散的
//	自然语言，梳理为标准化的 entity / concept 单元（严格 JSON 返回）。
//
// 与旧标注器的根本差异：
//   - LLM 驱动而非正则：能理解语义，而非按标题层级机械映射。
//   - 只标注 entity / concept，不判断 domain / subdomain 归属（那是提取阶段的职责）。
//   - 不区分 public / private —— 可见性概念已从全系统移除。
//   - 文档间完全隔离：1 文档 = 1 次 LLM 调用，互不影响。
//   - 原地重试：LLM 调用失败或返回非法 JSON 时，以全新 session 原地重试（契约：
//     标注失败不终止流水线，而由原地重试保证鲁棒性）。
//
// This file implements the LLM-driven per-document annotator (LLMAnnotator),
// replacing the old rule-based annotator. For each reference/*.md file it makes an
// isolated LLM call asking the model to distill discrete human prose into
// standardized entity/concept items (strict JSON). It annotates only
// entity/concept (no domain/subdomain, no public/private), keeps documents fully
// isolated (1 doc = 1 call), and retries in place with a fresh session on a hard
// call failure or invalid JSON.
package annotation

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/xcosmosbox/domain-knowledge-layer/dk-build/internal/llm"
	"github.com/xcosmosbox/domain-knowledge-layer/core/dktypes"
)

// DefaultAnnotateMaxRetries 是单篇文档标注的默认原地重试上限。
// DefaultAnnotateMaxRetries is the default in-place retry cap for annotating one doc.
const DefaultAnnotateMaxRetries = 3

// LLMAnnotator 是 LLM 驱动的 per-doc 标注器。llm.Client 通过依赖注入传入（便于 mock）。
// LLMAnnotator is the LLM-driven per-document annotator; the llm.Client is injected.
type LLMAnnotator struct {
	client     llm.Client
	maxTokens  int // 单次请求 max_tokens；≤0 交由 client 默认。
	maxRetries int // 单篇文档的原地重试上限。
}

// NewLLMAnnotator 创建一个 LLMAnnotator。client 不可为 nil；maxRetries ≤0 时取默认值。
// NewLLMAnnotator creates an LLMAnnotator. client must not be nil; maxRetries ≤ 0
// falls back to the default.
func NewLLMAnnotator(client llm.Client, maxTokens, maxRetries int) *LLMAnnotator {
	if maxRetries <= 0 {
		maxRetries = DefaultAnnotateMaxRetries
	}
	return &LLMAnnotator{client: client, maxTokens: maxTokens, maxRetries: maxRetries}
}

// Annotate 对单篇文档执行标注，返回结构化的 AnnotatedDocument。
//
// 行为：
//   - content 为空 → 直接返回空标注（不消耗 LLM 调用）。
//   - 否则发起 LLM 调用；调用失败或返回非法 JSON → 以全新 session 原地重试，
//     最多 maxRetries 次。全部耗尽仍失败才返回 error（由上层编排器决定降级 / 跳过）。
//   - JSON 合法但个别条目非法（tag 非 entity/concept、缺 name/content）→ 跳过该条目
//     并记录日志，不整体失败（严格的去重 / 命名规范由后续 Gate A 校验）。
//   - 文档合法但无任何 entity/concept（纯叙述性文档）→ 返回空 Items，不视为失败。
//
// Annotate annotates a single document into a structured AnnotatedDocument, retrying
// in place with a fresh session on a hard call failure or invalid JSON, up to
// maxRetries. Structurally invalid items are dropped (Gate A enforces strict rules);
// an empty result is acceptable (a purely narrative document).
func (a *LLMAnnotator) Annotate(ctx context.Context, skill, path, content string) (*dktypes.AnnotatedDocument, error) {
	if a.client == nil {
		return nil, fmt.Errorf("annotate: llm client is nil")
	}
	if strings.TrimSpace(content) == "" {
		return &dktypes.AnnotatedDocument{Skill: skill, FilePath: path}, nil
	}

	user := buildAnnotateUserMessage(path, content)

	var lastErr error
	for attempt := 1; attempt <= a.maxRetries; attempt++ {
		// 每次都是独立 Complete 调用，无历史累积（llm.Client 本就无会话状态），
		// 天然满足「全新 session 原地重试」。
		resp, err := a.client.Complete(ctx, llm.CompleteRequest{
			System:    annotateSystemPrompt,
			User:      user,
			MaxTokens: a.maxTokens,
		})
		if err != nil {
			lastErr = fmt.Errorf("第 %d 次 LLM 调用: %w", attempt, err)
			log.Printf("[annotate] %s 第 %d/%d 次调用失败，原地重试: %v", path, attempt, a.maxRetries, err)
			continue
		}

		doc, perr := parseAnnotateResponse(skill, path, resp.Text)
		if perr != nil {
			lastErr = fmt.Errorf("第 %d 次解析: %w", attempt, perr)
			log.Printf("[annotate] %s 第 %d/%d 次解析失败，原地重试: %v", path, attempt, a.maxRetries, perr)
			continue
		}
		return doc, nil
	}

	return nil, fmt.Errorf("annotate %s: 原地重试 %d 次后仍失败: %w", path, a.maxRetries, lastErr)
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
     detail 必须比 content 更长、更完整（content 是 detail 的浓缩），且绝不允许为空或省略——为空会导致整篇文档回退重标注。
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

// annotateOutput 是 LLM 返回 JSON 的顶层容器。
// annotateOutput is the top-level container of the LLM's JSON.
type annotateOutput struct {
	Items []struct {
		Tag        string  `json:"tag"`
		Name       string  `json:"name"`
		Content    string  `json:"content"`
		Detail     string  `json:"detail"`
		Confidence float64 `json:"confidence"`
	} `json:"items"`
}

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

	var out annotateOutput
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}

	doc := &dktypes.AnnotatedDocument{Skill: skill, FilePath: path}
	for i := range out.Items {
		it := out.Items[i]
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
			Confidence: it.Confidence,
		})
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
