// Package incremental 的本文件实现数据闭环第三块地基：全量重整（rebalance）编排器。
//
// 背景：人机协同持续增量（cairn-incremental）导致 KG 结构漂移——子域碎片化、
// node 重复、归属错乱、连边爆炸。本编排器做「保面积的全局化简」：
//
//	R-0 入口校验（复用第二块 preflight：repo 是目录、db 已存在、kb_version 非空、
//	    sidecar↔DB uuid 有交集）；
//	R-1 快照当前 KG（跳过标注：直接拿已带归属的结构，不读原始文档）；
//	R-2 复杂度哨兵 + 触发判定（现状 Q / 单例率 / 边节点比 / 累计改动占比，零 LLM）；
//	R-3 Louvain 社区检测（纯 CPU，只读图产建议社区，绝不改图，R-louvain-readonly）；
//	R-4 全图 alias 组装 + LLM 语义微调 → 结构 patch（唯一 LLM 环节，R1：uuid 零 prompt）；
//	R-5 代码应用 patch（逐操作直改 KG + 填 runState/alignOutcome/DirtySet，归属列先落库）；
//	R-6 脏集重算（复用第二块 I-6/I-7 reflower，仅脏集，human_curated 跳过）；
//	R-7 局部 upsert 入库（复用 I-8 applyIncrementalIngest，绝不删库重建）；
//	R-8 回写（复用 I-9）+ 重置累计改动量 + 写 rebalance.last 可观测键。
//
// 铁律落实：
//   - R1 uuid 零 prompt：R-4 及复用的 I-6/I-7 全程 alias 呈现；重试反馈固定文案，
//     绝不回显 LLM 原始输出；
//   - R2 uuid 钉死：只有 split 才 NodeUUID 首派生；merge 保留 members 最多者旧 uuid；
//     remigrate/rename/子域变动绝不换 uuid；
//   - R5 绝不悬空边：删 node 必 DeleteBySource+DeleteByTarget+取 RowID 填 rs.deleted；
//   - R8 human_curated：merge 含人工成分 → survivor 继承且 description 以人工版为基底，
//     provenance 在 fuseDescriptions 之前落库；
//   - R9 脏集封闭：未触及的 node/edge/subdomain/description 字节不变；
//   - R-lineage：只有 merge/split 写 uuid_lineage。
//
// This file implements the rebalance orchestrator (R-0..R-8): sentinel-triggered,
// Louvain-prior, LLM-refined global simplification of the KG via local upserts,
// reusing the incremental pipeline's reflow/ingest/writeback stages verbatim.
package incremental

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
	"github.com/xcosmosbox/cairn/build/internal/extract"
	"github.com/xcosmosbox/cairn/build/internal/llm"
	"github.com/xcosmosbox/cairn/build/internal/rebalance/community"
	"github.com/xcosmosbox/cairn/core/metrics"
	"github.com/xcosmosbox/cairn/build/internal/rebalance/patch"
)

// maxRebalanceRounds 是 R-4 重整判定 LLM 调用的原地重试上限。
// maxRebalanceRounds is the in-place retry cap for the R-4 LLM call.
const maxRebalanceRounds = 2

// RebalanceRunOpts 配置一轮重整运行。
// RebalanceRunOpts configures one rebalance run.
type RebalanceRunOpts struct {
	// Force 跳过哨兵强制重整。
	Force bool
	// CheckOnly 只算指标+建议（零 LLM、零改图）。
	CheckOnly bool
	// Thresholds 是哨兵阈值（零值取默认）。
	Thresholds metrics.Thresholds
}

// RebalanceReport 是一轮重整的结果摘要（可观测）。
// RebalanceReport summarizes one rebalance run.
type RebalanceReport struct {
	CheckOnly       bool     // 是否 --check 只读模式
	Triggered       bool     // 是否判定需要重整（哨兵越界或 force）
	TriggerReasons  []string // 触发原因（越界明细 / force）
	NoOp            bool     // 是否未改图（未触发 / 空 patch / LLM 耗尽降级）
	NoOpReason      string   // no-op 原因
	Metrics         metrics.Snapshot
	CumulativeRatio float64 // kg_manifest 的累计改动占比
	LouvainQ        float64 // Louvain 划分模块度
	Communities     int     // Louvain 社区数（≥2 成员的非单例社区）
	PatchOps        int     // 应用的 patch 操作数
	OpsByKind       map[string]int
	// 操作分类计数（报告/测试断言用）。
	NodesMerged     int // 被合并消失的 dead node 数
	NodesSplitNew   int // split 新派生的 node 数
	NodesRemigrated int
	SubdomainsDirty int
	EdgesDropped    int
	Renames         int
	LineageWritten  int
	DocsAffected    int
	DocsRewritten   int
	Warnings        []string
	Version         string // 本轮 Bump 的 KB 版本（no-op 为当前版本）
}

// RebalanceOrchestrator 是全量重整流水线的编排器。
// RebalanceOrchestrator orchestrates the rebalance pipeline.
type RebalanceOrchestrator struct {
	client    llm.Client
	maxTokens int
	// beforeIngest 是包内测试的故障注入钩子（生产为 nil）。
	beforeIngest func(context.Context) error
}

// NewRebalanceOrchestrator 组装重整编排器。client 不可为 nil。
// NewRebalanceOrchestrator assembles the orchestrator. client must not be nil.
func NewRebalanceOrchestrator(client llm.Client, maxTokens int) (*RebalanceOrchestrator, error) {
	if client == nil {
		return nil, fmt.Errorf("rebalance: llm client is nil")
	}
	return &RebalanceOrchestrator{client: client, maxTokens: maxTokens}, nil
}

