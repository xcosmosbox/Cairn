// Package extract 的本文件实现第三期「description 融合重写」阶段。
//
// 第二期把提取改成「只输出融合映射（members），不写正文」，融合节点的 description 因此留空。
// 本阶段在悬空修正之后、ingest 之前，按 **subdomain** 为并发单元，为每个融合节点综合其
// members 对应的 04 原文 detail，用 LLM 重写出一段完整、去重、语义连贯的 description：
//
//   - 并发单元 = subdomain：一次 LLM 调用重写该 subdomain 内所有（有素材的）融合节点，
//     受全局并发上限（parallel.MaxConcurrency）约束；不同 subdomain 写各自节点，无数据竞争。
//   - 素材 = 节点 members 的 04 detail 聚合（按 04 id 收集所有来源文档的 detail，带来源标记）。
//     detail 的多源聚合正是在这里真正被消费（第二期刻意推迟到此）。
//   - 幂等与忠实：要求 LLM 只综合素材、去重合并、不臆造素材外内容；解析失败原地重试。
//
// This file implements phase-3 description fusion: per subdomain (bounded by the global
// concurrency cap), rewrite each fusion node's description by synthesizing the 04 details
// of its members. Detail aggregation by member id is finally consumed here.
package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/xcosmosbox/domain-knowledge-layer/core/dktypes"
	"github.com/xcosmosbox/domain-knowledge-layer/core/parallel"
	"github.com/xcosmosbox/domain-knowledge-layer/dk-build/internal/llm"
)

// maxDescribeRounds 是单个 subdomain description 融合的最大轮数：
// 首轮全量重写 + 后续针对「漏点 / 空 description」节点的伪 session 补写重试，直至全部补齐或耗尽。
const maxDescribeRounds = 3

// DescribeReport 是 description 融合阶段的结果摘要（可观测，写盘到 05d）。
// DescribeReport summarizes the description-fusion stage (written to 05d).
type DescribeReport struct {
	SubdomainsTotal int      // 参与的 subdomain 总数
	SubdomainsOK    int      // LLM 成功返回并解析的 subdomain 数
	NodesTotal      int      // 融合节点总数
	NodesWithDetail int      // 有素材（members 有 detail）的节点数
	NodesRewritten  int      // 被写入非空 description 的节点数
	Failures        []string // subdomain 级失败原因（可观测）
}

// ——————————————————————————————————————————————————————————————————————————————
// detail 聚合（按 04 id）/ Detail aggregation by 04 id
// ——————————————————————————————————————————————————————————————————————————————

// sourceDetail 是某个 04 单元来自某篇文档的一份 detail。
type sourceDetail struct {
	Path   string
	Detail string
}

// buildDetailIndex 构造 04 id → 该单元在各来源文档中的 detail 列表。
// per-document 结构下，同一 04 id 可能出现在同 skill 的多篇文档中，故值为列表（保留多源）。
//
// buildDetailIndex maps each 04 id to its details across source documents.
func buildDetailIndex(docs []*dktypes.AnnotatedDocument) map[string][]sourceDetail {
	idx := make(map[string][]sourceDetail)
	for _, d := range docs {
		if d == nil {
			continue
		}
		for _, it := range d.Items {
			if it.ID == "" || strings.TrimSpace(it.Detail) == "" {
				continue
			}
			idx[it.ID] = append(idx[it.ID], sourceDetail{Path: d.FilePath, Detail: strings.TrimSpace(it.Detail)})
		}
	}
	return idx
}

// aggregateMemberDetails 把一个融合节点 members 对应的多源 04 detail 聚合为带来源标记的素材文本。
// 无任何 detail 时返回空串（调用方据此跳过该节点）。
//
// aggregateMemberDetails concatenates the multi-source 04 details of a node's members.
func aggregateMemberDetails(members []string, idx map[string][]sourceDetail) string {
	var sb strings.Builder
	for _, m := range members {
		for _, sd := range idx[m] {
			path := sd.Path
			if strings.TrimSpace(path) == "" {
				path = "(unknown)"
			}
			fmt.Fprintf(&sb, "【成员 %s / 来源 %s】\n%s\n\n", m, path, sd.Detail)
		}
	}
	return strings.TrimSpace(sb.String())
}

