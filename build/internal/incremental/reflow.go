// Package incremental 的本文件实现 I-6（脏 node description 重融合）与 I-7
// （脏 subdomain relation 重算）——参照 extract.FuseDescriptions / IdentifyRelations
// 的 prompt 风格与门控语义，但只喂「脏集」子集输入（R9 脏集封闭）：
//
//   - I-6：仅对 dirty.NodesFused 的 node 重融合 description。
//     素材 = node 现有 description（已有 members 的融合沉淀——04 detail 不持久化，
//     增量场景以现有 description 为旧素材载体）+ 本轮新融入 members 的 detail（I-3 产出）。
//     human_curated 的 node 跳过（R8：人工内容绝不覆盖）。
//   - I-7：仅对 dirty.Subdomains 剪枝式重算 relation：输入该子域现存全部 node
//     （含本轮新增、已删的自然消失）+ 现有 relation，要求 LLM 输出**最终集合**
//     （保留仍有效的、删无效的=不出现在输出、补涉及新 node 的、无关原样保留——
//     最小改变）；6 枚举 kind 校验 + 端点存在性过滤 + 越界原地重试（与全量一致）。
//
// R1 铁律：两个阶段的 LLM 都只见 alias（<subdomain-slug>#<序号>），uuid 绝不进
// prompt；LLM 返回的端点/节点引用由代码翻译回 uuid（call-scoped aliasBook）。
//
// This file implements I-6 (dirty-node description re-fusion) and I-7 (dirty-
// subdomain relation recomputation), mirroring the full pipeline's prompt style
// but feeding only the dirty subset (R9). human_curated nodes are never
// re-fused (R8). All LLM references go through call-scoped aliases (R1).
package incremental

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"strings"
	"sync"

	"github.com/xcosmosbox/cairn/build/internal/annotation"
	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/parallel"
)

// reflowResult 是 I-6 + I-7 的联合产出（I-8 据此落库）。
// reflowResult is the combined I-6/I-7 output (I-8 persists it).
type reflowResult struct {
	// descriptions 是 I-6 重融合出的新 description（uuid → 正文）。
	// descriptions maps node uuid → re-fused description (I-6).
	descriptions map[string]string
	// relations 是每个脏子域重算后的「最终」语义 relation 集合（端点已是 uuid）。
	// relations holds the final recomputed semantic relations per dirty subdomain.
	relations map[SubdomainKey][]extract.Relation
	// keptCross 是每个脏子域「端点跨子域」的现存边（不进 LLM，原样保留重插）。
	// keptCross keeps existing edges whose endpoints cross subdomains (verbatim).
	keptCross map[SubdomainKey][]*dktypes.Edge
}

// reflower 封装 I-6/I-7 的依赖（llm.Client 注入，测试 mock）。
// reflower bundles I-6/I-7 dependencies (mockable llm.Client).
type reflower struct {
	client       llm.Client
	maxTokens    int
	st           *stores
	nodeMu       sync.Mutex
	nodeOutcomes []extract.NodeDescriptionOutcome
}

// newReflower 构造 I-6/I-7 执行器。
// newReflower constructs the I-6/I-7 executor.
func newReflower(client llm.Client, maxTokens int, st *stores) *reflower {
	return &reflower{client: client, maxTokens: maxTokens, st: st}
}

// ——————————————————————————————————————————————————————————————————————————————
// I-6 脏 node description 重融合
// ——————————————————————————————————————————————————————————————————————————————

// reflowNode 是 I-6 待重融合的单个节点的素材视图。
// reflowNode is one node's material view for I-6 re-fusion.
type reflowNode struct {
	uuid        string // 代码侧真实 uuid（绝不进 prompt）/ real uuid (never in prompt)
	label       string
	name        string
	summary     string
	existing    string // 现有 description（融入节点；新建为空）/ existing description
	newMaterial string // 新融入 members 的全部语义素材 / complete new-member material
	isNew       bool
}