// Run 执行一轮全量重整（R-0..R-8）。repoPath 是仓库根；dbPath 是既有 KG 库
// （局部 upsert，绝不 os.Remove 删库重建）。
//
// Run executes one rebalance round over the existing KG at dbPath.
func (o *RebalanceOrchestrator) Run(ctx context.Context, repoPath, dbPath string, opts RebalanceRunOpts) (*RebalanceReport, error) {
	rpt := &RebalanceReport{CheckOnly: opts.CheckOnly, OpsByKind: map[string]int{}}

	// ══════════ R-0 入口校验（复用第二块 preflight，语义一致）══════════
	if err := preflightPaths(repoPath, dbPath); err != nil {
		return nil, err
	}
	preflightDB, err := storage.OpenPreflightReadOnly(dbPath)
	if err != nil {
		return nil, fmt.Errorf("rebalance: 只读验证 db %s: %w", dbPath, err)
	}
	preflightErr := preflightKGForRebalance(ctx, newStores(preflightDB), repoPath)
	closeErr := preflightDB.Close()
	if preflightErr != nil {
		return nil, preflightErr
	}
	if closeErr != nil {
		return nil, fmt.Errorf("rebalance: 关闭只读预检 db %s: %w", dbPath, closeErr)
	}

	db, err := storage.NewDB(storage.DBOptions{Path: dbPath})
	if err != nil {
		return nil, fmt.Errorf("rebalance: open db %s: %w", dbPath, err)
	}
	defer db.Close()
	st := newStores(db)
	rs := newRunState()

	// ══════════ R-1 快照当前 KG（跳过标注，不读原始文档）══════════
	log.Printf("══════════ R-1 快照 KG / snapshot ══════════")
	graph, err := metrics.LoadGraph(ctx, st.nodes, st.edges)
	if err != nil {
		return nil, fmt.Errorf("rebalance: snapshot: %w", err)
	}
	if len(graph.Nodes) == 0 {
		rpt.NoOp = true
		rpt.NoOpReason = "KG 无 entity/concept 节点，无需重整"
		return rpt, nil
	}

	// ══════════ R-2 复杂度哨兵 + 触发判定（零 LLM、零 Louvain 迭代）══════════
	snap := metrics.Compute(graph)
	rpt.Metrics = snap
	cumulative, cumulativeDocs := readCumulativeChangeRatio(ctx, st)
	rpt.CumulativeRatio = cumulative
	reasons := metrics.Breach(snap, cumulative, cumulativeDocs, opts.Thresholds)
	if opts.Force {
		reasons = append(reasons, "--force 强制重整")
	}
	rpt.TriggerReasons = reasons
	rpt.Triggered = len(reasons) > 0
	log.Printf("[rebalance] R-2 哨兵: Q=%.3f 单例率=%.3f 边节点比=%.3f 累计改动=%.3f（%d 篇）→ 触发=%v",
		snap.ModularityQ, snap.SingletonRatio, snap.EdgeNodeRatio, cumulative, cumulativeDocs, rpt.Triggered)

	if opts.CheckOnly {
		return rpt, nil // --check：只读，零 LLM 零改图
	}
	if !rpt.Triggered {
		rpt.NoOp = true
		rpt.NoOpReason = "哨兵未越阈值，无需重整"
		return rpt, nil
	}

	// FTS 孤儿自愈（在触发判定之后、任何改图之前）：上一轮若在 R-5 之后、I-8
	// 之前失败，被删 node 的 FTS 行可能残留（上轮 rs.deleted 未及清理）；幂等
	// DELETE 保证重跑收敛（可重入，坑点 6）。--check / 未触发路径保持只读不经过此处。
	if err := st.fts.DeleteOrphans(ctx); err != nil {
		return rpt, fmt.Errorf("rebalance: fts orphan sweep: %w", err)
	}

	// ══════════ R-3 Louvain 社区检测（纯 CPU，只读，绝不改图）══════════
	log.Printf("══════════ R-3 Louvain 社区检测 / community ══════════")
	comm := detectCommunities(graph)
	rpt.LouvainQ = comm.Q
	for _, members := range comm.Communities {
		if len(members) >= 2 {
			rpt.Communities++
		}
	}
	log.Printf("[rebalance] R-3: 社区 %d 个（非单例 %d），划分 Q=%.3f",
		len(comm.Communities), rpt.Communities, comm.Q)

	// ══════════ R-4 全图 alias + LLM 语义微调 → patch（唯一 LLM 环节）══════════
	log.Printf("══════════ R-4 LLM 重整判定 / judge ══════════")
	rb, err := o.judgeRebalance(ctx, st, graph, comm, snap, cumulative, rs)
	if err != nil {
		// LLM 重试耗尽：降级 no-op（不改图，报告失败，绝不半改）。
		rpt.NoOp = true
		rpt.NoOpReason = fmt.Sprintf("R-4 LLM 判定失败（降级不改图）: %v", err)
		rpt.Warnings = rs.warningsSnapshot()
		rs.warnf(rpt.NoOpReason)
		return rpt, nil
	}
	if rb == nil || len(rb.p.Ops) == 0 {
		rpt.NoOp = true
		rpt.NoOpReason = "LLM 判定当前结构已最优（空 patch）"
		return rpt, nil // 空 patch：不改图、不 Bump、不重置（坑点 13 no-op 收敛）
	}

	// ══════════ R-5 代码应用 patch（逐操作直改 KG + 填 rs/ao/dirty）══════════
	log.Printf("══════════ R-5 应用 patch / apply（%d 个操作）══════════", len(rb.p.Ops))
	ao := &alignOutcome{pending: map[string]*pendingNode{}, unitsByID: map[string]*alignUnit{}}
	applier := &rebalanceApplier{
		st: st, rs: rs, ao: ao, book: rb.book, louvain: comm,
		membersByUUID: rb.membersByUUID, now: time.Now().UTC(), rpt: rpt,
	}
	for i := range rb.p.Ops {
		op := &rb.p.Ops[i]
		if err := applier.apply(ctx, op); err != nil {
			return rpt, fmt.Errorf("rebalance: 应用第 %d 个操作（%s）失败: %w", i+1, op.Op, err)
		}
	}
	rpt.PatchOps = len(rb.p.Ops)
	rpt.LineageWritten = applier.lineageWritten
	rpt.SubdomainsDirty = len(rs.dirty.Subdomains)

	// ══════════ R-6 脏集重算（复用第二块 I-6/I-7，仅脏集）══════════
	rr := &reflowResult{
		descriptions: map[string]string{},
		relations:    map[SubdomainKey][]extract.Relation{},
		keptCross:    map[SubdomainKey][]*dktypes.Edge{},
	}
	if !rs.dirty.Empty() {
		log.Printf("══════════ R-6 重融合+relation 重算 / reflow（脏 node %d，脏子域 %d）══════════",
			len(rs.dirty.NodesFused), len(rs.dirty.Subdomains))
		rf := newReflower(o.client, o.maxTokens, st)
		rr.descriptions = rf.fuseDescriptions(ctx, rs.dirty, ao, rs)
		rr.relations, rr.keptCross = rf.recomputeRelations(ctx, rs.dirty, ao, rr.descriptions, rs)
	}
	if err := requirePendingRelationResults(ao, rr); err != nil {
		return rpt, fmt.Errorf("rebalance: reflow: %w", err)
	}

	// ══════════ R-7 局部 upsert 入库（复用 I-8，绝不删库重建）══════════
	if o.beforeIngest != nil {
		if err := o.beforeIngest(ctx); err != nil {
			return rpt, fmt.Errorf("rebalance: before R-7: %w", err)
		}
	}
	log.Printf("══════════ R-7 局部 upsert / ingest ══════════")
	if err := applyIncrementalIngest(ctx, st, rs.dirty, ao, rr, rs); err != nil {
		return rpt, fmt.Errorf("rebalance: ingest: %w", err)
	}

	// ══════════ R-8 回写 + 重置累计计数（复用 I-9；回写锚随 node_sources 重指向
	// 自然带出新 uuid——merge/split 的全部来源文档均已 affectDocs）══════════
	var writebackErr error
	log.Printf("══════════ R-8 回写 / writeback（受影响文档 %d）══════════", len(rs.affectedDocs))
	wrpt, werr := applyIncrementalWriteback(ctx, st, rs, ao, nil, repoPath)
	if werr != nil {
		// 部分失败：成功部分照常，向上报告（与第二块 I-9 失败语义一致）。
		writebackErr = werr
		log.Printf("[rebalance] ⚠ R-8 部分失败: %v", werr)
	}
	if wrpt != nil {
		rpt.DocsRewritten = wrpt.DocsRewritten
	}
	rpt.DocsAffected = len(rs.affectedDocsSnapshot())

	// 重置累计改动量（坑点 14：只有真的应用了非空 patch 才重置）+ 可观测键。
	resetCumulativeChangeStats(ctx, st, rs)
	writeRebalanceManifest(ctx, st, rpt, rs)

	rpt.Warnings = rs.warningsSnapshot()
	if v, err := st.version.Current(ctx); err == nil {
		rpt.Version = v
	}
	log.Printf("[rebalance] ✓ 完成: patch 操作 %d（merge %d/split新 %d/remigrate %d/drop %d/rename %d），"+
		"lineage %d，重写文档 %d，告警 %d",
		rpt.PatchOps, rpt.NodesMerged, rpt.NodesSplitNew, rpt.NodesRemigrated, rpt.EdgesDropped,
		rpt.Renames, rpt.LineageWritten, rpt.DocsRewritten, len(rpt.Warnings))
	return rpt, writebackErr
}

// preflightKGForRebalance 复用第二块的 KG 预检实现（同包未导出方法，语义零改动）。
// preflightKGForRebalance reuses the incremental preflight verbatim.
func preflightKGForRebalance(ctx context.Context, st *stores, repoPath string) error {
	return (&IncrementalOrchestrator{}).preflightKG(ctx, st, repoPath)
}

// detectCommunities 在语义边子图上跑 Louvain（边权恒 1；只读，R-louvain-readonly）。
// detectCommunities runs Louvain over the semantic subgraph (unit weights, read-only).
func detectCommunities(g *metrics.SemanticGraph) *community.Result {
	nodeIDs := make([]string, 0, len(g.Nodes))
	for id := range g.Nodes {
		nodeIDs = append(nodeIDs, id)
	}
	edges := make([]community.Edge, 0, len(g.Edges))
	for _, e := range g.Edges {
		edges = append(edges, community.Edge{SourceID: e.SourceID, TargetID: e.TargetID, Weight: 1})
	}
	return community.Detect(nodeIDs, edges)
}

// readCumulativeChangeRatio 读 kg_manifest 的累计改动占比与累计改动文档数
// （无记录为 0）。占比用于比例判据，篇数用于绝对量下限（小文档集防误触发）。
// readCumulativeChangeRatio reads the cumulative change ratio and doc count.
func readCumulativeChangeRatio(ctx context.Context, st *stores) (float64, int) {
	raw, err := st.manifest.Get(ctx, manifestStatsKey)
	if err != nil || raw == "" {
		return 0, 0
	}
	var stats incrementalStats
	if json.Unmarshal([]byte(raw), &stats) != nil {
		return 0, 0
	}
	return stats.CumulativeChangeRatio, stats.CumulativeDocsChanged
}

// resetCumulativeChangeStats 重置累计改动量（重整成功后从新基线重新累计，坑点 14）。
// 保留 runs 等其它字段；写失败只告警（可观测产物，不阻断）。
// resetCumulativeChangeStats zeroes the cumulative counters after a successful
// rebalance, preserving other fields.
func resetCumulativeChangeStats(ctx context.Context, st *stores, rs *runState) {
	raw, err := st.manifest.Get(ctx, manifestStatsKey)
	if err != nil || raw == "" {
		return // 无记录：无需重置
	}
	var stats incrementalStats
	if json.Unmarshal([]byte(raw), &stats) != nil {
		rs.warnf("rebalance: 解析 incremental.stats 失败（跳过重置）")
		return
	}
	stats.CumulativeDocsChanged = 0
	stats.CumulativeChangeRatio = 0
	data, err := json.Marshal(&stats)
	if err != nil {
		rs.warnf(fmt.Sprintf("rebalance: 序列化 incremental.stats 失败: %v", err))
		return
	}
	if err := st.manifest.Set(ctx, manifestStatsKey, string(data)); err != nil {
		rs.warnf(fmt.Sprintf("rebalance: 重置 incremental.stats 失败: %v", err))
	}
}

// rebalanceLastKey 是重整摘要在 kg_manifest 的可观测键。
// rebalanceLastKey is the kg_manifest key for the last rebalance summary.
const rebalanceLastKey = "rebalance.last"