// ——————————————————————————————————————————————————————————————————————————————
// FuseDescriptions — description 融合主入口
// ——————————————————————————————————————————————————————————————————————————————

// FuseDescriptions 按 subdomain 并发地为所有融合节点重写 description（就地修改 res）。
// docs 是刷了 04 id 的标注文档（提供 detail 素材）。不返回 error（单 subdomain 失败降级、
// 保持其 description 为空，不阻断流水线）。
//
// FuseDescriptions rewrites every fusion node's description, fanning out per subdomain.
func (e *Extractor) FuseDescriptions(ctx context.Context, res *Result, docs []*dktypes.AnnotatedDocument) *DescribeReport {
	rpt := &DescribeReport{}
	if res == nil || len(res.Domains) == 0 {
		return rpt
	}
	detailIdx := buildDetailIndex(docs)

	// 收集 subdomain 任务（携带所属 domain 名以给 LLM 上下文）。
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
	rpt.SubdomainsTotal = len(tasks)

	// per-task 结果（按下标写，无竞争），并发执行后汇总。
	results := make([]sdDescribeResult, len(tasks))
	parallel.ForEachIndexed(parallel.MaxConcurrency, tasks, func(i int, t sdTask) {
		results[i] = e.fuseSubdomainDescriptions(ctx, t.domainName, t.sd, detailIdx)
	})

	for _, r := range results {
		rpt.NodesTotal += r.nodesTotal
		rpt.NodesWithDetail += r.nodesWithDetail
		rpt.NodesRewritten += r.nodesRewritten
		if r.ok {
			rpt.SubdomainsOK++
		}
		if r.failure != "" {
			rpt.Failures = append(rpt.Failures, r.failure)
		}
	}
	log.Printf("[extract-describe] description 融合完成：subdomain %d/%d 成功，节点 %d 个（有素材 %d），重写 %d 个",
		rpt.SubdomainsOK, rpt.SubdomainsTotal, rpt.NodesTotal, rpt.NodesWithDetail, rpt.NodesRewritten)
	// 门控告警：正常数据下每个融合节点都有 members、每个 04 单元 detail 非空，
	// 故应满足「节点数 == 有素材数 == 重写数」。任一缺口都指向上游异常或 LLM 未补齐，需关注。
	if noMaterial := rpt.NodesTotal - rpt.NodesWithDetail; noMaterial > 0 {
		log.Printf("[extract-describe] ⚠ %d 个节点无 detail 素材（members 为空或来源 detail 缺失）→ description 留空；正常不应出现，请检查上游融合/覆盖", noMaterial)
	}
	if miss := rpt.NodesWithDetail - rpt.NodesRewritten; miss > 0 {
		log.Printf("[extract-describe] ⚠ %d 个有素材节点经门控 %d 轮重试仍未写入 description（见 Failures）", miss, maxDescribeRounds)
	}
	return rpt
}

// sdDescribeResult 是单个 subdomain description 融合的结果（供并发汇总）。
type sdDescribeResult struct {
	nodesTotal      int
	nodesWithDetail int
	nodesRewritten  int
	ok              bool
	failure         string
}

