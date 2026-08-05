// Package extract 的本文件实现「relation 补充轮」（重构第二期收尾）。
//
// 提取阶段（融合映射）刻意弱化了关系识别，导致 relations 普遍为空。本阶段在 description
// 融合之后、ingest 之前，以 **subdomain 为最小并发单元**，专门识别节点间的语义关系：
//
//   - 最小并发单元 = subdomain：relation 的端点在现有架构中就限定在同一 subdomain 内解析
//     （ingest 用 BuildNodeID(dSlug,sSlug,id) 定位），故 subdomain 是天然、无干扰的判定视角；
//   - 每个 subdomain 一次 LLM 调用，输入仅该 subdomain 的 domain 上下文 + 其内全部融合节点
//     （含已重写的 description），在此隔离视角内判定关系，**允许返回空**（确无关系）；
//   - 返回的是「关系操作」：source/target 必须是本 subdomain 内的节点 id，kind 必须是既有
//     6 枚举之一——**kind 越界则原地重试**；端点不在本 subdomain 的条目直接过滤（绝不产生悬空）。
//
// This file implements the relation-identification pass: per subdomain (the minimal,
// interference-free unit), ask the LLM to detect semantic relations among the fusion
// nodes, retrying in place on an out-of-enum kind and filtering out-of-scope endpoints.
package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/parallel"
	"github.com/xcosmosbox/cairn/build/internal/llm"
)

// maxRelateRounds 是单个 subdomain relation 识别的原地重试上限（kind 越界 / JSON 非法时重试）。
const maxRelateRounds = 2

// RelateReport 是 relation 补充阶段的结果摘要（可观测，写盘到 05e）。
// RelateReport summarizes the relation-identification stage (written to 05e).
type RelateReport struct {
	SubdomainsTotal int      // 参与的 subdomain 总数（节点数 ≥ 2 才参与）
	SubdomainsOK    int      // 成功返回并解析的 subdomain 数
	RelationsAdded  int      // 新增的合法 relation 总数
	InvalidDropped  int      // 因端点越界 / 自环而被过滤的 relation 数
	Failures        []string // subdomain 级失败原因
}

// IdentifyRelations 按 subdomain 并发识别节点间语义关系并写回 sd.Relations（就地修改 res）。
// 不返回 error（单 subdomain 失败降级，不阻断流水线）。
//
// IdentifyRelations detects semantic relations per subdomain and writes them back.
func (e *Extractor) IdentifyRelations(ctx context.Context, res *Result) *RelateReport {
	rpt := &RelateReport{}
	if res == nil || len(res.Domains) == 0 {
		return rpt
	}

	type sdTask struct {
		domainName string
		sd         *Subdomain
	}
	var tasks []sdTask
	for di := range res.Domains {
		d := &res.Domains[di]
		for si := range d.Subdomains {
			tasks = append(tasks, sdTask{domainName: d.Name, sd: &d.Subdomains[si]})
		}
	}

	results := make([]sdRelateResult, len(tasks))
	parallel.ForEachIndexed(parallel.MaxConcurrency, tasks, func(i int, t sdTask) {
		results[i] = e.relateSubdomain(ctx, t.domainName, t.sd)
	})

	for _, r := range results {
		if r.participated {
			rpt.SubdomainsTotal++
			if r.ok {
				rpt.SubdomainsOK++
			}
		}
		rpt.RelationsAdded += r.added
		rpt.InvalidDropped += r.dropped
		if r.failure != "" {
			rpt.Failures = append(rpt.Failures, r.failure)
		}
	}
	log.Printf("[extract-relate] relation 识别完成：subdomain %d/%d 成功，新增关系 %d，过滤越界/自环 %d",
		rpt.SubdomainsOK, rpt.SubdomainsTotal, rpt.RelationsAdded, rpt.InvalidDropped)
	return rpt
}

// sdRelateResult 是单个 subdomain relation 识别的结果（供并发汇总）。
type sdRelateResult struct {
	participated bool
	ok           bool
	added        int
	dropped      int
	failure      string
}