// writeRebalanceManifest 把本次重整摘要写入 kg_manifest（可观测；失败只告警）。
// writeRebalanceManifest records a rebalance summary for observability.
func writeRebalanceManifest(ctx context.Context, st *stores, rpt *RebalanceReport, rs *runState) {
	summary := map[string]interface{}{
		"last_run_at":     time.Now().UTC().Format(time.RFC3339),
		"trigger_reasons": rpt.TriggerReasons,
		"louvain_q":       rpt.LouvainQ,
		"communities":     rpt.Communities,
		"patch_ops":       rpt.PatchOps,
		"ops_by_kind":     rpt.OpsByKind,
		"lineage_written": rpt.LineageWritten,
		"docs_rewritten":  rpt.DocsRewritten,
		"version":         rpt.Version,
	}
	data, err := json.Marshal(summary)
	if err != nil {
		rs.warnf(fmt.Sprintf("rebalance: 序列化 rebalance.last 失败: %v", err))
		return
	}
	if err := st.manifest.Set(ctx, rebalanceLastKey, string(data)); err != nil {
		rs.warnf(fmt.Sprintf("rebalance: 写入 rebalance.last 失败: %v", err))
	}
}

// ——————————————————————————————————————————————————————————————————————————————
// R-4 重整判定（唯一 LLM 环节，R1：全图 alias，uuid 零 prompt）
// ——————————————————————————————————————————————————————————————————————————————

// rebalanceJudgement 是 R-4 的产出：校验通过的 patch + 代码侧翻译材料。
// rebalanceJudgement is R-4's output: the validated patch + translation material.
type rebalanceJudgement struct {
	p             *patch.Patch
	book          *aliasBook
	membersByUUID map[string][]string // node uuid → distinct member（04 id，稳定序）
}

// judgeRebalance 组装全图重整输入并调用 LLM 产出结构 patch（含校验与原地重试）。
// R1 铁律：prompt 里只有 alias/04 id/name/summary/slug/指标数值，绝无 node uuid；
// 重试反馈用固定文案，绝不回显 LLM 原始输出。
//
// judgeRebalance assembles the full-graph input and asks the LLM for a structural
// patch, with validation and in-place retry. UUIDs never enter the prompt (R1).
func (o *RebalanceOrchestrator) judgeRebalance(ctx context.Context, st *stores,
	graph *metrics.SemanticGraph, comm *community.Result, snap metrics.Snapshot,
	cumulative float64, rs *runState) (*rebalanceJudgement, error) {

	// —— 全图快照：全部 entity/concept + 全部层节点（稳定序，确定性）——
	all, err := st.nodes.ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取节点失败: %w", err)
	}
	var candidates, subs, doms []*dktypes.Node
	for _, n := range all {
		switch n.Label {
		case dktypes.LabelEntity, dktypes.LabelConcept:
			candidates = append(candidates, n)
		case dktypes.LabelSubdomain:
			subs = append(subs, n)
		case dktypes.LabelDomain:
			doms = append(doms, n)
		}
	}
	sortNodesByID(candidates)
	sortNodesByID(subs)
	sortNodesByID(doms)

	// alias 分配复用第二块 buildAliases（<subdomain-slug>#<序号> / sd#n / dom#n）。
	book, _ := buildAliases(candidates, subs, doms)

	// node 材料：alias → 节点、uuid → alias、uuid → members（split 划分用）。
	membersByUUID := make(map[string][]string, len(candidates))
	for _, n := range candidates {
		rows, err := st.sources.ListByNode(ctx, n.ID)
		if err != nil {
			return nil, fmt.Errorf("读取 node 来源失败: %w", err)
		}
		memberSet := make(map[string]bool)
		for _, r := range rows {
			if r.MemberID != "" && !memberSet[r.MemberID] {
				memberSet[r.MemberID] = true
				membersByUUID[n.ID] = append(membersByUUID[n.ID], r.MemberID)
			}
		}
		sort.Strings(membersByUUID[n.ID])
	}
	aliasByUUID := make(map[string]string, len(book.nodeByAlias))
	for alias, n := range book.nodeByAlias {
		aliasByUUID[n.ID] = alias
	}

	scope := &patch.Scope{
		NodeAliases:      make(map[string]bool, len(book.nodeByAlias)),
		SubdomainAliases: make(map[string]bool, len(book.subByAlias)),
		DomainAliases:    make(map[string]bool, len(book.domByAlias)),
		MembersOf: func(alias string) []string {
			n := book.nodeByAlias[alias]
			if n == nil {
				return nil
			}
			return membersByUUID[n.ID]
		},
		IsHumanCurated: func(alias string) bool {
			n := book.nodeByAlias[alias]
			return n != nil && n.Provenance == dktypes.ProvenanceHumanCurated
		},
	}
	for alias := range book.nodeByAlias {
		scope.NodeAliases[alias] = true
	}
	for alias := range book.subByAlias {
		scope.SubdomainAliases[alias] = true
	}
	for alias := range book.domByAlias {
		scope.DomainAliases[alias] = true
	}

	user := buildRebalancePrompt(candidates, subs, doms, book, aliasByUUID, membersByUUID, comm, snap, cumulative, "")
	var lastErr error
	for round := 1; round <= maxRebalanceRounds; round++ {
		resp, err := o.client.Complete(ctx, llm.CompleteRequest{
			System:    rebalanceSystemPrompt,
			User:      user,
			MaxTokens: o.maxTokens,
		})
		if err != nil {
			lastErr = fmt.Errorf("第 %d 次重整判定调用: %w", round, err)
			continue
		}
		if resp == nil {
			lastErr = fmt.Errorf("第 %d 次重整判定返回空响应", round)
			continue
		}
		p, perr := patch.Parse(resp.Text)
		if perr != nil {
			lastErr = perr
			// R1：重试反馈固定文案，绝不回显 LLM 原始输出（防 uuid 经回显泄漏）。
			user = buildRebalancePrompt(candidates, subs, doms, book, aliasByUUID, membersByUUID, comm, snap, cumulative,
				"上一轮返回的不是合法 JSON patch，请仅返回符合操作词表的 JSON 对象")
			continue
		}
		if verr := p.Validate(scope); verr != nil {
			lastErr = verr
			log.Printf("[rebalance] R-4 patch 校验未过（第 %d 轮，详情仅落日志）: %v", round, verr)
			user = buildRebalancePrompt(candidates, subs, doms, book, aliasByUUID, membersByUUID, comm, snap, cumulative,
				"上一轮 patch 未通过校验（存在未命中 alias / 非法操作 / 非法 kind / 非法 split 划分），请修正后重试")
			continue
		}
		return &rebalanceJudgement{p: p, book: book, membersByUUID: membersByUUID}, nil
	}
	return nil, fmt.Errorf("重整判定 %d 轮仍未通过: %w", maxRebalanceRounds, lastErr)
}

// rebalanceSystemPrompt 是 R-4 的 system 提示（操作词表 + 严格 JSON + 保守优先）。
// rebalanceSystemPrompt instructs the rebalance judgement task.
const rebalanceSystemPrompt = `你是知识图谱结构重整专家。这张领域知识图谱经历多轮人机协同增量后发生了结构漂移：
子域碎片化、节点重复、归属错乱、连边冗余。用户会给你图谱的当前完整结构
（领域 domain → 子域 subdomain → 节点 node 三层，节点以 alias 标识）、
算法（Louvain 社区检测）建议的分组、以及当前复杂度诊断指标。

你的任务是在算法骨架上做【保面积的全局化简】：剪枝、合并、迁移、升降位。
只对【需要改的部分】输出结构 patch——未涉及的部分绝不要出现在 patch 中。

可用操作（严格使用下列 op 与字段名）：
1. merge_nodes：{"op":"merge_nodes","members":["alias1","alias2",...],"name":"可选新名"}
   语义等价的 node 合并为一个（members ≥2）。
2. split_node：{"op":"split_node","source":"alias","parts":[{"name":"新节点名","tag":"entity|concept","member_ids":["成员id",...]},...]}
   一个 node 拆成多个；各 part 的 member_ids 合起来必须是该节点全部 members 的不重不漏划分。
3. remigrate_node：{"op":"remigrate_node","node":"alias","to_domain":"dom#n 或新领域名（同域迁移可省略）","to_subdomain":"sd#n 或新子域名"}
   节点换归属（身份不变）。
4. merge_subdomain：{"op":"merge_subdomain","sources":["sd#1","sd#2"],"target":"sd#n 或新子域名"}
   过小子域合并（节点身份不变）。
5. split_subdomain：{"op":"split_subdomain","source":"sd#n","parts":[{"name":"新子域名","nodes":["alias",...]},...]}
   过大子域拆分（节点身份不变）。
6. promote：{"op":"promote","node":"alias","new_subdomain_name":"新子域名"}
   以该 node 为核心新建子域，并把算法认为同组的节点迁入（保守：仅在明显成立时使用）。
7. demote：{"op":"demote","subdomain":"sd#n","into_subdomain":"sd#m"}
   过小/无独立价值的子域整体并入另一子域。
8. drop_edge：{"op":"drop_edge","source":"alias","target":"alias","kind":"triggers|depends_on|references|generalizes|composes|contradicts"}
   删除冗余/失效语义边。
9. rename：{"op":"rename","target":"alias 或 sd#n 或 dom#n","name":"新名称"}

严格要求：
1. 只返回一个 JSON 对象（json 格式），形如 {"ops":[...]}，不要输出任何解释性文字或代码围栏。
2. 所有 alias 必须逐字引用输入清单中给出的值，绝不编造、绝不改写。
3. 保守优先：不确定就不要动；如果当前结构已足够好，返回 {"ops":[]} 是完全允许的。
4. 不要拆分标注为 human_curated（人工策展）的节点。
5. 优先使用 merge/remigrate/merge_subdomain 等低风险操作；promote/demote 仅在语义明显时使用。`

