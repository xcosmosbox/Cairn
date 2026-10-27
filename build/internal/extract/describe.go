// Package extract 的本文件从全部来源成员的 Name/Content/Detail 构造节点素材，
// 使用全量与增量共享的有限语义修订 helper，核验通过后才应用 Summary+Description。
// 失败节点保留原候选并输出明确结果；来源 marker 和非空正文都不能冒充语义完成。
// This wrapper gathers complete member-bound materials and applies only verified node pairs.
package extract

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/parallel"
)

// DescribeReport 是 description 融合阶段的结果摘要（可观测，写盘到 05d）。
// DescribeReport summarizes the description-fusion stage (written to 05d).
type DescribeReport struct {
	SubdomainsTotal int                      // 参与的 subdomain 总数
	SubdomainsOK    int                      // 全部节点语义核验完成的 subdomain 数
	NodesTotal      int                      // 融合节点总数
	NodesWithDetail int                      // 有素材（members 有 detail）的节点数
	NodesRewritten  int                      // 已应用核验通过摘要/详述对的节点数
	Failures        []string                 // subdomain 级失败原因（可观测）
	Outcomes        []NodeDescriptionOutcome // 每个节点的实际语义核验与有限尝试
}

// ——————————————————————————————————————————————————————————————————————————————
// detail 聚合（按 04 id）/ Detail aggregation by 04 id
// ——————————————————————————————————————————————————————————————————————————————

// sourceDetail 是某个 04 单元来自某篇文档的一份 detail。
type sourceDetail struct {
	Path, Skill, CandidateID string
	ItemIndex                int
	Detail                   string
}

// buildDetailIndex 构造 04 id → 该单元在各来源文档中的 detail 列表。
// per-document 结构下，同一 04 id 可能出现在同 skill 的多篇文档中，故值为列表（保留多源）。
//
// buildDetailIndex maps each 04 id to its details across source documents.
func buildDetailIndex(docs []*dktypes.AnnotatedDocument) map[string][]sourceDetail {
	idx := make(map[string][]sourceDetail)
	var members []string
	for _, d := range docs {
		if d != nil {
			for _, it := range d.Items {
				members = append(members, it.ID)
			}
		}
	}
	for _, c := range LookupMemberContributions(docs, members) {
		it := c.Item
		if strings.TrimSpace(it.ID) == "" {
			continue
		}
		var detail string
		// Content 和 Detail 共同承载知识，不能用非空 Detail 遮掉 Content 中的条件。
		// Source labels alone are not substantive material.
		if strings.TrimSpace(it.Content) != "" || strings.TrimSpace(it.Detail) != "" {
			detail = fmt.Sprintf("名称：%s\n内容：%s\n详述：%s", it.Name, it.Content, it.Detail)
		}
		idx[it.ID] = append(idx[it.ID], sourceDetail{Path: c.FilePath, Skill: c.Skill, CandidateID: c.CandidateID, ItemIndex: c.ItemIndex, Detail: detail})
	}
	return idx
}

// aggregateMemberDetails 把一个融合节点 members 对应的多源 04 detail 聚合为带来源标记的素材文本。
// 无任何 detail 时返回空串（调用方据此跳过该节点）。
//
// aggregateMemberDetails concatenates the multi-source 04 details of a node's members.
func aggregateMemberDetails(members []string, idx map[string][]sourceDetail) string {
	var sb strings.Builder
	seen := map[string]bool{}
	for _, m := range members {
		if seen[m] {
			continue
		}
		seen[m] = true
		for _, sd := range idx[m] {
			if strings.TrimSpace(sd.Detail) == "" {
				continue
			}
			path := sd.Path
			if strings.TrimSpace(path) == "" {
				path = "(unknown)"
			}
			fmt.Fprintf(&sb, "【成员 %s / skill %s / 来源 %s / candidate %s / item %d】\n%s\n\n", m, sd.Skill, path, sd.CandidateID, sd.ItemIndex, sd.Detail)
		}
	}
	return strings.TrimSpace(sb.String())
}

// ——————————————————————————————————————————————————————————————————————————————
// FuseDescriptions — description 融合主入口
// ——————————————————————————————————————————————————————————————————————————————