// relateSubdomain 在单个 subdomain 的隔离视角内识别节点间关系并写回。
// 节点数 < 2 时无关系可言，直接跳过（不消耗 LLM）。
func (e *Extractor) relateSubdomain(ctx context.Context, domainName string, sd *Subdomain) sdRelateResult {
	res := sdRelateResult{}

	nodes := collectRelateNodes(sd)
	if len(nodes) < 2 {
		res.ok = true // 少于 2 个节点，无关系可判定
		return res
	}
	res.participated = true

	declared := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		declared[n.ID] = true
	}

	var lastErr string
	for round := 1; round <= maxRelateRounds; round++ {
		userMsg := buildRelatePrompt(domainName, sd.Name, sd.Summary, nodes, lastErr)
		resp, err := e.client.Complete(ctx, llm.CompleteRequest{
			System:    relateSystemPrompt,
			User:      userMsg,
			MaxTokens: e.maxTokens,
		})
		if err != nil {
			res.failure = fmt.Sprintf("subdomain %q relation 识别 LLM 调用失败: %v", sd.Name, err)
			return res
		}

		var out relateOutput
		if perr := json.Unmarshal([]byte(stripCodeFence(strings.TrimSpace(resp.Text))), &out); perr != nil {
			lastErr = fmt.Sprintf("上一轮返回非法 JSON: %v", perr)
			continue
		}

		valid, dropped, hasInvalidKind := validateRelations(out.Relations, declared)
		// kind 越界必须原地重试（除非已是最后一轮，则用已过滤的合法部分降级）。
		if hasInvalidKind && round < maxRelateRounds {
			lastErr = "存在越界的 kind（必须是 6 个枚举之一），请修正后重试"
			continue
		}

		sd.Relations = appendDedupRelations(sd.Relations, valid)
		res.added = len(valid)
		res.dropped = dropped
		res.ok = true
		return res
	}
	res.failure = fmt.Sprintf("subdomain %q relation 识别 %d 轮仍未通过", sd.Name, maxRelateRounds)
	return res
}

// relateNode 是投喂给 relation 识别 LLM 的单个节点。
type relateNode struct {
	ID    string
	Label string
	Name  string
	Body  string // 节点详述：完整 description 全文（不回退 summary、不截断），供关系判定
}

// collectRelateNodes 汇总 subdomain 内全部融合节点（entity + concept）的展示信息，
// 节点详述统一用完整 description：
//   - 关系判定的信息基础必须是完整详述（describe 阶段已按 members 的 04 detail 融合重写好），
//     summary 过于简短、不足以支撑准确的关系判定，故不使用、也不作回退；
//   - 不截断：单个 subdomain 的全部 description 远小于 LLM 上下文窗口，完整投喂避免信息损失。
//
// 说明：detail 强制非空 + 每个融合节点均有 members，故 describe 后节点 description 常态非空；
// 极少数无素材节点 description 可能为空，此时 Body 为空——该节点仍作为潜在关系端点参与判定，
// 只是不额外提供详述文本。
//
// collectRelateNodes gathers all fusion nodes in a subdomain, using each node's full
// description (no summary fallback, no truncation) as the body for relation judgement.
func collectRelateNodes(sd *Subdomain) []relateNode {
	var out []relateNode
	add := func(nodes []Node, label string) {
		for _, n := range nodes {
			out = append(out, relateNode{ID: n.ID, Label: label, Name: n.Name, Body: strings.TrimSpace(n.Description)})
		}
	}
	add(sd.Entities, string(dktypes.LabelEntity))
	add(sd.Concepts, string(dktypes.LabelConcept))
	return out
}