// buildRebalancePrompt 组装 R-4 的 user 消息（诊断 + 建议社区 + 全图结构 + 上轮反馈）。
// R1 自查：本函数拼进 prompt 的只有 alias / 04 id / name / summary / slug / 指标数值，
// 绝无 node uuid。
//
// buildRebalancePrompt assembles the R-4 user message. R1: only aliases, 04 ids,
// names, summaries, slugs and metric values enter the prompt — never a uuid.
func buildRebalancePrompt(candidates, subs, doms []*dktypes.Node, book *aliasBook,
	aliasByUUID map[string]string, membersByUUID map[string][]string, comm *community.Result,
	snap metrics.Snapshot, cumulative float64, prevErr string) string {

	var sb strings.Builder
	sb.WriteString("# 图谱结构重整任务\n\n## 复杂度诊断（触发本次重整的指标现值）\n")
	fmt.Fprintf(&sb, "- 现状模块度 Q = %.3f（越低说明现有子域归属与真实连接越脱节）\n", snap.ModularityQ)
	fmt.Fprintf(&sb, "- 单例子域占比 = %.3f（%d/%d，越高越碎片化）\n",
		snap.SingletonRatio, snap.SingletonCount, snap.SubdomainCount)
	fmt.Fprintf(&sb, "- 语义边/节点比 = %.3f（%d 边 / %d 节点，越高连边越冗余）\n",
		snap.EdgeNodeRatio, snap.EdgeCount, snap.NodeCount)
	fmt.Fprintf(&sb, "- 累计改动占比 = %.3f（增量漂移程度）\n", cumulative)

	sb.WriteString("\n## 算法建议的社区划分（Louvain，仅作先验骨架，不强制采纳）\n")
	commIDs := make([]int, 0, len(comm.Communities))
	for cid := range comm.Communities {
		commIDs = append(commIDs, cid)
	}
	sort.Ints(commIDs)
	shown, singletons := 0, 0
	for _, cid := range commIDs {
		members := comm.Communities[cid]
		if len(members) < 2 {
			singletons += len(members)
			continue
		}
		shown++
		aliases := make([]string, 0, len(members))
		for _, id := range members {
			if a := aliasByUUID[id]; a != "" {
				aliases = append(aliases, a)
			}
		}
		sort.Strings(aliases)
		fmt.Fprintf(&sb, "- 建议社区 %d：%s\n", shown, strings.Join(aliases, "、"))
	}
	if singletons > 0 {
		fmt.Fprintf(&sb, "（另有 %d 个节点算法认为各自独立，未列入建议社区）\n", singletons)
	}

	sb.WriteString("\n## 当前节点清单（entity/concept；以 alias 引用）\n")
	for _, n := range candidates {
		alias := aliasByUUID[n.ID]
		fmt.Fprintf(&sb, "- alias=%s [%s] %s（域 %s / 子域 %s）：%s\n",
			alias, n.Label, n.Name, n.Domain, n.Subdomain, truncateRunes(n.Summary, 120))
		if n.Provenance == dktypes.ProvenanceHumanCurated {
			sb.WriteString("  标记：human_curated（人工策展，勿拆分、勿覆盖其内容）\n")
		}
		if members := membersByUUID[n.ID]; len(members) > 0 {
			fmt.Fprintf(&sb, "  members：%s\n", strings.Join(members, "、"))
		}
	}
	sb.WriteString("\n## 当前子域清单（以 alias 引用）\n")
	for alias, n := range book.subByAlias {
		fmt.Fprintf(&sb, "- alias=%s %s（slug %s，所属域 slug：%s）：%s\n",
			alias, n.Name, n.Subdomain, n.Domain, truncateRunes(n.Summary, 120))
	}
	sb.WriteString("\n## 当前领域清单（以 alias 引用）\n")
	for alias, n := range book.domByAlias {
		fmt.Fprintf(&sb, "- alias=%s %s（slug %s）：%s\n",
			alias, n.Name, n.Domain, truncateRunes(n.Summary, 120))
	}
	sb.WriteString("\n请只输出需要改的部分的 patch JSON（{\"ops\":[...]}）；没有值得改的，返回 {\"ops\":[]}。\n")
	if strings.TrimSpace(prevErr) != "" {
		fmt.Fprintf(&sb, "\n## 上一轮的问题（请修正后重试）\n- %s\n", prevErr)
	}
	return sb.String()
}

// ——————————————————————————————————————————————————————————————————————————————
// R-5 patch 应用（逐操作直改 KG；归属列先落库，真相 1）
// ——————————————————————————————————————————————————————————————————————————————

// rebalanceApplier 携带一次 patch 应用的上下文（仓储、运行态、alias 翻译、Louvain）。
// rebalanceApplier carries the context for applying one patch.
type rebalanceApplier struct {
	st             *stores
	rs             *runState
	ao             *alignOutcome
	book           *aliasBook
	louvain        *community.Result
	membersByUUID  map[string][]string
	now            time.Time
	rpt            *RebalanceReport
	lineageWritten int
	// synthCounter 是 I-6 合成单元 id 的序号器——合成 id 绝不含 uuid（R1：合成
	// member id 会进 I-6 prompt 的「成员」行，含 uuid 就违反 uuid 零 prompt）。
	// synthCounter numbers synthetic I-6 unit ids (never uuid-bearing, R1).
	synthCounter int
}

// synthUnitID 生成下一个合成单元 id（不含任何 uuid，R1 安全）。
// synthUnitID returns the next synthetic unit id (uuid-free, R1-safe).
func (a *rebalanceApplier) synthUnitID() string {
	a.synthCounter++
	return fmt.Sprintf("rebalance-synth-%d", a.synthCounter)
}

// apply 分发执行单个 patch 操作。存储错误返回 error（整体失败可重入）；
// 语义性失效（如 alias 目标已被前序操作合并）只告警跳过，不中断后续操作。
//
// apply dispatches one patch op. Storage errors are fatal (reentrant);
// semantic staleness degrades to a warning.
func (a *rebalanceApplier) apply(ctx context.Context, op *patch.Op) error {
	a.rpt.OpsByKind[op.Op]++
	switch op.Op {
	case patch.OpMergeNodes:
		return a.applyMergeNodes(ctx, op)
	case patch.OpSplitNode:
		return a.applySplitNode(ctx, op)
	case patch.OpRemigrateNode:
		return a.applyRemigrateNode(ctx, op)
	case patch.OpMergeSubdomain:
		return a.applyMergeSubdomain(ctx, op)
	case patch.OpSplitSubdomain:
		return a.applySplitSubdomain(ctx, op)
	case patch.OpPromote:
		return a.applyPromote(ctx, op)
	case patch.OpDemote:
		return a.applyDemote(ctx, op)
	case patch.OpDropEdge:
		return a.applyDropEdge(ctx, op)
	case patch.OpRename:
		return a.applyRename(ctx, op)
	default:
		a.rs.warnf(fmt.Sprintf("R-5：未知操作 %q（跳过）", op.Op))
		return nil
	}
}

// resolveNode 把 node alias 解析为当前仍存活的 KG 节点（被前序操作删掉的返回 nil）。
// resolveNode resolves a node alias to a still-living node (nil if gone).
func (a *rebalanceApplier) resolveNode(ctx context.Context, alias string) (*dktypes.Node, error) {
	ref := a.book.nodeByAlias[alias]
	if ref == nil {
		return nil, nil
	}
	n, err := a.st.nodes.GetByID(ctx, ref.ID)
	if err != nil {
		return nil, fmt.Errorf("读取 node 失败: %w", err)
	}
	return n, nil
}

// sourceDocsOf 返回 node 的 distinct 来源文档（affectDocs / touched 用）。
// sourceDocsOf returns the node's distinct source docs.
func (a *rebalanceApplier) sourceDocsOf(ctx context.Context, uuid string) ([]string, error) {
	rows, err := a.st.sources.ListByNode(ctx, uuid)
	if err != nil {
		return nil, fmt.Errorf("读取 node 来源失败: %w", err)
	}
	return distinctFilePaths(rows), nil
}

// deleteNodeFully 真删一个 node：取 RowID → 删出入边 → 删来源行 → 删节点 →
// 填 rs.deleted（R5 绝不悬空边；RowID 供 I-8 清 FTS 残留）。
// deleteNodeFully deletes a node with its edges/sources and records the snapshot.
func (a *rebalanceApplier) deleteNodeFully(ctx context.Context, node *dktypes.Node) error {
	rowID, err := a.st.nodes.RowID(ctx, node.ID)
	if err != nil {
		return fmt.Errorf("取 node rowid 失败: %w", err)
	}
	docs, err := a.sourceDocsOf(ctx, node.ID)
	if err != nil {
		return err
	}
	if _, err := a.st.edges.DeleteBySource(ctx, node.ID); err != nil {
		return fmt.Errorf("删 node 出边失败: %w", err)
	}
	if _, err := a.st.edges.DeleteByTarget(ctx, node.ID); err != nil {
		return fmt.Errorf("删 node 入边失败: %w", err)
	}
	if _, err := a.st.sources.DeleteByNode(ctx, node.ID); err != nil {
		return fmt.Errorf("删 node 来源行失败: %w", err)
	}
	if _, err := a.st.nodes.Delete(ctx, node.ID); err != nil {
		return fmt.Errorf("删 node 失败: %w", err)
	}
	a.rs.deleted[node.ID] = &deletedNodeInfo{
		Node: node, RowID: rowID, SourceDocs: docs, WasShared: len(docs) >= 2,
	}
	return nil
}