// fuseSubdomainDescriptions 为单个 subdomain 内所有有素材的融合节点重写 description。
// 仅对「members 有 detail 素材」的节点投喂 LLM；无素材节点保持 description 为空
// （ingest 侧 summary 已有兜底）。解析失败原地重试，耗尽则该 subdomain 降级（不写 description）。
func (e *Extractor) fuseSubdomainDescriptions(ctx context.Context, domainName string, sd *Subdomain, detailIdx map[string][]sourceDetail) sdDescribeResult {
	res := sdDescribeResult{}

	// 收集本 subdomain 内「有素材」的节点及其素材。
	type nodeMat struct {
		id       string
		label    string
		name     string
		summary  string
		material string
	}
	var mats []nodeMat
	collect := func(nodes []Node, label string) {
		for i := range nodes {
			res.nodesTotal++
			material := aggregateMemberDetails(nodes[i].Members, detailIdx)
			if material == "" {
				// 诊断：区分「members 为空」和「members 有 id 但 detail 缺失」
				if len(nodes[i].Members) == 0 {
					log.Printf("[extract-describe] ⚠ 节点 %q [%s] 无素材: members 为空（上游融合/覆盖未分配 member）", nodes[i].Name, label)
				} else {
					log.Printf("[extract-describe] ⚠ 节点 %q [%s] 无素材: members=%v 但 detail 缺失（04 id 在 detailIndex 中无对应或 Detail 字段为空）", nodes[i].Name, label, nodes[i].Members)
				}
				continue
			}
			res.nodesWithDetail++
			mats = append(mats, nodeMat{
				id: nodes[i].ID, label: label, name: nodes[i].Name,
				summary: nodes[i].Summary, material: material,
			})
		}
	}
	collect(sd.Entities, string(dktypes.LabelEntity))
	collect(sd.Concepts, string(dktypes.LabelConcept))

	if len(mats) == 0 {
		res.ok = true // 无素材可融合，视为正常完成
		return res
	}

	// matByID 供门控按 id 反查素材，对漏点 / 空 description 的节点做针对性伪 session 补写。
	matByID := make(map[string]nodeMat, len(mats))
	for _, m := range mats {
		matByID[m.id] = m
	}

	// 门控 + 伪 session 重试：确保每个有素材节点都写出非空 description。
	//   - 每轮只投喂 pending（首轮为全部有素材节点，其后为上一轮漏掉 / 返回空的节点）；
	//   - LLM 批量返回易漏节点或给空串，门控据此逐轮收敛，直到全部补齐或耗尽 maxDescribeRounds；
	//   - written 累积已确认的非空 description，避免重复重写、也便于耗尽时一次性写回。
	written := make(map[string]string, len(mats)) // node_id → 已确认非空的 description
	pending := mats
	var lastErr string
	for round := 1; round <= maxDescribeRounds; round++ {
		promptNodes := make([]describePromptNode, 0, len(pending))
		for _, m := range pending {
			promptNodes = append(promptNodes, describePromptNode{
				ID: m.id, Label: m.label, Name: m.name, Summary: m.summary, Material: m.material,
			})
		}

		userMsg := buildDescribePrompt(domainName, sd.Name, sd.Summary, promptNodes, lastErr)
		resp, err := e.client.Complete(ctx, llm.CompleteRequest{
			System:    describeSystemPrompt,
			User:      userMsg,
			MaxTokens: e.maxTokens,
		})
		if err != nil {
			res.failure = fmt.Sprintf("subdomain %q description 融合 LLM 调用失败: %v", sd.Name, err)
			break
		}
		var out describeOutput
		if perr := json.Unmarshal([]byte(stripCodeFence(strings.TrimSpace(resp.Text))), &out); perr != nil {
			lastErr = fmt.Sprintf("上一轮返回非法 JSON: %v", perr)
			continue
		}
		// 收集本轮有效返回：node_id 属于本 subdomain 有素材集合、且 description 非空。
		for _, d := range out.Descriptions {
			id := strings.TrimSpace(d.NodeID)
			desc := strings.TrimSpace(d.Description)
			if id == "" || desc == "" {
				continue // 空 description 视为未写，交由门控下一轮补
			}
			if _, ok := matByID[id]; !ok {
				continue // 忽略不属于有素材集合的 id（LLM 编造 / 越界）
			}
			written[id] = desc
		}
		// 门控：重算仍缺 description 的节点。
		var still []nodeMat
		for _, m := range pending {
			if _, ok := written[m.id]; !ok {
				still = append(still, m)
			}
		}
		if len(still) == 0 {
			res.nodesRewritten += applyDescriptions(sd, written)
			res.ok = true
			return res
		}
		// 仍有漏点 / 空 description → 伪 session 只重试这些节点，并把漏点显式反馈给 LLM。
		pending = still
		missingIDs := make([]string, 0, len(still))
		for _, m := range still {
			missingIDs = append(missingIDs, m.id)
		}
		lastErr = fmt.Sprintf("以下 %d 个节点尚未给出有效 description（漏掉或为空），必须为每一个都补写非空、语义完整的 description：%s",
			len(still), strings.Join(missingIDs, "、"))
	}

	// 耗尽：写回已确认的，剩余缺失记为失败（可观测）。这些节点 description 仍为空，ingest 侧由 summary 兜底。
	res.nodesRewritten += applyDescriptions(sd, written)
	if len(written) < len(mats) {
		res.failure = fmt.Sprintf("subdomain %q description 融合 %d 轮后仍有 %d/%d 个节点缺少 description",
			sd.Name, maxDescribeRounds, len(mats)-len(written), len(mats))
	} else {
		res.ok = true
	}
	return res
}

