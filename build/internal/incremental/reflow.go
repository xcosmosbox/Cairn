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
	"strings"

	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/parallel"
	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/llm"
)

// maxReflowRounds 是 I-6/I-7 单个 LLM 阶段的原地重试上限（与全量 relate 一致）。
// maxReflowRounds is the in-place retry cap for the I-6/I-7 LLM stages.
const maxReflowRounds = 2

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
	client    llm.Client
	maxTokens int
	st        *stores
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
	newMaterial string // 新融入 members 的 detail 聚合 / new members' detail material
}

// fuseDescriptions 执行 I-6：对 dirty.NodesFused 的 node 重融合 description。
// 按 subdomain 分组批量调用 LLM（与全量 describe 同构）；human_curated 跳过（R8）；
// 单子域失败降级（保持原 description），不阻断流水线。
//
// fuseDescriptions runs I-6: re-fuse descriptions for member-dirty nodes only,
// batched per subdomain; human_curated nodes are skipped (R8).
func (r *reflower) fuseDescriptions(ctx context.Context, dirty *DirtySet, ao *alignOutcome, rs *runState) map[string]string {
	out := make(map[string]string)
	if len(dirty.NodesFused) == 0 {
		return out
	}

	// 收集每个脏 node 的素材视图（human_curated 跳过）。
	bySub := make(map[SubdomainKey][]*reflowNode)
	for _, uuid := range dirty.SortedFusedNodes() {
		pn := ao.pending[uuid]
		if pn == nil {
			continue // 防御：脏集与 pending 不一致（不应发生）/ defensive
		}
		rn := &reflowNode{uuid: uuid}
		if pn.IsNew {
			// 新建 node：素材 = 全部 members（都是新单元）的 detail。
			rn.label = pn.Label
			rn.name = pn.Name
			rn.summary = pn.Summary
			rn.newMaterial = aggregateUnitDetails(pn.NewMembers, ao.unitsByID)
		} else {
			node, err := r.st.nodes.GetByID(ctx, uuid)
			if err != nil || node == nil {
				rs.warnf(fmt.Sprintf("I-6：脏 node %s 在 KG 中不存在（跳过重融合）", uuid))
				continue
			}
			// R8：human_curated 的 description 不被增量重融合覆盖。
			if node.Provenance == dktypes.ProvenanceHumanCurated {
				log.Printf("[incremental-reflow] node %s 为 human_curated，跳过重融合（R8）", uuid)
				continue
			}
			rn.label = string(node.Label)
			rn.name = node.Name
			rn.summary = node.Summary
			rn.existing = node.Description
			rn.newMaterial = aggregateUnitDetails(pn.NewMembers, ao.unitsByID)
			// 融入 node 无任何新素材（极端边界：member 的 detail 全空）→ 跳过，
			// 避免 LLM 对着空素材臆造（无新信息时不重写）。
			if rn.newMaterial == "" {
				continue
			}
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
	return out
}

// fuseSubdomainBatch 为一个子域内的脏 node 批量重融合 description（一次 LLM 调用，
// 门控重试：漏点/空 description 的节点进入下一轮伪 session 补写——与全量 describe 同构）。
// LLM 只见 alias（<subdomain-slug>#<序号>），uuid 只存在代码侧映射（R1）。
//
// fuseSubdomainBatch re-fuses one subdomain's dirty nodes in a single LLM call
// with a gating retry for missing/empty descriptions (full-pipeline semantics).
func (r *reflower) fuseSubdomainBatch(ctx context.Context, key SubdomainKey, nodes []*reflowNode, rs *runState) map[string]string {
	written := make(map[string]string)
	if len(nodes) == 0 {
		return written
	}
	// alias 分配（call-scoped）：<subdomain-slug>#<序号>。
	uuidByAlias := make(map[string]string)
	aliasByUUID := make(map[string]string)
	for i, n := range nodes {
		alias := fmt.Sprintf("%s#%d", key.Subdomain, i+1)
		uuidByAlias[alias] = n.uuid
		aliasByUUID[n.uuid] = alias
	}

	pending := nodes
	var lastErr string
	for round := 1; round <= maxReflowRounds; round++ {
		user := buildReflowPrompt(key, pending, aliasByUUID, lastErr)
		resp, err := r.client.Complete(ctx, llm.CompleteRequest{
			System:    reflowSystemPrompt,
			User:      user,
			MaxTokens: r.maxTokens,
		})
		if err != nil {
			rs.warnf(fmt.Sprintf("I-6 子域 %s LLM 调用失败（保持原 description）: %v", key, err))
			return written
		}
		if resp == nil {
			rs.warnf(fmt.Sprintf("I-6 子域 %s LLM 返回空响应（保持原 description）", key))
			return written
		}
		var out reflowOutput
		if perr := json.Unmarshal([]byte(stripAlignFence(strings.TrimSpace(resp.Text))), &out); perr != nil {
			lastErr = fmt.Sprintf("上一轮返回非法 JSON: %v", perr)
			continue
		}
		for _, d := range out.Descriptions {
			alias := strings.TrimSpace(d.NodeID)
			desc := strings.TrimSpace(d.Description)
			uuid, ok := uuidByAlias[alias]
			if !ok || desc == "" {
				continue // 忽略编造 alias / 空 description（门控下轮补）
			}
			written[uuid] = desc
		}
		var still []*reflowNode
		for _, n := range pending {
			if _, ok := written[n.uuid]; !ok {
				still = append(still, n)
			}
		}
		if len(still) == 0 {
			return written
		}
		pending = still
		var missing []string
		for _, n := range still {
			missing = append(missing, aliasByUUID[n.uuid])
		}
		lastErr = fmt.Sprintf("以下 %d 个节点尚未给出有效 description（漏掉或为空），必须逐一补写：%s",
			len(still), strings.Join(missing, "、"))
	}
	if len(pending) > 0 {
		rs.warnf(fmt.Sprintf("I-6 子域 %s %d 轮后仍有 %d 个节点缺 description（保持原值）",
			key, maxReflowRounds, len(pending)))
	}
	return written
}

// aggregateUnitDetails 聚合若干 member（04 id）的 detail 为带来源标记的素材文本
// （参照全量 aggregateMemberDetails 的风格）。
// aggregateUnitDetails concatenates member details with source markers.
func aggregateUnitDetails(memberIDs []string, unitsByID map[string]*alignUnit) string {
	var sb strings.Builder
	for _, m := range memberIDs {
		u := unitsByID[m]
		if u == nil || strings.TrimSpace(u.Detail) == "" {
			continue
		}
		fmt.Fprintf(&sb, "【成员 %s / 来源 %s】\n%s\n\n", m, u.FilePath, strings.TrimSpace(u.Detail))
	}
	return strings.TrimSpace(sb.String())
}

// reflowOutput 是 I-6 LLM 返回的 JSON。
// reflowOutput is the I-6 LLM JSON output.
type reflowOutput struct {
	Descriptions []struct {
		NodeID      string `json:"node_id"`
		Description string `json:"description"`
	} `json:"descriptions"`
}

// reflowSystemPrompt 是 I-6 增量重融合的 system 提示（参照全量 describeSystemPrompt，
// 增加「现有详述 + 新增素材」的增量语义）。
// reflowSystemPrompt is the I-6 system prompt (full-pipeline style + incremental).
const reflowSystemPrompt = `你是知识融合重写专家。用户会给你一个子域内若干需要更新的节点，
每个节点有名称、摘要、【现有详述】（可能为空）和【新增素材】（新融入来源单元的原文详述）。
你的任务是为每个节点产出一段新的完整详述（description）：

- 把新增素材融合进现有详述：现有详述中仍然成立的内容必须保留，新增素材中与现有内容
  重复的部分去重合并，互补的部分补充进去，保持语义连贯完整；
- 若现有详述为空，则直接依据新增素材写出完整详述；
- 绝不臆造素材与现有详述之外的信息，也不要混入其他节点的内容。

严格要求：
1. 只返回一个 JSON 对象（json 格式），形如 {"descriptions":[{"node_id":"...","description":"..."}]}，
   不要输出任何解释性文字或代码围栏。
2. node_id 必须逐字取自输入给出的节点 alias，不得编造或改写。
3. 必须为输入中的【每一个】节点都产出一条【非空】description——一个都不能遗漏、绝不允许留空。`

// buildReflowPrompt 组装 I-6 的 user 消息（子域内全部待重融合节点 + 上轮反馈）。
// buildReflowPrompt assembles the I-6 user message.
func buildReflowPrompt(key SubdomainKey, nodes []*reflowNode, aliasByUUID map[string]string, prevErr string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "## 子域：%s（所属域：%s）\n\n", key.Subdomain, key.Domain)
	sb.WriteString("请为下列每个节点，把【新增素材】融合进【现有详述】，产出新的完整详述：\n\n")
	for _, n := range nodes {
		fmt.Fprintf(&sb, "### 节点 alias=%s [%s]：%s\n", aliasByUUID[n.uuid], n.label, n.name)
		if strings.TrimSpace(n.summary) != "" {
			fmt.Fprintf(&sb, "摘要：%s\n", n.summary)
		}
		if strings.TrimSpace(n.existing) != "" {
			fmt.Fprintf(&sb, "现有详述：%s\n", n.existing)
		} else {
			sb.WriteString("现有详述：（空）\n")
		}
		sb.WriteString("新增素材：\n")
		if strings.TrimSpace(n.newMaterial) != "" {
			sb.WriteString(n.newMaterial)
		} else {
			sb.WriteString("（无）")
		}
		sb.WriteString("\n\n")
	}
	sb.WriteString("只返回 descriptions JSON。\n")
	if strings.TrimSpace(prevErr) != "" {
		fmt.Fprintf(&sb, "\n## 上一轮的问题（请修正后重试）\n- %s\n", prevErr)
	}
	return sb.String()
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
		seenEdge := make(map[string]bool)
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
				if seenEdge[edgeKey] {
					continue
				}
				seenEdge[edgeKey] = true
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
		// 因此：两端都未被本轮触碰、且被 LLM 遗漏的现存内部边，由代码补回。
		// 注意只补「遗漏」——若 LLM 返回了该边（即它确认保留），仍走原有路径，
		// 由 I-8 做字段级合并（允许更新 description/confidence，同时沿用旧边的
		// id/provenance/properties/created_at 等权威字段）。这样既堵住静默丢失，
		// 又不牺牲 LLM 对既有边的描述改进能力。
		//
		// 删边应通过 rebalance 的显式 drop_edge patch 表达（可观测），而不是靠
		// 重算时「少返回几条」隐式发生。
		missing := missingProtectedEdges(internal, touched, rels)
		finalRels[key] = rels
		// 补回的边以原始 *Edge 形态重插，保留全部列（避免 lossy-cast 成 Relation）。
		kept := append([]*dktypes.Edge(nil), cross...)
		keptEdges[key] = append(kept, missing...)
		if n := len(missing); n > 0 {
			log.Printf("[incremental-reflow] I-7 子域 %s: 补回 %d 条被 LLM 遗漏且与本轮无关的既有边（防静默丢失）", key, n)
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

// missingProtectedEdges 返回需要补回的现存内部边：两端都未被本轮触碰（与本轮变化
// 无关）、且未出现在 LLM 的最终集合里（被遗漏）。
//
// 只要有一端被触碰，该边就与本轮变化相关，LLM 保有完整裁量权（含删除）。
// 若 LLM 返回了该边，则不补回——交由 I-8 做字段级合并，保留 LLM 的描述更新。
//
// missingProtectedEdges returns existing intra-subdomain edges that are
// unrelated to this run's changes and were omitted by the LLM's final set.
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
		if e == nil || touched[e.SourceID] || touched[e.TargetID] {
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
// alias + 非自环；越界 kind / 非法 JSON 原地重试。
//
// relateOneSubdomain prune-recomputes one dirty subdomain's relations: the LLM
// emits the final relation set over aliased nodes, validated against the 6-kind
// enum and alias endpoints, with in-place retry.
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

	var lastErr string
	for round := 1; round <= maxReflowRounds; round++ {
		user := buildRelateRecomputePrompt(key, sortedNodes, existing, aliasByUUID, lastErr)
		resp, err := r.client.Complete(ctx, llm.CompleteRequest{
			System:    relateRecomputeSystemPrompt,
			User:      user,
			MaxTokens: r.maxTokens,
		})
		if err != nil {
			return nil, fmt.Errorf("LLM 调用失败: %w", err)
		}
		if resp == nil {
			return nil, fmt.Errorf("LLM 返回空响应")
		}
		var out relateRecomputeOutput
		if perr := json.Unmarshal([]byte(stripAlignFence(strings.TrimSpace(resp.Text))), &out); perr != nil {
			lastErr = fmt.Sprintf("上一轮返回非法 JSON: %v", perr)
			continue
		}
		rels, hasInvalidKind := translateRecomputedRelations(out.Relations, uuidByAlias)
		if hasInvalidKind {
			lastErr = "存在越界的 kind（必须是 6 个枚举之一），请修正后重试"
			// 最后一轮也不能把过滤后的空/部分集合当作权威成功结果；继续后
			// 由循环末尾返回 error，子域级调用方会降级保留全部现存内部边。
			continue
		}
		return rels, nil
	}
	return nil, fmt.Errorf("%d 轮仍未通过", maxReflowRounds)
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
// kind 越界丢弃并标记（触发重试）；端点未命中 alias（编造）或自环丢弃（绝不悬空，R5）；
// 同身份 (source,target,kind) 重复条目去重（问题 7：LLM 重复返回只落一条）。
// translateRecomputedRelations translates alias endpoints back to uuids, dropping
// invalid kinds (triggers retry), fabricated endpoints, self-loops (R5), and
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
		if !extract.IsValidRelationKind(r.Kind) {
			hasInvalidKind = true
			continue
		}
		src, okS := uuidByAlias[r.Source]
		tgt, okT := uuidByAlias[r.Target]
		if !okS || !okT || src == tgt {
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