// fuseDescriptions 执行 I-6：对 dirty.NodesFused 的 node 重融合 description。
// 按 subdomain 并发，逐节点复用全量语义修订 helper；human_curated 零调用保护（R8）。
// 只发布能按当前写入契约应用的核验结果，其余保留旧内容并报告未完成。
//
// fuseDescriptions runs I-6: re-fuse descriptions for member-dirty nodes only,
// parallel per subdomain; every node retains an explicit outcome (R8/R9).
func (r *reflower) fuseDescriptions(ctx context.Context, dirty *DirtySet, ao *alignOutcome, rs *runState) map[string]string {
	r.nodeMu.Lock()
	r.nodeOutcomes = nil // 每次调用只发布本轮证据，不能重用前次成功来改写新候选。
	r.nodeMu.Unlock()
	out := make(map[string]string)
	if dirty == nil || len(dirty.NodesFused) == 0 {
		return out
	}

	// 收集每个脏 node 的素材视图（human_curated 跳过）。
	bySub := make(map[SubdomainKey][]*reflowNode)
	for _, uuid := range dirty.SortedFusedNodes() {
		var pn *pendingNode
		if ao != nil {
			pn = ao.pending[uuid]
		}
		if pn == nil {
			reason := "I-6: dirty node has no pending member mapping"
			r.recordNodeOutcome(extract.NodeDescriptionOutcome{ID: uuid, Outcome: annotation.DocumentUnavailable, Reason: reason})
			rs.warnf(reason + ": " + uuid)
			continue
		}
		rn := &reflowNode{uuid: uuid, isNew: pn.IsNew}
		if pn.IsNew {
			// 新建 node：素材 = 全部 members（都是新单元）的 detail。
			rn.label = pn.Label
			rn.name = pn.Name
			rn.summary = pn.Summary
			rn.newMaterial = aggregateReflowMemberDetails(pn.NewMembers, ao)
		} else {
			if r.st == nil || r.st.nodes == nil {
				reason := "I-6: existing node store is unavailable"
				r.recordNodeOutcome(extract.NodeDescriptionOutcome{ID: uuid, Domain: pn.Domain, Subdomain: pn.Subdomain, Outcome: annotation.DocumentUnavailable, Reason: reason})
				rs.warnf(reason + ": " + uuid)
				continue
			}
			node, err := r.st.nodes.GetByID(ctx, uuid)
			if err != nil || node == nil {
				reason := fmt.Sprintf("I-6: existing node %s is unavailable: %v", uuid, err)
				r.recordNodeOutcome(extract.NodeDescriptionOutcome{ID: uuid, Domain: pn.Domain, Subdomain: pn.Subdomain, Outcome: annotation.DocumentUnavailable, Reason: reason})
				rs.warnf(reason)
				continue
			}
			// R8：human_curated 的 description 不被增量重融合覆盖。
			if node.Provenance == dktypes.ProvenanceHumanCurated {
				r.recordNodeOutcome(extract.NodeDescriptionOutcome{ID: uuid, Domain: pn.Domain, Subdomain: pn.Subdomain, Summary: node.Summary, Description: node.Description, Outcome: annotation.DocumentPartial, Reason: "human_curated content protected; added material is not verified or applied"})
				log.Printf("[incremental-reflow] node %s 为 human_curated，跳过重融合（R8）；新增素材尚未验证/应用", uuid)
				continue
			}
			rn.label = string(node.Label)
			rn.name = node.Name
			rn.summary = node.Summary
			rn.existing = node.Description
			rn.newMaterial = aggregateReflowMemberDetails(pn.NewMembers, ao)
		}
		key := SubdomainKey{Domain: pn.Domain, Subdomain: pn.Subdomain}
		bySub[key] = append(bySub[key], rn)
	}

	// 按 subdomain 并发批量重融合（不同子域写各自节点，无共享状态）。
	type sdResult struct {
		descByUUID map[string]string
	}
	keys := make([]SubdomainKey, 0, len(bySub))
	for k := range bySub {
		keys = append(keys, k)
	}
	sortSubdomainKeys(keys)
	results := make([]sdResult, len(keys))
	parallel.ForEachIndexed(parallel.MaxConcurrency, keys, func(i int, key SubdomainKey) {
		results[i] = sdResult{descByUUID: r.fuseSubdomainBatch(ctx, key, bySub[key], rs)}
	})
	for _, res := range results {
		for uuid, desc := range res.descByUUID {
			out[uuid] = desc
		}
	}
	for _, result := range r.nodeDescriptionOutcomes() {
		if result.Outcome == annotation.DocumentComplete && ao != nil {
			if pn := ao.pending[result.ID]; pn != nil && pn.IsNew {
				pn.Summary = result.Summary
			}
		}
	}
	return out
}