// writeLineage 写一条 uuid_lineage（仅 merge/split，R-lineage）。
// writeLineage records one lineage row (merge/split only).
func (a *rebalanceApplier) writeLineage(ctx context.Context, newUUID, oldUUID, reason string) error {
	if err := a.st.lineage.InsertBatch(ctx, []storage.UUIDLineage{
		{NewUUID: newUUID, OldUUID: oldUUID, Reason: reason},
	}); err != nil {
		return fmt.Errorf("写 uuid_lineage 失败: %w", err)
	}
	a.lineageWritten++
	return nil
}

// —— remigrate_node：node 换归属（uuid 不变，不写 lineage）——

// applyRemigrateNode 执行 remigrate_node：归属列先落库（真相 1），
// 脏旧+新两个子域（坑点 11：两边 relation 都可能变）。
func (a *rebalanceApplier) applyRemigrateNode(ctx context.Context, op *patch.Op) error {
	node, err := a.resolveNode(ctx, op.Node)
	if err != nil {
		return err
	}
	if node == nil {
		a.rs.warnf("R-5：remigrate 目标 node 已不存在（前序操作已重组，跳过）")
		return nil
	}
	oldKey := SubdomainKey{Domain: node.Domain, Subdomain: node.Subdomain}

	toDomain, toSub, ok := a.resolveOwnership(op.ToDomain, op.ToSubdomain, node.Domain)
	if !ok {
		a.rs.warnf("R-5：remigrate 目标归属无法解析（跳过）")
		return nil
	}
	newKey := SubdomainKey{Domain: toDomain.slug, Subdomain: toSub.slug}
	if newKey == oldKey {
		return nil // 原地迁移：无操作
	}

	// 归属列先落库（真相 1：I-7 从 DB 读脏子域节点）。
	node.Domain, node.Subdomain = toDomain.slug, toSub.slug
	node.UpdatedAt = a.now
	if err := a.st.nodes.Update(ctx, node); err != nil {
		return fmt.Errorf("remigrate 更新归属失败: %w", err)
	}
	// 迁 ownership 层级边：删旧子域→node composes（新边由 I-8 pending 幂等确认插入）。
	if _, err := a.st.edges.DeleteBetween(ctx,
		subdomainNodeID(oldKey.Domain, oldKey.Subdomain), node.ID, dktypes.KindComposes); err != nil {
		return fmt.Errorf("remigrate 迁 ownership 边失败: %w", err)
	}

	// pending（IsNew=false）：I-8 物化新层节点 + 确认新 ownership 边 + 刷 source_refs。
	a.ao.pending[node.ID] = &pendingNode{
		UUID: node.ID, IsNew: false, Domain: toDomain.slug, Subdomain: toSub.slug,
		NewDomain: toDomain.isNew, DomainName: toDomain.name,
		NewSubdomain: toSub.isNew, SubdomainName: toSub.name,
		SourceSkills: a.skillsOf(ctx, node.ID),
	}
	a.rs.dirty.DirtySubdomain(oldKey)
	a.rs.dirty.DirtySubdomain(newKey)
	docs, err := a.sourceDocsOf(ctx, node.ID)
	if err != nil {
		return err
	}
	a.rs.touched[node.ID] = &touchedNodeInfo{SourceDocs: docs, WasShared: len(docs) >= 2}
	a.rs.affectDocs(docs...)
	// 跨域迁移：旧 domain 的 provides 可能失效（I-8 syncProvenance 只覆盖新 domain）。
	if toDomain.slug != oldKey.Domain {
		if err := a.syncDomainProvides(ctx, oldKey.Domain); err != nil {
			return err
		}
	}
	// 旧子域被掏空 → 清理空层节点。
	if err := a.cleanupEmptySubdomain(ctx, oldKey); err != nil {
		return err
	}
	a.rpt.NodesRemigrated++
	return nil
}

// ownershipRef 是一个解析后的归属目标（slug + 展示名 + 是否新建）。
// ownershipRef is a resolved ownership target.
type ownershipRef struct {
	slug  string
	name  string
	isNew bool
}

// resolveOwnership 解析 remigrate 的目标 domain/subdomain（alias 或新名）。
// 防御：sub 来自 sd# alias 且显式给了 dom# 时，两者 domain 必须一致（防拼装错位）。
// resolveOwnership resolves a remigrate target (alias or new display name).
func (a *rebalanceApplier) resolveOwnership(domainRef, subRef, currentDomain string) (dom, sub ownershipRef, ok bool) {
	// —— subdomain：sd# alias 或新名 ——
	subRef = strings.TrimSpace(subRef)
	if subRef == "" {
		return dom, sub, false
	}
	subFromAlias := false
	if layer := a.book.subByAlias[subRef]; layer != nil {
		sub = ownershipRef{slug: layer.Subdomain, name: layer.Name, isNew: false}
		subFromAlias = true
		// sd# alias 自带 domain（除非显式给了 to_domain）。
		if strings.TrimSpace(domainRef) == "" {
			dom = ownershipRef{slug: layer.Domain, isNew: false}
		}
	} else {
		name, valid := normalizePersistedName(subRef)
		if !valid {
			return dom, sub, false
		}
		slug := extract.Slugify(name)
		if slug == "" {
			return dom, sub, false
		}
		sub = ownershipRef{slug: slug, name: name, isNew: true}
	}
	// —— domain：dom# alias / 新名 / 空（= sd# 自带或保持现域）——
	domainRef = strings.TrimSpace(domainRef)
	if domainRef != "" {
		if layer := a.book.domByAlias[domainRef]; layer != nil {
			dom = ownershipRef{slug: layer.Domain, name: layer.Name, isNew: false}
		} else if strings.HasPrefix(domainRef, "dom#") {
			return dom, sub, false // 编造 alias（校验已拦，防御）
		} else {
			name, valid := normalizePersistedName(domainRef)
			if !valid {
				return dom, sub, false
			}
			slug := extract.Slugify(name)
			if slug == "" {
				return dom, sub, false
			}
			dom = ownershipRef{slug: slug, name: name, isNew: true}
		}
	}
	if dom.slug == "" {
		dom = ownershipRef{slug: currentDomain, isNew: false}
	}
	// sd# 自带 domain 与显式 dom# 冲突：拒绝（防御拼装错位）。
	if subFromAlias {
		if layer := a.book.subByAlias[subRef]; layer != nil && layer.Domain != dom.slug {
			return dom, sub, false
		}
	}
	return dom, sub, true
}

// skillsOf 返回 node 的 distinct 来源 skill（pending.SourceSkills 用）。
// skillsOf returns the node's distinct source skills.
func (a *rebalanceApplier) skillsOf(ctx context.Context, uuid string) []string {
	skills, err := a.st.sources.DistinctSkillsByNode(ctx, uuid)
	if err != nil {
		return nil
	}
	return skills
}

// syncDomainProvides 以 node_sources 为权威，精确同步单个 domain 的
// skill→domain provides 与 domain 节点 source_refs（跨域迁移后旧 domain 的收尾——
// I-8 syncProvenance 只覆盖受影响 node 的现 domain，旧 domain 需在此补齐）。
// syncDomainProvides precisely syncs one domain's provides edges/source_refs.
func (a *rebalanceApplier) syncDomainProvides(ctx context.Context, domain string) error {
	if domain == "" {
		return nil
	}
	contributing, err := a.st.sources.DistinctSkillsByDomain(ctx, domain)
	if err != nil {
		return fmt.Errorf("读 domain 贡献 skill 失败: %w", err)
	}
	contributingSet := make(map[string]bool, len(contributing))
	for _, s := range contributing {
		contributingSet[s] = true
	}
	domainNode, err := a.st.nodes.GetByID(ctx, domainNodeID(domain))
	if err != nil {
		return fmt.Errorf("读 domain 节点失败: %w", err)
	}
	domainName := domain
	if domainNode != nil {
		domainName = firstNonEmptyStr(domainNode.Name, domain)
		refs := strings.Join(contributing, ",")
		if domainNode.SourceRefs != refs {
			domainNode.SourceRefs = refs
			domainNode.UpdatedAt = a.now
			if err := a.st.nodes.Update(ctx, domainNode); err != nil {
				return fmt.Errorf("同步 domain source_refs 失败: %w", err)
			}
		}
	}
	for _, skill := range contributing {
		if err := ensureSkillNode(ctx, a.st, skill, a.now); err != nil {
			return err
		}
		if err := insertCheckedEdge(ctx, a.st, skillNodeID(skill), domainNodeID(domain),
			dktypes.KindProvides, fmt.Sprintf("skill %s 贡献了领域 %s", skill, domainName),
			1.0, a.now, a.rs); err != nil {
			return err
		}
	}
	incoming, err := a.st.edges.GetIncoming(ctx, domainNodeID(domain), 0)
	if err != nil {
		return fmt.Errorf("列 domain provides 失败: %w", err)
	}
	for _, e := range incoming {
		if e.Kind != dktypes.KindProvides {
			continue
		}
		skill := strings.TrimPrefix(e.SourceID, "skill::")
		if !contributingSet[skill] {
			if _, err := a.st.edges.DeleteBetween(ctx, e.SourceID, domainNodeID(domain), dktypes.KindProvides); err != nil {
				return fmt.Errorf("删失效 provides 失败: %w", err)
			}
			log.Printf("[rebalance] 删除失效 provides: %s → %s（该 skill 已不再贡献）", e.SourceID, domain)
		}
	}
	return nil
}