// FuseDescriptions 按 subdomain 并发地为所有融合节点重写 description（就地修改 res）。
// docs 是刷了 04 id 的标注文档。失败节点保持原候选，Outcomes 明确记录其未完成状态。
// Product completion remains the caller's responsibility.
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
		rpt.Outcomes = append(rpt.Outcomes, r.outcomes...)
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
		log.Printf("[extract-describe] ⚠ %d 个节点来源素材不完整（成员缺失或来源为空）→ 保留原候选并报告未完成", noMaterial)
	}
	if miss := rpt.NodesWithDetail - rpt.NodesRewritten; miss > 0 {
		log.Printf("[extract-describe] ⚠ %d 个有素材节点有限修订后仍未通过语义核验（见 Outcomes/Failures）", miss)
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
	outcomes        []NodeDescriptionOutcome
}

// fuseSubdomainDescriptions 为单个 subdomain 内所有有素材的融合节点重写 description。
// 所有声明成员必须具有实质来源；只应用语义核验完成的摘要/详述对。
// Missing material and exhausted/unknown candidates remain explicit unresolved outcomes.
func (e *Extractor) fuseSubdomainDescriptions(ctx context.Context, domainName string, sd *Subdomain, detailIdx map[string][]sourceDetail) sdDescribeResult {
	res := sdDescribeResult{ok: true}
	var inputs []NodeDescriptionInput
	var failures []string
	collect := func(nodes []Node, label string) {
		for _, n := range nodes {
			res.nodesTotal++
			material := aggregateMemberDetails(n.Members, detailIdx)
			reason := ""
			if len(n.Members) == 0 {
				reason = "node has no source members"
			}
			for _, member := range n.Members {
				if strings.TrimSpace(member) == "" {
					reason = "node contains a blank source member identity"
					break
				}
				rows := detailIdx[member]
				if len(rows) == 0 {
					reason = fmt.Sprintf("member %q has no source contribution", member)
					break
				}
				for _, row := range rows {
					if strings.TrimSpace(row.Path) == "" {
						reason = fmt.Sprintf("member %q has an unknown source path", member)
						break
					}
					if strings.TrimSpace(row.Detail) == "" {
						reason = fmt.Sprintf("member %q has an empty source contribution", member)
						break
					}
				}
				if reason != "" {
					break
				}
			}
			if material == "" && reason == "" {
				reason = "node has no substantive source material"
			}
			if reason != "" {
				res.ok = false
				out := NodeDescriptionOutcome{ID: n.ID, Domain: domainName, Subdomain: sd.Name, Summary: n.Summary, Description: n.Description, Outcome: annotation.DocumentUnavailable, Reason: reason, Attempts: []annotation.DocumentAttempt{}}
				res.outcomes = append(res.outcomes, out)
				failures = append(failures, fmt.Sprintf("node %q: %s", n.ID, reason))
				continue
			}
			res.nodesWithDetail++
			inputs = append(inputs, NodeDescriptionInput{ID: n.ID, Label: label, Name: n.Name, Summary: n.Summary, Description: n.Description, Material: material})
		}
	}
	collect(sd.Entities, string(dktypes.LabelEntity))
	collect(sd.Concepts, string(dktypes.LabelConcept))
	outcomes := FuseNodeDescriptions(ctx, e.client, e.maxTokens, 0, domainName, sd.Name, inputs)
	res.outcomes = append(res.outcomes, outcomes.Nodes...)
	complete := map[string]NodeDescriptionOutcome{}
	for _, out := range outcomes.Nodes {
		if out.Outcome == annotation.DocumentComplete {
			complete[out.ID] = out
			res.nodesRewritten++
		} else {
			res.ok = false
			failures = append(failures, fmt.Sprintf("node %q: %s (%s)", out.ID, out.Reason, out.Outcome))
		}
	}
	apply := func(nodes []Node) {
		for i := range nodes {
			if out, ok := complete[nodes[i].ID]; ok {
				nodes[i].Summary = out.Summary
				nodes[i].Description = out.Description
			}
		}
	}
	apply(sd.Entities)
	apply(sd.Concepts)
	res.failure = strings.Join(failures, "; ")
	return res
}