// fuseSubdomainBatch 逐节点核验摘要与详述，调用只含 alias，UUID 保留在代码侧。
// Existing summaries requiring a persistence change remain explicitly unresolved.
func (r *reflower) fuseSubdomainBatch(ctx context.Context, key SubdomainKey, nodes []*reflowNode, rs *runState) map[string]string {
	written := make(map[string]string)
	var inputs []extract.NodeDescriptionInput
	byID := map[string]*reflowNode{}
	for _, n := range nodes {
		if n == nil {
			continue
		}
		inputs = append(inputs, extract.NodeDescriptionInput{ID: n.uuid, Label: n.label, Name: n.name, Summary: n.summary, ExistingDescription: n.existing, Material: n.newMaterial})
		byID[n.uuid] = n
	}
	result := extract.FuseNodeDescriptions(ctx, r.client, r.maxTokens, 0, key.Domain, key.Subdomain, inputs)
	for _, out := range result.Nodes {
		n := byID[out.ID]
		if out.Outcome == annotation.DocumentComplete && n != nil {
			if !n.isNew && out.Summary != n.summary {
				// 既有 I-8 只写 description，不能把未能持久化的新摘要冒充完成。
				// Expose the verified correction, retain the old pair, and hand off to .5/.6.
				out.Outcome = annotation.DocumentPartial
				out.Reason = "verified summary repair requires persistence hook; existing summary and description retained"
			} else {
				written[out.ID] = out.Description
			}
		}
		r.recordNodeOutcome(out)
		if out.Outcome != annotation.DocumentComplete {
			rs.warnf(fmt.Sprintf("I-6 node %s: %s (%s)", out.ID, out.Reason, out.Outcome))
		}
	}
	return written
}