// cleanupEmptySubdomain 清理被掏空的子域层节点（先删其 composes 层级边，R5；
// 填 rs.deleted 供 I-8 清 FTS 残留）。子域内仍有 entity/concept 时不动作。
// cleanupEmptySubdomain removes an emptied subdomain layer node (edges first).
func (a *rebalanceApplier) cleanupEmptySubdomain(ctx context.Context, key SubdomainKey) error {
	remaining, err := a.st.nodes.ListBySubdomain(ctx, key.Domain, key.Subdomain)
	if err != nil {
		return fmt.Errorf("检查空子域失败: %w", err)
	}
	for _, n := range remaining {
		if n.Label == dktypes.LabelEntity || n.Label == dktypes.LabelConcept {
			return nil // 仍有内容节点：保留层节点
		}
	}
	layerID := subdomainNodeID(key.Domain, key.Subdomain)
	layer, err := a.st.nodes.GetByID(ctx, layerID)
	if err != nil {
		return fmt.Errorf("读子域层节点失败: %w", err)
	}
	if layer == nil {
		return nil
	}
	if err := a.deleteNodeFully(ctx, layer); err != nil {
		return err
	}
	log.Printf("[rebalance] 清理空子域层节点: %s", layerID)
	return nil
}

// —— merge_nodes：语义等价 node 合一（survivor 保留 members 最多者旧 uuid，R2/W3）——

// applyMergeNodes 执行 merge_nodes：node_sources 重指向 survivor → human_curated
// 继承（R8，先于 I-6 落库）→ 删 dead（R5 三件套 + rs.deleted + lineage merged）→
// survivor 进 pending 触发 description 重融合（A.0 真相 2 推荐 A：喂合成单元）。
func (a *rebalanceApplier) applyMergeNodes(ctx context.Context, op *patch.Op) error {
	// 1. 解析成分（已不存在的跳过；不足 2 个存活则整操作降级）。
	var comps []*dktypes.Node
	seen := make(map[string]bool)
	for _, alias := range op.Members {
		n, err := a.resolveNode(ctx, alias)
		if err != nil {
			return err
		}
		if n == nil || seen[n.ID] {
			continue
		}
		seen[n.ID] = true
		comps = append(comps, n)
	}
	if len(comps) < 2 {
		a.rs.warnf("R-5：merge_nodes 存活成分不足 2 个（跳过）")
		return nil
	}
	// 2. 选 survivor：members 最多者（并列取 uuid 字典序最小，确定性，W3）。
	survivor := comps[0]
	for _, n := range comps[1:] {
		if len(a.membersByUUID[n.ID]) > len(a.membersByUUID[survivor.ID]) ||
			(len(a.membersByUUID[n.ID]) == len(a.membersByUUID[survivor.ID]) && n.ID < survivor.ID) {
			survivor = n
		}
	}
	key := SubdomainKey{Domain: survivor.Domain, Subdomain: survivor.Subdomain}

	// 3. human_curated 判定与继承（R8）：任一成分人工 → survivor 继承，
	// description 以人工版为基底（survivor 自身人工优先，否则取 members 最多的人工成分）。
	var humanComp *dktypes.Node
	for _, n := range comps {
		if n.Provenance == dktypes.ProvenanceHumanCurated {
			humanComp = n
			if n.ID == survivor.ID {
				break // survivor 自身人工：最优基底
			}
		}
	}
	humanCurated := humanComp != nil

	// 4. node_sources 重指向 survivor（members 的权威账本，坑点 7：绝不留孤儿行）。
	for _, n := range comps {
		if n.ID == survivor.ID {
			continue
		}
		rows, err := a.st.sources.ListByNode(ctx, n.ID)
		if err != nil {
			return fmt.Errorf("读 dead node 来源失败: %w", err)
		}
		for i := range rows {
			rows[i].NodeUUID = survivor.ID
		}
		if err := a.st.sources.InsertBatch(ctx, rows); err != nil {
			return fmt.Errorf("node_sources 重指向失败: %w", err)
		}
	}

	// 5. survivor 继承人工策展 / 新名（先于 I-6 落库——fuseDescriptions 按 DB
	// provenance 跳过，否则人工内容会被重融合覆盖，R8）。
	if humanCurated || strings.TrimSpace(op.Name) != "" {
		fresh, err := a.st.nodes.GetByID(ctx, survivor.ID)
		if err != nil {
			return err
		}
		if fresh == nil {
			return fmt.Errorf("merge survivor %s 意外消失", survivor.ID)
		}
		survivor = fresh
		if humanCurated {
			survivor.Provenance = dktypes.ProvenanceHumanCurated
			if desc := strings.TrimSpace(humanComp.Description); desc != "" {
				survivor.Description = humanComp.Description
			}
		}
		if name, ok := normalizePersistedName(op.Name); ok {
			survivor.Name = name
		} else if strings.TrimSpace(op.Name) != "" {
			a.rs.warnf("R-5：merge 新名非法（保留 survivor 原名）")
		}
		survivor.UpdatedAt = a.now
		if err := a.st.nodes.Update(ctx, survivor); err != nil {
			return fmt.Errorf("merge 更新 survivor 失败: %w", err)
		}
	}

	// 6. 删 dead node（R5 三件套 + rs.deleted + lineage merged + 脏其原子域）。
	var allDocs []string
	for _, n := range comps {
		if n.ID == survivor.ID {
			continue
		}
		docs, err := a.sourceDocsOf(ctx, n.ID)
		if err != nil {
			return err
		}
		allDocs = append(allDocs, docs...)
		deadKey := SubdomainKey{Domain: n.Domain, Subdomain: n.Subdomain}
		if err := a.deleteNodeFully(ctx, n); err != nil {
			return err
		}
		if err := a.writeLineage(ctx, survivor.ID, n.ID, "merged"); err != nil {
			return err
		}
		if deadKey != key {
			a.rs.dirty.DirtySubdomain(deadKey) // 跨子域合并：dead 原子域拓扑也变
			if err := a.cleanupEmptySubdomain(ctx, deadKey); err != nil {
				return err
			}
		}
		a.rpt.NodesMerged++
	}

	// 7. survivor 进 pending（IsNew=false）+ 脏集；description 走 A.0 真相 2 推荐 A：
	// 为每个 dead 成分造合成 alignUnit 作 I-6 素材（人工继承则跳过重融合，R8）。
	pn := &pendingNode{
		UUID: survivor.ID, IsNew: false, Domain: survivor.Domain, Subdomain: survivor.Subdomain,
		SourceSkills: a.skillsOf(ctx, survivor.ID),
	}
	a.ao.pending[survivor.ID] = pn
	if !humanCurated {
		for _, n := range comps {
			if n.ID == survivor.ID {
				continue
			}
			synthID := a.synthUnitID() // 合成 id 绝不含 uuid（R1）
			detail := strings.TrimSpace(n.Description)
			if detail == "" {
				detail = strings.TrimSpace(n.Summary)
			}
			a.ao.unitsByID[synthID] = &alignUnit{ID: synthID, Detail: detail}
			pn.NewMembers = append(pn.NewMembers, synthID)
		}
		a.rs.dirty.DirtyNodeFused(survivor.ID, key)
	} else {
		a.rs.dirty.DirtySubdomain(key) // 拓扑变 → relation 重算；description 不动（R8）
	}
	survivorDocs, err := a.sourceDocsOf(ctx, survivor.ID)
	if err != nil {
		return err
	}
	allDocs = append(allDocs, survivorDocs...)
	a.rs.touched[survivor.ID] = &touchedNodeInfo{
		SourceDocs: mergeDistinctPaths(nil, allDocs), WasShared: len(distinctStrings(allDocs)) >= 2,
	}
	a.rs.affectDocs(allDocs...)
	return nil
}

// distinctStrings 去重（保持首次出现序）。
// distinctStrings de-duplicates preserving first-seen order.
func distinctStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// —— split_node：一 node 拆多片（每片 NodeUUID(子集) 首派生，R2；lineage split）——