// applyDescriptions 把 node_id → description 写回 subdomain 的 entities/concepts，返回写入数。
func applyDescriptions(sd *Subdomain, descByID map[string]string) int {
	n := 0
	for i := range sd.Entities {
		if desc, ok := descByID[sd.Entities[i].ID]; ok {
			sd.Entities[i].Description = desc
			n++
		}
	}
	for i := range sd.Concepts {
		if desc, ok := descByID[sd.Concepts[i].ID]; ok {
			sd.Concepts[i].Description = desc
			n++
		}
	}
	return n
}

// ——————————————————————————————————————————————————————————————————————————————
// prompt 与解析 / Prompt and parsing
// ——————————————————————————————————————————————————————————————————————————————

// describePromptNode 是投喂给 LLM 的单个待重写节点。
type describePromptNode struct {
	ID       string
	Label    string
	Name     string
	Summary  string
	Material string
}

// describeOutput 是 description 融合 LLM 返回的 JSON。
type describeOutput struct {
	Descriptions []struct {
		NodeID      string `json:"node_id"`
		Description string `json:"description"`
	} `json:"descriptions"`
}

// describeSystemPrompt 是 description 融合阶段的 system 提示。
const describeSystemPrompt = `你是知识融合重写专家。用户会给你一个子域（subdomain）内的若干融合节点，
每个节点有名称、摘要，以及它所融合的多个来源单元的原文详述（素材，可能来自多篇文档、存在重复或互补）。
你的任务是为每个节点，综合其全部素材，重写出一段完整、准确、连贯的详述（description）。

严格要求：
1. 只返回一个 JSON 对象（json 格式），形如 {"descriptions":[{"node_id":"...","description":"..."}]}，不要输出任何解释性文字或代码围栏。
2. 每个 node_id 必须逐字取自输入给出的节点 id，不得编造或改写。
3. description 必须是详细、完整、语义幂等的正文：综合该节点自己的全部素材，去重、合并互补信息、保持语义连贯完整，
   在语义幂等前提下尽量保留素材中的长度与关键细节；绝不臆造素材之外的信息，也不要把别的节点的内容混入。
4. 必须为输入中的【每一个】节点都产出一条【非空】description——一个都不能遗漏、绝不允许留空。`

// buildDescribePrompt 组装 description 融合的 user 消息。
func buildDescribePrompt(domainName, subName, subSummary string, nodes []describePromptNode, prevErr string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "## 子域：%s（所属领域：%s）\n", subName, domainName)
	if strings.TrimSpace(subSummary) != "" {
		fmt.Fprintf(&sb, "子域说明：%s\n", subSummary)
	}
	sb.WriteString("\n请为下列每个融合节点，综合其「来源素材」重写 description：\n\n")

	for _, n := range nodes {
		fmt.Fprintf(&sb, "### 节点 node_id=%s [%s]：%s\n", n.ID, n.Label, n.Name)
		if strings.TrimSpace(n.Summary) != "" {
			fmt.Fprintf(&sb, "摘要：%s\n", n.Summary)
		}
		sb.WriteString("来源素材：\n")
		sb.WriteString(n.Material)
		sb.WriteString("\n\n")
	}

	sb.WriteString("只返回 descriptions JSON。\n")
	if strings.TrimSpace(prevErr) != "" {
		fmt.Fprintf(&sb, "\n## 上一轮的问题（请修正后重试）\n- %s\n", prevErr)
	}
	return sb.String()
}