// nodeDescriptionOutcomes 返回实际结果供调用者汇总；保存原文不代表核验通过。
// The caller, not this node helper, decides product-level completion.
func (r *reflower) nodeDescriptionOutcomes() []extract.NodeDescriptionOutcome {
	r.nodeMu.Lock()
	defer r.nodeMu.Unlock()
	out := append([]extract.NodeDescriptionOutcome(nil), r.nodeOutcomes...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].ID > out[j].ID; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

func (r *reflower) recordNodeOutcome(out extract.NodeDescriptionOutcome) {
	r.nodeMu.Lock()
	defer r.nodeMu.Unlock()
	r.nodeOutcomes = append(r.nodeOutcomes, out)
}

// aggregateUnitDetails 聚合若干 member（04 id）的 detail 为带来源标记的素材文本
// （参照全量 aggregateMemberDetails 的风格）。
// aggregateUnitDetails concatenates member details with source markers.
func aggregateUnitDetails(memberIDs []string, unitsByID map[string]*alignUnit) string {
	var sb strings.Builder
	seen := map[string]bool{}
	for _, m := range memberIDs {
		if seen[m] {
			continue
		}
		seen[m] = true
		u := unitsByID[m]
		if u == nil || (strings.TrimSpace(u.Content) == "" && strings.TrimSpace(u.Detail) == "") {
			return ""
		}
		fmt.Fprintf(&sb, "【成员 %s / skill %s / 来源 %s】\n名称：%s\n内容：%s\n详述：%s\n\n", m, u.Skill, u.FilePath, u.Name, u.Content, u.Detail)
	}
	return strings.TrimSpace(sb.String())
}

// aggregateReflowMemberDetails 优先使用每条来源贡献，不把聚合行的首个路径冒充所有来源。
// Preserve all source-bound contributions when alignment has retained them.
func aggregateReflowMemberDetails(memberIDs []string, ao *alignOutcome) string {
	if ao == nil {
		return ""
	}
	var b strings.Builder
	seen := map[string]bool{}
	for _, member := range memberIDs {
		if strings.TrimSpace(member) == "" {
			return ""
		}
		if seen[member] {
			continue
		}
		seen[member] = true
		rows := ao.sourcesByID[member]
		if len(rows) == 0 {
			rows = []*alignUnit{ao.unitsByID[member]}
		}
		for _, u := range rows {
			if u == nil || strings.TrimSpace(u.FilePath) == "" || (strings.TrimSpace(u.Content) == "" && strings.TrimSpace(u.Detail) == "") {
				return ""
			}
			fmt.Fprintf(&b, "【成员 %s / skill %s / 来源 %s】\n名称：%s\n内容：%s\n详述：%s\n\n", member, u.Skill, u.FilePath, u.Name, u.Content, u.Detail)
		}
	}
	return strings.TrimSpace(b.String())
}

// ——————————————————————————————————————————————————————————————————————————————
// I-7 脏 subdomain relation 重算（剪枝式）
// ——————————————————————————————————————————————————————————————————————————————

// relateCandidates 是 I-7 单个子域的候选节点视图（KG 现存 + 本轮新增）。
// relateCandidates is one dirty subdomain's candidate nodes for I-7.
type relateCandidates struct {
	nodes []*dktypes.Node // entity/concept 节点（含 pending 新建的虚拟节点）
}

// recomputeRelations 执行 I-7：对每个脏子域剪枝式重算语义 relation。
// 输入：脏子域 + I-6 重融合的 description（关系判定要看最新正文）+ 本轮新建 node。
// 输出：每子域「最终」relation 集合（uuid 端点）+ 跨子域保留边（I-8 先删后插）。
//
// recomputeRelations runs I-7: per dirty subdomain, prune-recompute semantic
// relations over current + newly added nodes, using freshly fused descriptions.
func (r *reflower) recomputeRelations(ctx context.Context, dirty *DirtySet, ao *alignOutcome,
	descriptions map[string]string, rs *runState) (map[SubdomainKey][]extract.Relation, map[SubdomainKey][]*dktypes.Edge) {

	finalRels := make(map[SubdomainKey][]extract.Relation)
	keptEdges := make(map[SubdomainKey][]*dktypes.Edge)
	// 本轮被触碰的 node（新增/融入、C1 内容编辑、I-6 正文重融合）。两端都不在此
	// 集合中的现存内部边与本轮变化无关，受硬保护——详见下方结果合并处的说明。
	touched := touchedNodeSet(dirty, ao, descriptions, rs)
	for _, key := range dirty.SortedSubdomains() {
		// —— 现存节点（entity/concept；层节点不参与 relation）——
		existing, err := r.st.nodes.ListBySubdomain(ctx, key.Domain, key.Subdomain)
		if err != nil {
			rs.warnf(fmt.Sprintf("I-7 子域 %s 读取节点失败（跳过）: %v", key, err))
			continue
		}
		var nodes []*dktypes.Node
		for _, n := range existing {
			if n.Label == dktypes.LabelEntity || n.Label == dktypes.LabelConcept {
				nodes = append(nodes, n)
			}
		}
		// —— 本轮新建 node（虚拟节点：uuid 已派生、description 用 I-6 产出）——
		// ao is normally non-nil in the orchestrator, but treating a nil outcome as
		// an empty pending set keeps this read-only stage fail-closed for callers that
		// only need to reflow existing nodes.
		if ao != nil {
			for uuid, pn := range ao.pending {
				if pn == nil {
					continue
				}
				if !pn.IsNew || pn.Domain != key.Domain || pn.Subdomain != key.Subdomain {
					continue
				}
				desc := descriptions[uuid]
				if desc == "" {
					desc = pn.Summary
				}
				nodes = append(nodes, &dktypes.Node{
					ID: uuid, Label: dktypes.Label(pn.Label), Name: pn.Name,
					Summary: pn.Summary, Description: desc,
					Domain: key.Domain, Subdomain: key.Subdomain,
				})
			}
		}
		// I-6 重融合的正文覆盖 KG 旧值（关系判定看最新 description）。
		for _, n := range nodes {
			if d, ok := descriptions[n.ID]; ok {
				n.Description = d
			}
		}

		// —— 现存语义边：子域内（进 LLM 剪枝）与跨子域（原样保留）分流 ——
		nodeSet := make(map[string]bool, len(nodes))
		for _, n := range nodes {
			nodeSet[n.ID] = true
		}
		var internal, cross []*dktypes.Edge
		seenEdge := make(map[string]*dktypes.Edge)
		readFailed := false
		for _, n := range nodes {
			edges, err := r.st.edges.GetOutgoing(ctx, n.ID, 0)
			if err != nil {
				rs.warnf(fmt.Sprintf("I-7 读取 node %s 出边失败（跳过该子域）: %v", n.ID, err))
				// A partial edge view is not authoritative. Do not let the nodes/edges
				// collected before the failure reach the LLM or the ingest maps.
				readFailed = true
				break
			}
			for _, e := range edges {
				if !extract.IsValidRelationKind(string(e.Kind)) {
					continue // 只要 6 枚举语义边（composes 层级边由 ingest 物化，不经此处）
				}
				edgeKey := e.SourceID + "\x00" + e.TargetID + "\x00" + string(e.Kind)
				if previous := seenEdge[edgeKey]; previous != nil {
					// 旧库重复身份里人工行优先，不能因机器行先读到就丢掉 R8 标记。
					// Storage separately preserves every original row of this identity.
					if previous.Provenance != dktypes.ProvenanceHumanCurated && e.Provenance == dktypes.ProvenanceHumanCurated {
						*previous = *e
					}
					continue
				}
				seenEdge[edgeKey] = e
				if nodeSet[e.TargetID] {
					internal = append(internal, e)
				} else {
					cross = append(cross, e) // 端点跨子域：原样保留，不进 LLM
				}
			}
		}
		if readFailed {
			// Missing map keys are the explicit fail-closed signal consumed by I-8.
			// In particular, do not publish an empty keptCross key: doing so would
			// make a later implementation mistake look like an authoritative result.
			continue
		}
		// —— 节点 < 2：无关系可判，最终集合为空（I-8 先删后插即清空）——
		if len(nodes) < 2 {
			finalRels[key] = nil
			// The edge view is complete, and no LLM stage can fail for a
			// subdomain with fewer than two nodes, so its cross edges are an
			// authoritative part of the successful empty result.
			keptEdges[key] = cross
			continue
		}

		// —— LLM 剪枝重算（alias 呈现，R1）——
		rels, err := r.relateOneSubdomain(ctx, key, nodes, internal, rs)
		if err != nil {
			rs.warnf(fmt.Sprintf("I-7 子域 %s relation 重算失败（保留现存内部边）: %v", key, err))
			// No authoritative result is published on exhausted retries. I-8
			// interprets the missing key as "leave the existing semantic edges
			// untouched", preserving every edge column rather than lossy-casting
			// old *Edge values into extract.Relation.
			continue
		}
		// Publish both pieces only after the relation stage has succeeded. A
		// successful empty relation set is represented by a present key with a
		// nil/empty slice; that is distinct from a missing key above.
		//
		// 硬保护（R9 脏集封闭在「边」维度的补全）：I-7 是「全量最终集合」语义——
		// LLM 不返回某条边即视为删除。脏集的最小单位是 subdomain，故一旦子域变脏，
		// 其内部**全部**既有边都进入可删范围，而其中大多数与本轮变化毫无关系。
		// LLM 一次遗漏就等于静默丢失既有知识连接：无告警、不可回溯（对比节点真删
		// 有「真删 N」、primary 删除有「清理 stale primary」日志）。
		//
		// R8 优先于脏集裁量：人工边即使端点被触碰也必须整行保留；机器返回同一
		// 身份不代表有权覆盖人工内容。先滤掉冲突生成项，再把原始人工边补回。
		// Machine-only unrelated omissions keep the existing R9 protection; all
		// human identities retain their original payload regardless of touched nodes.
		//
		// 删边应通过 rebalance 的显式 drop_edge patch 表达（可观测），而不是靠
		// 重算时「少返回几条」隐式发生。
		rels = excludeHumanEdgeReplacements(internal, rels)
		missing := missingProtectedEdges(internal, touched, rels)
		finalRels[key] = rels
		// 补回的边以原始 *Edge 形态重插，保留全部列（避免 lossy-cast 成 Relation）。
		kept := append([]*dktypes.Edge(nil), cross...)
		keptEdges[key] = append(kept, missing...)
		if n := len(missing); n > 0 {
			log.Printf("[incremental-reflow] I-7 子域 %s: 保留 %d 条人工边或与本轮无关的遗漏边（R8/R9）", key, n)
		}
	}
	return finalRels, keptEdges
}

// touchedNodeSet 汇总「本轮被触碰」的 node uuid：新增/融入（ao.pending、
// DirtySet.NodesFused）、内容编辑（DirtySet.NodesContent、runState.touched）、
// 正文重融合（I-6 descriptions）。与这些 node 都无关的边即「本轮不涉及」。
//
// 取并集是保守的：宁可少保护几条边，也不要把真正需要重判的边锁死。
//
// touchedNodeSet unions every node uuid this run touched.
func touchedNodeSet(dirty *DirtySet, ao *alignOutcome, descriptions map[string]string, rs *runState) map[string]bool {
	touched := make(map[string]bool)
	if dirty != nil {
		for u := range dirty.NodesFused {
			touched[u] = true
		}
		for u := range dirty.NodesContent {
			touched[u] = true
		}
	}
	if ao != nil {
		for u, pn := range ao.pending {
			if pn != nil {
				touched[u] = true
			}
		}
	}
	for u := range descriptions {
		touched[u] = true
	}
	if rs != nil {
		for u := range rs.touched {
			touched[u] = true
		}
	}
	return touched
}

// excludeHumanEdgeReplacements 不把机器返回的同身份结果当成人工编辑授权。
// Drop generated collisions before publishing I-7's machine relation set.
func excludeHumanEdgeReplacements(internal []*dktypes.Edge, rels []extract.Relation) []extract.Relation {
	protected := make(map[string]bool)
	for _, edge := range internal {
		if edge != nil && edge.Provenance == dktypes.ProvenanceHumanCurated {
			protected[edge.SourceID+"\x00"+edge.TargetID+"\x00"+string(edge.Kind)] = true
		}
	}
	kept := rels[:0]
	for _, rel := range rels {
		if protected[rel.Source+"\x00"+rel.Target+"\x00"+rel.Kind] {
			log.Printf("[incremental-reflow] R8 skip generated replacement of human edge %s --%s--> %s", rel.Source, rel.Kind, rel.Target)
			continue
		}
		kept = append(kept, rel)
	}
	return kept
}

// missingProtectedEdges 补回整条人工边（不受 touched/returned 影响），以及未触碰的机器遗漏边。
// Preserve every human row and the prior R9 machine-only omission policy.
func missingProtectedEdges(internal []*dktypes.Edge, touched map[string]bool, rels []extract.Relation) []*dktypes.Edge {
	if len(internal) == 0 {
		return nil
	}
	returned := make(map[string]bool, len(rels))
	for _, rel := range rels {
		returned[rel.Source+"\x00"+rel.Target+"\x00"+rel.Kind] = true
	}
	var out []*dktypes.Edge
	for _, e := range internal {
		if e == nil {
			continue
		}
		if e.Provenance == dktypes.ProvenanceHumanCurated {
			out = append(out, e)
			log.Printf("[incremental-reflow] R8 preserve human edge id=%d %s --%s--> %s", e.ID, e.SourceID, e.Kind, e.TargetID)
			continue
		}
		if touched[e.SourceID] || touched[e.TargetID] {
			continue // 与本轮变化相关 → LLM 裁量（含删除权）
		}
		if returned[e.SourceID+"\x00"+e.TargetID+"\x00"+string(e.Kind)] {
			continue // LLM 已确认保留 → 走 I-8 字段级合并路径
		}
		out = append(out, e)
	}
	return out
}

// relateOneSubdomain 对单个脏子域做剪枝式 relation 重算：
// 输入全部候选节点（alias + name + description）与现存内部 relation（alias 端点），
// 要求 LLM 输出「最终集合」——保留仍有效的、删除无效的（不出现在输出即删）、
// 为涉及新节点的关系补充新条目、无关的原样保留（最小改变）。
// 校验：6 枚举 kind（extract.IsValidRelationKind，与全量同一枚举集）+ 端点命中
// alias + 非自环；越界条目局部丢弃，非法 JSON 仅使当前子域不可用。
//
// relateOneSubdomain prune-recomputes one dirty subdomain's relations: the LLM
// emits the final relation set over aliased nodes, validated against the 6-kind
// enum and alias endpoints in one response.
func (r *reflower) relateOneSubdomain(ctx context.Context, key SubdomainKey, nodes []*dktypes.Node,
	existing []*dktypes.Edge, rs *runState) ([]extract.Relation, error) {

	// alias 分配（call-scoped）：<subdomain-slug>#<序号>。
	uuidByAlias := make(map[string]string)
	aliasByUUID := make(map[string]string)
	// 稳定序（输出确定性）：按 uuid 排序。
	sortedNodes := append([]*dktypes.Node(nil), nodes...)
	sortNodesByID(sortedNodes)
	for i, n := range sortedNodes {
		alias := fmt.Sprintf("%s#%d", key.Subdomain, i+1)
		uuidByAlias[alias] = n.ID
		aliasByUUID[n.ID] = alias
	}

	resp, err := r.client.Complete(ctx, llm.CompleteRequest{
		System:    relateRecomputeSystemPrompt,
		User:      buildRelateRecomputePrompt(key, sortedNodes, existing, aliasByUUID, ""),
		MaxTokens: r.maxTokens,
	})
	if err != nil {
		return nil, fmt.Errorf("LLM 调用失败: %w", err)
	}
	if resp == nil {
		return nil, fmt.Errorf("LLM 返回空响应")
	}
	var out relateRecomputeOutput
	if err := json.Unmarshal([]byte(stripAlignFence(strings.TrimSpace(resp.Text))), &out); err != nil {
		return nil, fmt.Errorf("relation JSON: %w", err)
	}
	rels, invalid := translateRecomputedRelations(out.Relations, uuidByAlias)
	if invalid {
		rs.warnf(fmt.Sprintf("I-7 子域 %s 丢弃非法 relation 条目；保留有效条目及已有关系", key))
		// 非法条目不能成为删边授权；保留旧关系，同时接受有效新增/更新。
		// A partial malformed set does not authorize deletion of existing pairs.
		seen := map[string]bool{}
		for _, rel := range rels {
			seen[rel.Source+"\x00"+rel.Target+"\x00"+rel.Kind] = true
		}
		for _, edge := range existing {
			identity := edge.SourceID + "\x00" + edge.TargetID + "\x00" + string(edge.Kind)
			if !seen[identity] {
				rels = append(rels, extract.Relation{Source: edge.SourceID, Target: edge.TargetID, Kind: string(edge.Kind), Description: edge.Description, Confidence: edge.Confidence})
				seen[identity] = true
			}
		}
	}
	return rels, nil
}

// relateRecomputeOutput 是 I-7 LLM 返回的 JSON。
// relateRecomputeOutput is the I-7 LLM JSON output.
type relateRecomputeOutput struct {
	Relations []struct {
		Source      string  `json:"source"`
		Target      string  `json:"target"`
		Kind        string  `json:"kind"`
		Description string  `json:"description"`
		Confidence  float64 `json:"confidence"`
	} `json:"relations"`
}

// translateRecomputedRelations 把 LLM 返回的 alias 端点 relation 翻译为 uuid 端点：
// kind 越界丢弃并告警；端点未命中 alias（编造）或自环丢弃（绝不悬空，R5）；
// 同身份 (source,target,kind) 重复条目去重（问题 7：LLM 重复返回只落一条）。
// translateRecomputedRelations translates alias endpoints back to uuids, dropping
// invalid kinds, fabricated endpoints, self-loops (R5), and
// duplicate (source,target,kind) triples.
func translateRecomputedRelations(rels []struct {
	Source      string  `json:"source"`
	Target      string  `json:"target"`
	Kind        string  `json:"kind"`
	Description string  `json:"description"`
	Confidence  float64 `json:"confidence"`
}, uuidByAlias map[string]string) (out []extract.Relation, hasInvalidKind bool) {
	seen := make(map[string]bool)
	for _, r := range rels {
		if !extract.IsValidRelationKind(r.Kind) || math.IsNaN(r.Confidence) || math.IsInf(r.Confidence, 0) {
			hasInvalidKind = true
			continue
		}
		src, okS := uuidByAlias[r.Source]
		tgt, okT := uuidByAlias[r.Target]
		if !okS || !okT || src == tgt {
			hasInvalidKind = true
			continue // 编造端点 / 自环 → 丢弃（R5）
		}
		identity := src + "\x00" + tgt + "\x00" + r.Kind
		if seen[identity] {
			continue // 重复三元组去重（问题 7）
		}
		seen[identity] = true
		conf := r.Confidence
		if conf <= 0 || conf > 1 {
			conf = 0.8
		}
		out = append(out, extract.Relation{
			Source: src, Target: tgt, Kind: r.Kind,
			Description: r.Description, Confidence: conf,
		})
	}
	return out, hasInvalidKind
}

// relateRecomputeSystemPrompt 是 I-7 剪枝重算的 system 提示（与全量 relate 同风格，
// 增加「最小改变」增量语义）。
// relateRecomputeSystemPrompt is the I-7 prune-recompute system prompt.
const relateRecomputeSystemPrompt = `你是领域知识关系识别专家。用户会给你**一个子域**内的全部节点（实体 / 概念）及其详述，
以及该子域**现有的语义关系**（以节点 alias 表示）。这是一次增量更新：子域刚发生了节点新增或删除。
请输出这些节点之间语义关系的【最终集合】：

- 保留仍然成立的关系（原样保留，包括 description）；
- 删除不再成立的关系（不出现在输出中即视为删除，例如端点已删除或语义已失效）；
- 为涉及新增节点的关系补充新条目；
- 与增删无关的关系保持原样（最小改变）。

严格要求：
1. 只返回一个 JSON 对象（json 格式），形如 {"relations":[{"source":"alias","target":"alias","kind":"...","description":"...","confidence":0.x}]}，
   不要输出任何解释性文字或代码围栏。
2. source 与 target 必须逐字引用输入节点列表中的 alias，不得编造、不得指向列表之外。
3. kind 只能取以下 6 个语义枚举之一，绝不允许自造：
   "triggers"、"depends_on"、"references"、"generalizes"、"composes"、"contradicts"。
4. 只识别真实、明确的关系；如果最终没有任何关系，返回 {"relations":[]} 是完全允许的。
5. 不要输出层级 / 归属类关系（那由结构自动物化）。`

// buildRelateRecomputePrompt 组装 I-7 的 user 消息（节点清单 + 现存关系 + 上轮反馈）。
// buildRelateRecomputePrompt assembles the I-7 user message.
func buildRelateRecomputePrompt(key SubdomainKey, nodes []*dktypes.Node, existing []*dktypes.Edge,
	aliasByUUID map[string]string, prevErr string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "## 子域：%s（所属域：%s）\n\n### 本子域内的节点（只在它们之间判定关系）\n", key.Subdomain, key.Domain)
	for _, n := range nodes {
		fmt.Fprintf(&sb, "- alias=%s [%s]：%s\n", aliasByUUID[n.ID], n.Label, n.Name)
		body := strings.TrimSpace(n.Description)
		if body == "" {
			body = strings.TrimSpace(n.Summary)
		}
		if body != "" {
			fmt.Fprintf(&sb, "  详述：%s\n", body)
		}
	}
	sb.WriteString("\n### 现有语义关系（请输出增删调整后的【最终集合】）\n")
	if len(existing) == 0 {
		sb.WriteString("（当前无关系）\n")
	}
	for _, e := range existing {
		fmt.Fprintf(&sb, "- %s --%s--> %s：%s\n",
			aliasByUUID[e.SourceID], e.Kind, aliasByUUID[e.TargetID], e.Description)
	}
	sb.WriteString("\n只返回 relations JSON。\n")
	if strings.TrimSpace(prevErr) != "" {
		fmt.Fprintf(&sb, "\n## 上一轮的问题（请修正后重试）\n- %s\n", prevErr)
	}
	return sb.String()
}

// ——————————————————————————————————————————————————————————————————————————————
// 小工具 / Small helpers
// ——————————————————————————————————————————————————————————————————————————————

// sortSubdomainKeys 稳定排序子域键（输出确定性）。
// sortSubdomainKeys sorts subdomain keys (determinism).
func sortSubdomainKeys(keys []SubdomainKey) {
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1].String() > keys[j].String(); j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
}

// sortNodesByID 按 ID 稳定排序节点（alias 分配确定性）。
// sortNodesByID sorts nodes by ID (deterministic alias assignment).
func sortNodesByID(nodes []*dktypes.Node) {
	for i := 1; i < len(nodes); i++ {
		for j := i; j > 0 && nodes[j-1].ID > nodes[j].ID; j-- {
			nodes[j-1], nodes[j] = nodes[j], nodes[j-1]
		}
	}
}