// applySplitNode 执行 split_node：校验划分（A.0）→ 每片派生新 uuid 进 pending
// （IsNew=true，合成单元喂 I-6）→ node_sources 按划分重指向 → 删原 node → lineage。
func (a *rebalanceApplier) applySplitNode(ctx context.Context, op *patch.Op) error {
	orig, err := a.resolveNode(ctx, op.Source)
	if err != nil {
		return err
	}
	if orig == nil {
		a.rs.warnf("R-5：split_node 源 node 已不存在（跳过）")
		return nil
	}
	key := SubdomainKey{Domain: orig.Domain, Subdomain: orig.Subdomain}
	origRows, err := a.st.sources.ListByNode(ctx, orig.ID)
	if err != nil {
		return fmt.Errorf("读 split 源 node 来源失败: %w", err)
	}
	origMembers := a.membersByUUID[orig.ID]

	// 防御性划分复核（patch.Validate 已保证；应用时再核一次，防快照漂移）。
	origSet := make(map[string]bool, len(origMembers))
	for _, m := range origMembers {
		origSet[m] = true
	}
	assigned := make(map[string]bool)
	for _, part := range op.Parts {
		for _, m := range part.MemberIDs {
			if !origSet[m] || assigned[m] {
				a.rs.warnf("R-5：split 划分与原 members 不一致（跳过本操作）")
				return nil
			}
			assigned[m] = true
		}
	}
	if len(assigned) != len(origSet) {
		a.rs.warnf("R-5：split 划分未覆盖全部 members（跳过本操作）")
		return nil
	}

	docs := distinctFilePaths(origRows)
	for _, part := range op.Parts {
		name, ok := normalizePersistedName(part.Name)
		if !ok {
			a.rs.warnf("R-5：split 部分名非法（跳过本操作）")
			return nil
		}
		newUUID := extract.NodeUUID(part.MemberIDs) // split 才首派生（R2 唯一时机之一）
		if existing, err := a.st.nodes.GetByID(ctx, newUUID); err != nil {
			return err
		} else if existing != nil {
			a.rs.warnf("R-5：split 派生 uuid 与已有 node 碰撞（跳过本部分）")
			continue
		}
		label := "Entity"
		if part.Tag == "concept" {
			label = "Concept"
		}
		// 该片的 node_sources 行：按 member 划分重指向新 uuid（I-8 InsertBatch 落库）。
		var partSkills []string
		skillSeen := make(map[string]bool)
		for _, r := range origRows {
			memberInPart := false
			for _, m := range part.MemberIDs {
				if r.MemberID == m {
					memberInPart = true
					break
				}
			}
			if !memberInPart {
				continue
			}
			r.NodeUUID = newUUID
			a.ao.sourceRows = append(a.ao.sourceRows, r)
			if !skillSeen[r.Skill] {
				skillSeen[r.Skill] = true
				partSkills = append(partSkills, r.Skill)
			}
		}
		// 合成单元喂 I-6：素材 = 原 node 现有 description（供 LLM 抽取该片对应部分）。
		// 合成 id 绝不含 uuid（R1）。
		synthID := a.synthUnitID()
		detail := strings.TrimSpace(orig.Description)
		if detail == "" {
			detail = strings.TrimSpace(orig.Summary)
		}
		a.ao.unitsByID[synthID] = &alignUnit{ID: synthID, Detail: detail, FilePath: firstNonEmptyStr(docs...)}
		a.ao.pending[newUUID] = &pendingNode{
			UUID: newUUID, IsNew: true, Label: label, Name: name,
			Summary: orig.Summary, Confidence: orig.Confidence,
			Domain: orig.Domain, Subdomain: orig.Subdomain,
			NewMembers: []string{synthID}, SourceSkills: partSkills,
		}
		a.rs.dirty.DirtyNodeFused(newUUID, key)
		if err := a.writeLineage(ctx, newUUID, orig.ID, "split"); err != nil {
			return err
		}
		a.rpt.NodesSplitNew++
	}

	// 删原 node（R5 三件套 + rs.deleted）。
	if err := a.deleteNodeFully(ctx, orig); err != nil {
		return err
	}
	a.rs.dirty.DirtySubdomain(key)
	a.rs.affectDocs(docs...)
	return nil
}

// —— merge_subdomain / split_subdomain / promote / demote：子域层结构变动 ——

// applyMergeSubdomain 执行 merge_subdomain：来源子域全部 node 迁入目标子域
// （uuid 不变，归属先落库），删空来源层节点，脏全部涉及子域。
func (a *rebalanceApplier) applyMergeSubdomain(ctx context.Context, op *patch.Op) error {
	var sourceKeys []SubdomainKey
	for _, alias := range op.Sources {
		layer := a.book.subByAlias[alias]
		if layer == nil {
			a.rs.warnf("R-5：merge_subdomain 来源 alias 失效（跳过）")
			return nil
		}
		sourceKeys = append(sourceKeys, SubdomainKey{Domain: layer.Domain, Subdomain: layer.Subdomain})
	}
	// 目标：sd# alias（沿用其 domain）或新名（落在第一来源的 domain）。
	var targetKey SubdomainKey
	var targetName string
	var targetNew bool
	if layer := a.book.subByAlias[strings.TrimSpace(op.Target)]; layer != nil {
		targetKey = SubdomainKey{Domain: layer.Domain, Subdomain: layer.Subdomain}
		targetName = layer.Name
	} else {
		name, ok := normalizePersistedName(op.Target)
		if !ok || extract.Slugify(name) == "" {
			a.rs.warnf("R-5：merge_subdomain 目标名非法（跳过）")
			return nil
		}
		targetKey = SubdomainKey{Domain: sourceKeys[0].Domain, Subdomain: extract.Slugify(name)}
		targetName = name
		targetNew = true
	}
	for _, srcKey := range sourceKeys {
		if srcKey == targetKey {
			continue
		}
		if err := a.moveSubdomainNodes(ctx, srcKey, targetKey, targetName, targetNew); err != nil {
			return err
		}
		if err := a.cleanupEmptySubdomain(ctx, srcKey); err != nil {
			return err
		}
	}
	a.rs.dirty.DirtySubdomain(targetKey)
	return nil
}

// moveSubdomainNodes 把源子域的全部 entity/concept node 迁入目标子域
// （归属先落库；迁 ownership 边；pending 物化目标层；脏两边；affectDocs）。
// moveSubdomainNodes moves every entity/concept of one subdomain into another.
func (a *rebalanceApplier) moveSubdomainNodes(ctx context.Context, srcKey, targetKey SubdomainKey,
	targetName string, targetNew bool) error {
	nodes, err := a.st.nodes.ListBySubdomain(ctx, srcKey.Domain, srcKey.Subdomain)
	if err != nil {
		return fmt.Errorf("列源子域节点失败: %w", err)
	}
	oldDomains := make(map[string]bool)
	for _, n := range nodes {
		if n.Label != dktypes.LabelEntity && n.Label != dktypes.LabelConcept {
			continue // 层节点自身不动（空后由 cleanupEmptySubdomain 清理）
		}
		oldDomains[n.Domain] = true
		n.Domain, n.Subdomain = targetKey.Domain, targetKey.Subdomain
		n.UpdatedAt = a.now
		if err := a.st.nodes.Update(ctx, n); err != nil {
			return fmt.Errorf("迁移 node 归属失败: %w", err)
		}
		if _, err := a.st.edges.DeleteBetween(ctx,
			subdomainNodeID(srcKey.Domain, srcKey.Subdomain), n.ID, dktypes.KindComposes); err != nil {
			return fmt.Errorf("迁 ownership 边失败: %w", err)
		}
		a.ao.pending[n.ID] = &pendingNode{
			UUID: n.ID, IsNew: false, Domain: targetKey.Domain, Subdomain: targetKey.Subdomain,
			NewSubdomain: targetNew, SubdomainName: targetName,
			SourceSkills: a.skillsOf(ctx, n.ID),
		}
		docs, err := a.sourceDocsOf(ctx, n.ID)
		if err != nil {
			return err
		}
		a.rs.touched[n.ID] = &touchedNodeInfo{SourceDocs: docs, WasShared: len(docs) >= 2}
		a.rs.affectDocs(docs...)
	}
	a.rs.dirty.DirtySubdomain(srcKey)
	a.rs.dirty.DirtySubdomain(targetKey)
	// 跨域子域合并：旧 domain 的 provides 收尾。
	for d := range oldDomains {
		if d != targetKey.Domain {
			if err := a.syncDomainProvides(ctx, d); err != nil {
				return err
			}
		}
	}
	return nil
}

// applySplitSubdomain 执行 split_subdomain：按 parts 把源子域 node 分配到各新子域
// （uuid 不变，归属先落库）；未列出的 node 留在源子域（保守）；源掏空才删层节点。
func (a *rebalanceApplier) applySplitSubdomain(ctx context.Context, op *patch.Op) error {
	layer := a.book.subByAlias[op.Source]
	if layer == nil {
		a.rs.warnf("R-5：split_subdomain 源 alias 失效（跳过）")
		return nil
	}
	srcKey := SubdomainKey{Domain: layer.Domain, Subdomain: layer.Subdomain}
	for _, part := range op.Parts {
		name, ok := normalizePersistedName(part.Name)
		if !ok || extract.Slugify(name) == "" {
			a.rs.warnf("R-5：split_subdomain 部分名非法（跳过本部分）")
			continue
		}
		targetKey := SubdomainKey{Domain: srcKey.Domain, Subdomain: extract.Slugify(name)}
		for _, alias := range part.Nodes {
			n, err := a.resolveNode(ctx, alias)
			if err != nil {
				return err
			}
			if n == nil {
				continue
			}
			if n.Domain != srcKey.Domain || n.Subdomain != srcKey.Subdomain {
				a.rs.warnf("R-5：split_subdomain 的 node 不在源子域（跳过该 node）")
				continue
			}
			n.Subdomain = targetKey.Subdomain
			n.UpdatedAt = a.now
			if err := a.st.nodes.Update(ctx, n); err != nil {
				return fmt.Errorf("split_subdomain 更新归属失败: %w", err)
			}
			if _, err := a.st.edges.DeleteBetween(ctx,
				subdomainNodeID(srcKey.Domain, srcKey.Subdomain), n.ID, dktypes.KindComposes); err != nil {
				return fmt.Errorf("迁 ownership 边失败: %w", err)
			}
			a.ao.pending[n.ID] = &pendingNode{
				UUID: n.ID, IsNew: false, Domain: targetKey.Domain, Subdomain: targetKey.Subdomain,
				NewSubdomain: true, SubdomainName: name,
				SourceSkills: a.skillsOf(ctx, n.ID),
			}
			docs, err := a.sourceDocsOf(ctx, n.ID)
			if err != nil {
				return err
			}
			a.rs.touched[n.ID] = &touchedNodeInfo{SourceDocs: docs, WasShared: len(docs) >= 2}
			a.rs.affectDocs(docs...)
		}
		a.rs.dirty.DirtySubdomain(targetKey)
	}
	a.rs.dirty.DirtySubdomain(srcKey)
	return a.cleanupEmptySubdomain(ctx, srcKey)
}

