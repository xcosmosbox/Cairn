// Package incremental 的本文件实现 I-6（脏 node description 重融合）与 I-7
// （脏 subdomain relation 重算）——参照 extract.FuseDescriptions / IdentifyRelations
// 的 prompt 风格与门控语义，但只喂「脏集」子集输入（R9 脏集封闭）：
//
//   - I-6：仅对 dirty.NodesFused 的 node 重融合 description。
//     素材 = node 现有 description（已有 members 的融合沉淀——04 detail 不持久化，
//     增量场景以现有 description 为旧素材载体）+ 本轮新融入 members 的 detail（I-3 产出）。
//     human_curated 的 node 跳过（R8：人工内容绝不覆盖）。
//   - I-7：复用原文关系 helper；缺失输出不是删边授权，既有 Edge 完整保留。
//     带原文的入口核验两端来源并有限修复，旧入口只报告来源不可用。
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
	"io"
	"log"
	"sort"
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
	// verifiedNodeContent is opt-in: only a complete pair authorizes both fields.
	verifiedNodeContent map[string]extract.NodeDescriptionOutcome
	// relations 是每个脏子域重算后的「最终」语义 relation 集合（端点已是 uuid）。
	// relations holds the final recomputed semantic relations per dirty subdomain.
	relations map[SubdomainKey][]extract.Relation
	// keptCross 是每个脏子域保留的完整既有边（包括本域与跨域，原样保留重插）。
	// keptCross retains complete existing local/cross edges (verbatim unless new facets merge).
	keptCross map[SubdomainKey][]*dktypes.Edge
}

// reflower 封装 I-6/I-7 的依赖（llm.Client 注入，测试 mock）。
// reflower bundles I-6/I-7 dependencies (mockable llm.Client).
type reflower struct {
	client           llm.Client
	maxTokens        int
	st               *stores
	nodeMu           sync.Mutex
	nodeOutcomes     []extract.NodeDescriptionOutcome
	relationMu       sync.Mutex
	relationOutcomes []relationReflowOutcome
	relationReport   *extract.RelationProcessReport
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
	return r.fuseDescriptionsForPersistence(ctx, dirty, ao, rs, false)
}

// fuseDescriptionsForPersistence 明确传入 pair 能力；旧调用方继续保护无法应用的修正。
// Explicit local capability avoids changing shared reflower state during parallel work.
func (r *reflower) fuseDescriptionsForPersistence(ctx context.Context, dirty *DirtySet, ao *alignOutcome, rs *runState, allowPair bool) map[string]string {
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
			// 取消可能发生在读取前或读取中，不能把 caller 终止误报成存储不可用。
			// Preserve caller cancellation before classifying ordinary lookup failures.
			if ctxErr := ctx.Err(); ctxErr != nil {
				reason := fmt.Sprintf("I-6: existing node %s read cancelled: %v", uuid, ctxErr)
				r.recordNodeOutcome(extract.NodeDescriptionOutcome{ID: uuid, Domain: pn.Domain, Subdomain: pn.Subdomain, Outcome: annotation.DocumentCancelled, Reason: reason})
				rs.warnf(reason)
				continue
			}
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
		results[i] = sdResult{descByUUID: r.fuseSubdomainBatchForPersistence(ctx, key, bySub[key], rs, allowPair)}
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
	return r.fuseSubdomainBatchForPersistence(ctx, key, nodes, rs, false)
}