// validateRelations 校验 LLM 返回的关系：
//   - kind 必须是 6 枚举之一，否则标记 hasInvalidKind（触发重试）并丢弃该条；
//   - source/target 必须都是本 subdomain 内已声明节点，否则过滤（计入 dropped，绝不产生悬空）；
//   - 过滤自环。
//
// validateRelations validates relations against the declared node set and the kind enum.
func validateRelations(rels []Relation, declared map[string]bool) (valid []Relation, dropped int, hasInvalidKind bool) {
	for _, r := range rels {
		if !validRelationKinds[r.Kind] {
			hasInvalidKind = true
			continue
		}
		if strings.TrimSpace(r.Source) == "" || strings.TrimSpace(r.Target) == "" ||
			!declared[r.Source] || !declared[r.Target] || r.Source == r.Target {
			dropped++
			continue
		}
		valid = append(valid, r)
	}
	return valid, dropped, hasInvalidKind
}

// appendDedupRelations 把新关系追加到已有关系，按 (source,target,kind) 去重。
func appendDedupRelations(existing, added []Relation) []Relation {
	seen := make(map[string]bool, len(existing)+len(added))
	key := func(r Relation) string { return r.Source + "\x00" + r.Target + "\x00" + r.Kind }
	for _, r := range existing {
		seen[key(r)] = true
	}
	out := existing
	for _, r := range added {
		k := key(r)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, r)
	}
	return out
}

// ——————————————————————————————————————————————————————————————————————————————
// prompt 与解析 / Prompt and parsing
// ——————————————————————————————————————————————————————————————————————————————

// relateOutput 是 relation 识别 LLM 返回的 JSON（复用中间 Relation 结构）。
type relateOutput struct {
	Relations []Relation `json:"relations"`
}

// relateSystemPrompt 是 relation 识别阶段的 system 提示。
const relateSystemPrompt = `你是领域知识关系识别专家。用户会给你**一个子域**内的全部融合节点（实体 / 概念）及其详述。
请只在这些节点之间，识别真实存在的语义关系。这是一个隔离的判定视角：只看给定节点，不要引入任何外部节点。

严格要求：
1. 只返回一个 JSON 对象（json 格式），形如 {"relations":[{"source":"节点id","target":"节点id","kind":"...","description":"...","confidence":0.x}]}，
   不要输出任何解释性文字或代码围栏。
2. source 与 target 必须是用户给定节点列表中的 node_id，逐字引用，不得编造、不得指向列表之外的节点。
3. kind 只能取以下 6 个语义枚举之一，绝不允许自造或使用其它词：
   "triggers"、"depends_on"、"references"、"generalizes"、"composes"、"contradicts"。
4. 只识别**真实、明确**的关系；如果这些节点之间确实没有语义关系，就返回空数组 {"relations":[]}——这是完全允许且正常的。
5. 不要输出层级 / 归属类关系（那由结构自动物化），只表达节点之间的语义关联。`

// buildRelatePrompt 组装 relation 识别的 user 消息（单 subdomain 隔离视角）。
// prevErr 非空时作为上一轮的纠错反馈附在末尾（伪 session 原地重试）。
func buildRelatePrompt(domainName, subName, subSummary string, nodes []relateNode, prevErr string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "## 子域：%s（所属领域：%s）\n", subName, domainName)
	if strings.TrimSpace(subSummary) != "" {
		fmt.Fprintf(&sb, "子域说明：%s\n", subSummary)
	}
	sb.WriteString("\n### 本子域内的融合节点（只在它们之间判定关系）\n")
	for _, n := range nodes {
		fmt.Fprintf(&sb, "- node_id=%s [%s]：%s\n", n.ID, n.Label, n.Name)
		if n.Body != "" {
			fmt.Fprintf(&sb, "  详述（完整 description）：%s\n", n.Body)
		}
	}
	sb.WriteString("\n请识别上述节点之间真实的语义关系（可为空），只返回 relations JSON。\n")
	if strings.TrimSpace(prevErr) != "" {
		fmt.Fprintf(&sb, "\n## 上一轮的问题（请修正后重试）\n- %s\n", prevErr)
	}
	return sb.String()
}