// applyPromote 执行 promote（保守语义，A.3）：以该 node 为核心在其现 domain 下
// 新建子域，把 Louvain 同社区且同子域的 node 一并迁入（展开为新建子域+一组迁移）。
func (a *rebalanceApplier) applyPromote(ctx context.Context, op *patch.Op) error {
	node, err := a.resolveNode(ctx, op.Node)
	if err != nil {
		return err
	}
	if node == nil {
		a.rs.warnf("R-5：promote 目标 node 已不存在（跳过）")
		return nil
	}
	name, ok := normalizePersistedName(op.NewSubdomainName)
	if !ok || extract.Slugify(name) == "" {
		a.rs.warnf("R-5：promote 新子域名非法（跳过）")
		return nil
	}
	targetKey := SubdomainKey{Domain: node.Domain, Subdomain: extract.Slugify(name)}
	srcKey := SubdomainKey{Domain: node.Domain, Subdomain: node.Subdomain}
	if targetKey == srcKey {
		return nil
	}
	// 迁入集合：node 本身 + Louvain 同社区且当前同子域的 node（保守，不跨子域拖拽）。
	moveSet := map[string]bool{node.ID: true}
	if a.louvain != nil {
		if cid, ok := a.louvain.CommunityOf[node.ID]; ok {
			for _, peerID := range a.louvain.Communities[cid] {
				if peerID == node.ID {
					continue
				}
				peer, err := a.st.nodes.GetByID(ctx, peerID)
				if err != nil {
					return err
				}
				if peer != nil && peer.Domain == srcKey.Domain && peer.Subdomain == srcKey.Subdomain {
					moveSet[peerID] = true
				}
			}
		}
	}
	for uuid := range moveSet {
		n, err := a.st.nodes.GetByID(ctx, uuid)
		if err != nil {
			return err
		}
		if n == nil {
			continue
		}
		n.Subdomain = targetKey.Subdomain
		n.UpdatedAt = a.now
		if err := a.st.nodes.Update(ctx, n); err != nil {
			return fmt.Errorf("promote 更新归属失败: %w", err)
		}
		if _, err := a.st.edges.DeleteBetween(ctx,
			subdomainNodeID(srcKey.Domain, srcKey.Subdomain), n.ID, dktypes.KindComposes); err != nil {
			return fmt.Errorf("迁 ownership 边失败: %w", err)
		}
		a.ao.pending[n.ID] = &pendingNode{
			UUID: n.ID, IsNew: false, Domain: targetKey.Domain, Subdomain: targetKey.Subdomain,
			NewSubdomain: true, SubdomainName: name,
			SourceSkills: a.skillsOf(ctx, n.ID),
		}
		docs, err := a.sourceDocsOf(ctx, n.ID)
		if err != nil {
			return err
		}
		a.rs.touched[n.ID] = &touchedNodeInfo{SourceDocs: docs, WasShared: len(docs) >= 2}
		a.rs.affectDocs(docs...)
	}
	a.rs.dirty.DirtySubdomain(srcKey)
	a.rs.dirty.DirtySubdomain(targetKey)
	return a.cleanupEmptySubdomain(ctx, srcKey)
}

// applyDemote 执行 demote：源子域全部 node 并入目标子域（等价单源 merge_subdomain）。
func (a *rebalanceApplier) applyDemote(ctx context.Context, op *patch.Op) error {
	src := a.book.subByAlias[op.Subdomain]
	tgt := a.book.subByAlias[op.IntoSubdomain]
	if src == nil || tgt == nil {
		a.rs.warnf("R-5：demote alias 失效（跳过）")
		return nil
	}
	srcKey := SubdomainKey{Domain: src.Domain, Subdomain: src.Subdomain}
	tgtKey := SubdomainKey{Domain: tgt.Domain, Subdomain: tgt.Subdomain}
	if err := a.moveSubdomainNodes(ctx, srcKey, tgtKey, tgt.Name, false); err != nil {
		return err
	}
	return a.cleanupEmptySubdomain(ctx, srcKey)
}

// —— drop_edge / rename ——

// applyDropEdge 执行 drop_edge：直删目标语义边（其子域已被其它操作 dirty 时，
// 由 I-7 重算统一收敛——已删边自然不再出现；未 dirty 则仅此直删，最小改动）。
func (a *rebalanceApplier) applyDropEdge(ctx context.Context, op *patch.Op) error {
	src, err := a.resolveNode(ctx, op.Source)
	if err != nil {
		return err
	}
	tgt, err := a.resolveNode(ctx, op.Target)
	if err != nil {
		return err
	}
	if src == nil || tgt == nil {
		a.rs.warnf("R-5：drop_edge 端点已不存在（跳过）")
		return nil
	}
	if _, err := a.st.edges.DeleteBetween(ctx, src.ID, tgt.ID, dktypes.RelationKind(op.Kind)); err != nil {
		return fmt.Errorf("drop_edge 失败: %w", err)
	}
	a.rpt.EdgesDropped++
	return nil
}

// applyRename 执行 rename：改 node/层节点的 Name（uuid 不变、无 lineage、name 非身份）；
// 受影响文档标 affected（回写更新展示名）；FTS 随内容脏子域重建。
func (a *rebalanceApplier) applyRename(ctx context.Context, op *patch.Op) error {
	name, ok := normalizePersistedName(op.Name)
	if !ok {
		a.rs.warnf("R-5：rename 新名非法（跳过）")
		return nil
	}
	// node alias 优先，其次 sd# / dom#。
	if ref := a.book.nodeByAlias[op.Target]; ref != nil {
		n, err := a.st.nodes.GetByID(ctx, ref.ID)
		if err != nil {
			return err
		}
		if n == nil {
			a.rs.warnf("R-5：rename 目标 node 已不存在（跳过）")
			return nil
		}
		n.Name = name
		n.UpdatedAt = a.now
		if err := a.st.nodes.Update(ctx, n); err != nil {
			return fmt.Errorf("rename 更新 node 失败: %w", err)
		}
		docs, err := a.sourceDocsOf(ctx, n.ID)
		if err != nil {
			return err
		}
		a.rs.touched[n.ID] = &touchedNodeInfo{SourceDocs: docs, WasShared: len(docs) >= 2}
		a.rs.affectDocs(docs...)
		// 名字进 FTS：标内容脏（只重建 FTS，不脏 relation/description，V1 语义）。
		a.rs.dirty.DirtyNodeContent(n.ID, SubdomainKey{Domain: n.Domain, Subdomain: n.Subdomain})
		a.rpt.Renames++
		return nil
	}
	if layer := a.book.subByAlias[op.Target]; layer != nil {
		n, err := a.st.nodes.GetByID(ctx, layer.ID)
		if err != nil {
			return err
		}
		if n == nil {
			a.rs.warnf("R-5：rename 目标子域已不存在（跳过）")
			return nil
		}
		n.Name = name
		n.UpdatedAt = a.now
		if err := a.st.nodes.Update(ctx, n); err != nil {
			return fmt.Errorf("rename 更新子域失败: %w", err)
		}
		key := SubdomainKey{Domain: n.Domain, Subdomain: n.Subdomain}
		// 展示名出现在该子域全部文档的归属行：全部 affected；FTS 含层节点名。
		a.rs.dirty.FTSSubdomains[key] = true
		members, err := a.st.nodes.ListBySubdomain(ctx, key.Domain, key.Subdomain)
		if err != nil {
			return err
		}
		for _, m := range members {
			if m.Label != dktypes.LabelEntity && m.Label != dktypes.LabelConcept {
				continue
			}
			docs, err := a.sourceDocsOf(ctx, m.ID)
			if err != nil {
				return err
			}
			a.rs.affectDocs(docs...)
		}
		a.rpt.Renames++
		return nil
	}
	if layer := a.book.domByAlias[op.Target]; layer != nil {
		n, err := a.st.nodes.GetByID(ctx, layer.ID)
		if err != nil {
			return err
		}
		if n == nil {
			a.rs.warnf("R-5：rename 目标域已不存在（跳过）")
			return nil
		}
		n.Name = name
		n.UpdatedAt = a.now
		if err := a.st.nodes.Update(ctx, n); err != nil {
			return fmt.Errorf("rename 更新域失败: %w", err)
		}
		// 域层节点的 FTS 行 keyed (domain, "")。
		a.rs.dirty.FTSSubdomains[SubdomainKey{Domain: n.Domain, Subdomain: ""}] = true
		members, err := a.st.nodes.ListByDomain(ctx, n.Domain)
		if err != nil {
			return err
		}
		for _, m := range members {
			if m.Label != dktypes.LabelEntity && m.Label != dktypes.LabelConcept {
				continue
			}
			docs, err := a.sourceDocsOf(ctx, m.ID)
			if err != nil {
				return err
			}
			a.rs.affectDocs(docs...)
		}
		a.rpt.Renames++
		return nil
	}
	a.rs.warnf("R-5：rename 目标 alias 未命中（跳过）")
	return nil
}