func (r *reflower) fuseSubdomainBatchForPersistence(ctx context.Context, key SubdomainKey, nodes []*reflowNode, rs *runState, allowPair bool) map[string]string {
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
			if !allowPair && !n.isNew && out.Summary != n.summary {
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
// I-7 原文核验与既有边保留 / Source-aware relations and complete old-edge retention
// ——————————————————————————————————————————————————————————————————————————————

// relationReflowOutcome 把“保留旧内容”与“核验新断言”分开，不能靠保留给推断补明示信用。
// Retention is a factual state, never proof that an old inference was explicit.
type relationReflowOutcome struct {
	Scope                 SubdomainKey
	EdgeID                int64
	Source, Target, Kind  string
	State, Classification string
	Outcome               annotation.DocumentOutcome
	Reason                string
	CandidateID           string
}

// relationReflowOutcomes / relationProcessReport 只暴露阶段事实；产品状态由 .6 汇总。
// These accessors expose stage facts, not product-level completion.
func (r *reflower) relationReflowOutcomes() []relationReflowOutcome {
	r.relationMu.Lock()
	defer r.relationMu.Unlock()
	return append([]relationReflowOutcome(nil), r.relationOutcomes...)
}
func (r *reflower) relationProcessReport() *extract.RelationProcessReport {
	r.relationMu.Lock()
	defer r.relationMu.Unlock()
	if r.relationReport == nil {
		return nil
	}
	// Reports contain nested evidence; do not let a reader rewrite the retained receipt.
	data, _ := json.Marshal(r.relationReport)
	var out extract.RelationProcessReport
	_ = json.Unmarshal(data, &out)
	return &out
}
func (r *reflower) recordRelationOutcome(out relationReflowOutcome) {
	r.relationMu.Lock()
	defer r.relationMu.Unlock()
	r.relationOutcomes = append(r.relationOutcomes, out)
}

// recomputeRelations 保留旧入口，但没有冻结原文时不能把派生 description 当明示证据。
// The legacy entry retains prior edges and reports unavailable source verification.
func (r *reflower) recomputeRelations(ctx context.Context, dirty *DirtySet, ao *alignOutcome,
	descriptions map[string]string, rs *runState) (map[SubdomainKey][]extract.Relation, map[SubdomainKey][]*dktypes.Edge) {
	return r.recomputeRelationsWithSources(ctx, dirty, ao, descriptions, nil, nil, extract.RelationProcessOptions{}, rs)
}

// recomputeRelationsWithSources 是 changed-document 的共享关系入口；调用者须提供两端的
// 全部标注成员绑定与冻结原文。它只把 source-explicit 的新选择加入结果，旧 Edge 在候选
// Result 之外独立保留。没有输出、unknown、传输错误都不是删边授权；人工正文不进模型。
// Source-aware changed construction reuses the full helper, with no duplicate retry loop.
func (r *reflower) recomputeRelationsWithSources(ctx context.Context, dirty *DirtySet, ao *alignOutcome,
	descriptions map[string]string, docs []*dktypes.AnnotatedDocument, originals []extract.FrozenSource,
	opts extract.RelationProcessOptions, rs *runState) (map[SubdomainKey][]extract.Relation, map[SubdomainKey][]*dktypes.Edge) {
	final := make(map[SubdomainKey][]extract.Relation)
	kept := make(map[SubdomainKey][]*dktypes.Edge)
	r.relationMu.Lock()
	r.relationOutcomes = nil
	r.relationReport = nil
	r.relationMu.Unlock()
	if dirty == nil {
		return final, kept
	}
	warn := func(msg string) {
		if rs != nil {
			rs.warnf(msg)
		}
	}
	fail := func(key SubdomainKey, reason string) {
		status := annotation.DocumentUnavailable
		if ctx.Err() != nil {
			status = annotation.DocumentCancelled
			reason = ctx.Err().Error() + ": " + reason
		}
		r.recordRelationOutcome(relationReflowOutcome{Scope: key, State: "unavailable", Classification: "unknown", Outcome: status, Reason: reason})
		warn("I-7 " + key.String() + ": " + reason)
	}
	if r.st == nil || r.st.nodes == nil || r.st.edges == nil {
		for _, key := range dirty.SortedSubdomains() {
			fail(key, "relation stores are unavailable")
		}
		return final, kept
	}
	active := make(map[SubdomainKey]bool)
	for _, key := range dirty.SortedSubdomains() {
		active[key] = true
	}
	touched := touchedNodeSet(dirty, ao, descriptions, rs)
	// 新来源可能改变“流入”脏节点的跨域边，不能只看本域出边而直接保留跨边。
	// Include affected incoming owners so both endpoint materials are revalidated.
	if len(originals) > 0 {
		ids := make([]string, 0, len(touched))
		for id := range touched {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			// Content-only changes and pending members can also introduce outgoing edges.
			if node, err := r.st.nodes.GetByID(ctx, id); err == nil && node != nil {
				active[SubdomainKey{node.Domain, node.Subdomain}] = true
			} else if ao != nil && ao.pending[id] != nil {
				pn := ao.pending[id]
				active[SubdomainKey{pn.Domain, pn.Subdomain}] = true
			}
			edges, err := r.st.edges.GetIncoming(ctx, id, 0)
			if err != nil {
				warn(fmt.Sprintf("I-7 incoming relation lookup %s: %v", id, err))
				continue
			}
			for _, edge := range edges {
				if edge.Provenance == dktypes.ProvenanceHumanCurated || !extract.IsValidRelationKind(string(edge.Kind)) {
					continue
				}
				node, err := r.st.nodes.GetByID(ctx, edge.SourceID)
				if err != nil || node == nil {
					warn(fmt.Sprintf("I-7 incoming source %s unavailable: %v", edge.SourceID, err))
					continue
				}
				key := SubdomainKey{node.Domain, node.Subdomain}
				active[key] = true
			}
		}
	}
	// 现有边不是跨域候选的目录：按调用者实际提供的文档身份补齐尚未相连的端点。
	// Supplement only supplied source identities; unrelated graph scopes stay outside discovery.
	eligible := make(map[SubdomainKey]bool, len(active))
	for key := range active {
		eligible[key] = true
	}
	if len(originals) > 0 && r.st.sources != nil {
		supplied := map[[2]string]bool{}
		for _, doc := range docs {
			if doc != nil {
				supplied[[2]string{doc.Skill, doc.FilePath}] = true
			}
		}
		rows, err := r.st.sources.ListAll(ctx)
		if err != nil {
			for key := range eligible {
				fail(key, fmt.Sprintf("discover supplied-source relation endpoints: %v", err))
			}
		} else {
			for _, row := range rows {
				if !supplied[[2]string{row.Skill, row.FilePath}] {
					continue
				}
				node, err := r.st.nodes.GetByID(ctx, row.NodeUUID)
				if err == nil && node != nil && (node.Label == dktypes.LabelEntity || node.Label == dktypes.LabelConcept) {
					active[SubdomainKey{node.Domain, node.Subdomain}] = true
				}
			}
		}
	}
	nodes := map[string]*dktypes.Node{}
	edgeGroups := map[SubdomainKey][]*dktypes.Edge{}
	keys := make([]SubdomainKey, 0, len(active))
	for key := range active {
		keys = append(keys, key)
	}
	sortSubdomainKeys(keys)
	for _, key := range keys {
		existing, err := r.st.nodes.ListBySubdomain(ctx, key.Domain, key.Subdomain)
		if err != nil {
			fail(key, fmt.Sprintf("read nodes: %v", err))
			continue
		}
		local := map[string]*dktypes.Node{}
		for _, node := range existing {
			if node.Label != dktypes.LabelEntity && node.Label != dktypes.LabelConcept {
				continue
			}
			c := *node
			local[c.ID] = &c
		}
		if ao != nil {
			for id, pn := range ao.pending {
				if pn == nil || !pn.IsNew || pn.Domain != key.Domain || pn.Subdomain != key.Subdomain {
					continue
				}
				local[id] = &dktypes.Node{ID: id, Label: dktypes.Label(pn.Label), Name: pn.Name, Summary: pn.Summary, Domain: pn.Domain, Subdomain: pn.Subdomain, Provenance: dktypes.ProvenanceLLMInferred}
			}
		}
		ids := make([]string, 0, len(local))
		for id := range local {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		var allEdges []*dktypes.Edge
		readOK := true
		for _, id := range ids {
			if d, ok := descriptions[id]; ok && local[id].Provenance != dktypes.ProvenanceHumanCurated {
				local[id].Description = d
			}
			outgoing, err := r.st.edges.GetOutgoing(ctx, id, 0)
			if err != nil {
				fail(key, fmt.Sprintf("read edges %s: %v", id, err))
				readOK = false
				break
			}
			for _, edge := range outgoing {
				if extract.IsValidRelationKind(string(edge.Kind)) {
					copyEdge := *edge
					allEdges = append(allEdges, &copyEdge)
				}
			}
		}
		if !readOK {
			continue
		}
		final[key] = nil
		edgeGroups[key] = allEdges
		// A human identity is protected authoritatively by I-8, including old duplicate rows.
		protected := map[string]bool{}
		for _, edge := range allEdges {
			if edge.Provenance == dktypes.ProvenanceHumanCurated {
				protected[reflowEdgeKey(edge.SourceID, edge.TargetID, string(edge.Kind))] = true
			}
		}
		for _, edge := range allEdges {
			state, classification, reason, status := "retained_old", "unknown", "original support is unavailable; existing edge retained", annotation.DocumentUnavailable
			if edge.Provenance == dktypes.ProvenanceLLMInferred {
				classification = "inferred"
			}
			if edge.Provenance == dktypes.ProvenanceHumanCurated {
				state = "retained_human"
				reason = "human-curated edge protected outside model input"
				status = annotation.DocumentComplete
			}
			r.recordRelationOutcome(relationReflowOutcome{Scope: key, EdgeID: edge.ID, Source: edge.SourceID, Target: edge.TargetID, Kind: string(edge.Kind), State: state, Classification: classification, Outcome: status, Reason: reason})
			if !protected[reflowEdgeKey(edge.SourceID, edge.TargetID, string(edge.Kind))] || edge.Provenance == dktypes.ProvenanceHumanCurated {
				kept[key] = append(kept[key], edge)
			}
		}
		for id, node := range local {
			nodes[id] = node
		}
		// A cross endpoint stays in its real scope. Its derived body is never an original.
		for _, edge := range allEdges {
			if edge.Provenance == dktypes.ProvenanceHumanCurated {
				continue
			}
			if nodes[edge.TargetID] != nil {
				continue
			}
			node, err := r.st.nodes.GetByID(ctx, edge.TargetID)
			if err != nil || node == nil {
				warn(fmt.Sprintf("I-7 target %s unavailable: %v", edge.TargetID, err))
				continue
			}
			c := *node
			if d, ok := descriptions[c.ID]; ok && c.Provenance != dktypes.ProvenanceHumanCurated {
				c.Description = d
			}
			nodes[c.ID] = &c
		}
	}
	if len(originals) == 0 {
		report := &extract.RelationProcessReport{}
		for _, key := range keys {
			if _, ok := final[key]; ok {
				reason := "frozen original sources unavailable; retained old relations, no new relation certified"
				fail(key, reason)
				outcome := annotation.DocumentUnavailable
				if ctx.Err() != nil {
					outcome = annotation.DocumentCancelled
					reason = ctx.Err().Error()
				}
				report.Scopes = append(report.Scopes, extract.RelationScopeOutcome{Scope: key.String(), Outcome: outcome, Reason: reason})
			}
		}
		r.relationMu.Lock()
		r.relationReport = report
		r.relationMu.Unlock()
		return final, kept
	}
	// Build a call-scoped graph without persistent IDs or human-curated prose.
	ids := make([]string, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	aliasByID, idByAlias := map[string]string{}, map[string]string{}
	for i, id := range ids {
		alias := fmt.Sprintf("relation-node-%d", i+1)
		aliasByID[id] = alias
		idByAlias[alias] = id
	}
	graph := &extract.Result{}
	scopeIndex := map[SubdomainKey][2]int{}
	addScope := func(key SubdomainKey) *extract.Subdomain {
		if index, ok := scopeIndex[key]; ok {
			return &graph.Domains[index[0]].Subdomains[index[1]]
		}
		di := -1
		for i := range graph.Domains {
			if graph.Domains[i].Slug == key.Domain {
				di = i
				break
			}
		}
		if di < 0 {
			graph.Domains = append(graph.Domains, extract.Domain{Name: key.Domain, Slug: key.Domain})
			di = len(graph.Domains) - 1
		}
		graph.Domains[di].Subdomains = append(graph.Domains[di].Subdomains, extract.Subdomain{Name: key.Subdomain, Slug: key.Subdomain})
		si := len(graph.Domains[di].Subdomains) - 1
		scopeIndex[key] = [2]int{di, si}
		return &graph.Domains[di].Subdomains[si]
	}
	for _, id := range ids {
		node := nodes[id]
		key := SubdomainKey{node.Domain, node.Subdomain}
		en := extract.Node{ID: aliasByID[id], Label: string(node.Label), Name: node.Name, Summary: node.Summary, Description: node.Description, Confidence: node.Confidence}
		if node.Provenance == dktypes.ProvenanceHumanCurated {
			en.Name = "protected endpoint"
			en.Summary = ""
			en.Description = ""
		}
		members := map[string]bool{}
		memberReadOK := r.st.sources != nil
		if r.st.sources != nil {
			rows, err := r.st.sources.ListByNode(ctx, id)
			if err != nil {
				fail(key, fmt.Sprintf("read source members %s: %v", id, err))
				memberReadOK = false
			} else {
				for _, row := range rows {
					members[row.MemberID] = true
				}
			}
		}
		if ao != nil {
			for _, row := range ao.sourceRows {
				if row.NodeUUID == id {
					members[row.MemberID] = true
				}
			}
			if pn := ao.pending[id]; pn != nil {
				for _, member := range pn.NewMembers {
					members[member] = true
				}
			}
		}
		for member := range members {
			if memberReadOK && member != "" {
				en.Members = append(en.Members, member)
			}
		}
		sort.Strings(en.Members)
		sd := addScope(key)
		if node.Label == dktypes.LabelEntity {
			sd.Entities = append(sd.Entities, en)
		} else {
			sd.Concepts = append(sd.Concepts, en)
		}
	}
	human := map[string]bool{}
	for _, key := range keys {
		edges, ok := edgeGroups[key]
		if !ok {
			continue
		}
		sd := addScope(key)
		for _, edge := range edges {
			identity := reflowEdgeKey(edge.SourceID, edge.TargetID, string(edge.Kind))
			if edge.Provenance == dktypes.ProvenanceHumanCurated {
				human[identity] = true
				continue
			}
			sd.Relations = append(sd.Relations, extract.Relation{Source: aliasByID[edge.SourceID], Target: aliasByID[edge.TargetID], Kind: string(edge.Kind), Description: edge.Description, Confidence: edge.Confidence})
		}
	}
	report := extract.NewExtractor(r.client, r.maxTokens).ProcessRelations(ctx, graph, docs, originals, opts)
	r.relationMu.Lock()
	r.relationReport = report
	r.relationMu.Unlock()
	for _, scope := range report.Scopes {
		if scope.Outcome != annotation.DocumentComplete {
			warn(fmt.Sprintf("I-7 %s: %s (%s)", scope.Scope, scope.Reason, scope.Outcome))
		}
	}
	// Existing candidates are audited separately; source verification never upgrades
	// their retained inference classification to explicit.
	for _, decision := range report.Decisions {
		source, target := idByAlias[decision.Source.Alias], idByAlias[decision.Target.Alias]
		if source == "" || target == "" {
			continue
		}
		key := SubdomainKey{nodes[source].Domain, nodes[source].Subdomain}
		if _, ok := final[key]; !ok {
			continue
		}
		if decision.Classification != "explicit" || decision.Outcome != annotation.DocumentComplete {
			for _, edge := range edgeGroups[key] {
				if edge.SourceID == source && edge.TargetID == target && string(edge.Kind) == decision.Relation.Kind && edge.Description == decision.Relation.Description && edge.Provenance != dktypes.ProvenanceHumanCurated {
					classification := "unknown"
					if edge.Provenance == dktypes.ProvenanceLLMInferred {
						classification = "inferred"
					}
					r.relationMu.Lock()
					for i := range r.relationOutcomes {
						if r.relationOutcomes[i].Scope == key && r.relationOutcomes[i].EdgeID == edge.ID {
							r.relationOutcomes[i] = relationReflowOutcome{Scope: key, EdgeID: edge.ID, Source: source, Target: target, Kind: string(edge.Kind), State: "retained_old_audited", Classification: classification, Outcome: decision.Outcome, Reason: decision.Reason, CandidateID: decision.CandidateID}
						}
					}
					r.relationMu.Unlock()
				}
			}
		}
	}
	grouped := map[SubdomainKey]map[string][]extract.RelationProcessDecision{}
	for _, decision := range report.Selected {
		if decision.Classification != "explicit" || decision.Outcome != annotation.DocumentComplete {
			continue
		}
		source, target := idByAlias[decision.Source.Alias], idByAlias[decision.Target.Alias]
		if source == "" || target == "" {
			continue
		}
		key := SubdomainKey{nodes[source].Domain, nodes[source].Subdomain}
		if _, ok := final[key]; !ok {
			continue
		}
		if !eligible[key] && !touched[source] && !touched[target] {
			continue
		}
		identity := reflowEdgeKey(source, target, decision.Relation.Kind)
		if human[identity] {
			r.recordRelationOutcome(relationReflowOutcome{Scope: key, Source: source, Target: target, Kind: decision.Relation.Kind, State: "verified_new_unapplied", Classification: "explicit", Outcome: annotation.DocumentPartial, Reason: "source-verified addition conflicts with protected human edge identity; human content retained", CandidateID: decision.CandidateID})
			continue
		}
		if grouped[key] == nil {
			grouped[key] = map[string][]extract.RelationProcessDecision{}
		}
		grouped[key][identity] = append(grouped[key][identity], decision)
		r.recordRelationOutcome(relationReflowOutcome{Scope: key, Source: source, Target: target, Kind: decision.Relation.Kind, State: "verified_new", Classification: "explicit", Outcome: decision.Outcome, Reason: decision.Reason, CandidateID: decision.CandidateID})
	}
	for key, groups := range grouped {
		identities := make([]string, 0, len(groups))
		for identity := range groups {
			identities = append(identities, identity)
		}
		sort.Strings(identities)
		for _, identity := range identities {
			decisions := groups[identity]
			first := decisions[0]
			source, target := idByAlias[first.Source.Alias], idByAlias[first.Target.Alias]
			description := coalesceReflowAssertions(edgeGroups[key], source, target, first.Relation.Kind, decisions)
			confidence := first.Relation.Confidence
			for _, edge := range edgeGroups[key] {
				if reflowEdgeKey(edge.SourceID, edge.TargetID, string(edge.Kind)) == identity {
					confidence = edge.Confidence
					break
				}
			}
			final[key] = append(final[key], extract.Relation{Source: source, Target: target, Kind: first.Relation.Kind, Description: description, Confidence: confidence})
		}
	}
	// I-8 now preserves every legacy row and applies the merged facets to one stable row.
	// 原旧多行保护由持久化事务实现；无需丢弃已核验的新选择。
	for key := range final {
		if !eligible[key] && len(grouped[key]) == 0 {
			delete(final, key)
		}
	}
	// Incoming/supplemental owners are dirtied only after a complete read and a relevant selection.
	for key := range final {
		if !dirty.Subdomains[key] {
			dirty.DirtySubdomain(key)
		}
	}
	return final, kept
}

func reflowEdgeKey(source, target, kind string) string {
	return source + "\x00" + target + "\x00" + kind
}

// reflowAssertionEnvelope 适配既有单三元组存储形态；每个来源/限定断言独立保留。
// This existing description field carries labeled facets, without inventing schema/provenance.
type reflowAssertionEnvelope struct {
	Assertions []reflowAssertion `json:"cairn_relation_assertions_v1"`
}
type reflowAssertion struct {
	Classification string                  `json:"classification"`
	Description    string                  `json:"description"`
	Sources        []extract.CrossSource   `json:"sources,omitempty"`
	Evidence       []extract.CrossEvidence `json:"evidence,omitempty"`
}

// coalesceReflowAssertions 不信任旧机器正文里的自报标签；只有本轮 Selected 的精确
// 描述/来源/逐字证据组合有明示信用。其他旧字段保留为未核验历史，不能借 JSON 洗成事实。
// Prior envelope labels are data; only an exact currently verified facet grants explicit credit.
func coalesceReflowAssertions(old []*dktypes.Edge, source, target, kind string, selected []extract.RelationProcessDecision) string {
	current := map[string]reflowAssertion{}
	for _, decision := range selected {
		if decision.Classification != "explicit" || decision.Outcome != annotation.DocumentComplete {
			continue
		}
		assertion := reflowAssertion{Classification: "explicit", Description: decision.Relation.Description, Sources: decision.Sources, Evidence: decision.Evidence}
		key, normalized := canonicalReflowAssertion(assertion)
		current[key] = normalized
	}
	encoded := map[string]reflowAssertion{}
	retainHistorical := func(assertion reflowAssertion) {
		assertion.Classification = "legacy_unverified"
		key, normalized := canonicalReflowAssertion(assertion)
		// Reverified identical facets replace their historical label, without growing duplicates.
		if _, verified := current[key]; !verified {
			encoded[key] = normalized
		}
	}
	for _, edge := range old {
		if reflowEdgeKey(edge.SourceID, edge.TargetID, string(edge.Kind)) != reflowEdgeKey(source, target, kind) {
			continue
		}
		var prior reflowAssertionEnvelope
		decoder := json.NewDecoder(strings.NewReader(edge.Description))
		decoder.DisallowUnknownFields()
		// Non-format legacy JSON may contain meaningful extra fields; preserve it whole.
		if decoder.Decode(&prior) == nil && decoder.Decode(&struct{}{}) == io.EOF && len(prior.Assertions) > 0 {
			for _, assertion := range prior.Assertions {
				retainHistorical(assertion)
			}
		} else {
			retainHistorical(reflowAssertion{Description: edge.Description})
		}
	}
	for key, assertion := range current {
		encoded[key] = assertion
	}
	keys := make([]string, 0, len(encoded))
	for key := range encoded {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	envelope := reflowAssertionEnvelope{}
	for _, key := range keys {
		envelope.Assertions = append(envelope.Assertions, encoded[key])
	}
	data, _ := json.Marshal(envelope)
	return string(data)
}

// canonicalReflowAssertion 的身份不含信用标签，也不依赖 verifier 的来源/证据数组顺序。
// Classification is assigned from current verification, independently of facet identity.
func canonicalReflowAssertion(assertion reflowAssertion) (string, reflowAssertion) {
	assertion.Sources = append([]extract.CrossSource(nil), assertion.Sources...)
	assertion.Evidence = append([]extract.CrossEvidence(nil), assertion.Evidence...)
	sort.Slice(assertion.Sources, func(i, j int) bool {
		a, _ := json.Marshal(assertion.Sources[i])
		b, _ := json.Marshal(assertion.Sources[j])
		return string(a) < string(b)
	})
	sort.Slice(assertion.Evidence, func(i, j int) bool {
		a, _ := json.Marshal(assertion.Evidence[i])
		b, _ := json.Marshal(assertion.Evidence[j])
		return string(a) < string(b)
	})
	identity := assertion
	identity.Classification = ""
	data, _ := json.Marshal(identity)
	return string(data), assertion
}

// touchedNodeSet 汇总「本轮被触碰」的 node uuid：新增/融入（ao.pending、
// DirtySet.NodesFused）、内容编辑（DirtySet.NodesContent、runState.touched）、
// 正文重融合（I-6 descriptions）。与这些 node 都无关的边即「本轮不涉及」。
//
// 并集只决定需要核验的范围；它不再赋予模型通过省略删除机器边的权限。
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
