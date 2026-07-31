// Package incremental 的本文件实现增量流水线的编排器（IncrementalOrchestrator），
// 串起 I-1..I-9 九个 stage：
//
//	I-1 变化检测（file_states 粗筛 + sidecar 块级细判 → 四分类变更集）
//	I-2 变更分派（C1/C2/C3 → 旁路；C4 → 管道 P）
//	旁路 R/D/C3（直改 KG / 引用计数软删 / 告警还原）
//	I-3 标注（仅 C4 文档的块外新增片段；复用全量 annotator + 双门 + 回退）
//	I-4 增量对齐（FTS5 召回 + alias 判归属，R1 零 uuid prompt，R2 uuid 钉死）
//	I-5 脏集收敛（旁路 + 管道的脏标记合并）
//	I-6 脏 node description 重融合（human_curated 跳过，R8）
//	I-7 脏 subdomain relation 剪枝重算
//	I-8 增量入库（局部 upsert，绝不 os.Remove(db)；脏子域语义边先删后插）
//	I-9 增量回写（仅受影响文档 + primary/镜像 + sidecar；C3 还原）
//	累计改动量写 kg_manifest（供第三块保底触发；本块只写不消费）
//
// 与全量 RunFullRebuild 的关系：全量是「删库重建」，增量是「在既有库上局部 upsert」，
// 二者共用 storage/writeback/extract/annotation 的组件，不修改全量任何行为。
//
// This file implements the IncrementalOrchestrator, chaining stages I-1..I-9 of
// the incremental pipeline over an existing KG (local upsert — never a rebuild).
package incremental

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xcosmosbox/domain-knowledge-layer/core/dktypes"
	"github.com/xcosmosbox/domain-knowledge-layer/core/storage"
	"github.com/xcosmosbox/domain-knowledge-layer/dk-build/internal/discovery"
	"github.com/xcosmosbox/domain-knowledge-layer/dk-build/internal/extract"
	"github.com/xcosmosbox/domain-knowledge-layer/dk-build/internal/llm"
	"github.com/xcosmosbox/domain-knowledge-layer/dk-build/internal/writeback"
)

// Options 配置 IncrementalOrchestrator 的依赖与参数。
// Options configures the incremental orchestrator.
type Options struct {
	// Client 是共享 LLM 客户端（标注/语义门/对齐/重融合/重算），不可为 nil。
	// Client is the shared LLM client. Must not be nil.
	Client llm.Client
	// MaxTokens 是单次 LLM 请求的 max_tokens；≤0 交由 client 默认。
	MaxTokens int
	// MaxRetries 是标注/语义门的原地重试上限；≤0 取各自默认。
	MaxRetries int
	// MaxRollbacks 是单篇文档回退重标注上限；≤0 取默认 3。
	MaxRollbacks int
	// MaxNameRunes 是 Gate A 的名称长度上限；≤0 取默认。
	MaxNameRunes int
	// MinConfidence 是新建 entity/concept 的置信度门控（与全量一致）。
	MinConfidence float64
	// RecallK 是 FTS5 召回 top-K；≤0 取默认 15。
	RecallK int
	// Rules 是 discovery 扫描规则（与全量一致；零值用其内置默认）。
	Rules discovery.DiscoveryRules
}

// IncrementalOrchestrator 是增量流水线的编排器。
// IncrementalOrchestrator orchestrates the incremental pipeline.
type IncrementalOrchestrator struct {
	client       llm.Client
	maxTokens    int
	maxRetries   int
	maxRollbacks int
	maxNameRunes int
	minConf      float64
	recallK      int
	rules        discovery.DiscoveryRules
	// beforeWriteback is a package-test fault-injection hook. Production
	// constructors leave it nil.
	beforeWriteback func(context.Context) error
}

// NewIncrementalOrchestrator 组装增量编排器。Client 不可为 nil。
// NewIncrementalOrchestrator assembles the orchestrator. Client must not be nil.
func NewIncrementalOrchestrator(opts Options) (*IncrementalOrchestrator, error) {
	if opts.Client == nil {
		return nil, fmt.Errorf("incremental: llm client is nil")
	}
	maxRollbacks := opts.MaxRollbacks
	if maxRollbacks <= 0 {
		maxRollbacks = 3
	}
	recallK := opts.RecallK
	if recallK <= 0 {
		recallK = defaultRecallK
	}
	return &IncrementalOrchestrator{
		client:       opts.Client,
		maxTokens:    opts.MaxTokens,
		maxRetries:   opts.MaxRetries,
		maxRollbacks: maxRollbacks,
		maxNameRunes: opts.MaxNameRunes,
		minConf:      opts.MinConfidence,
		recallK:      recallK,
		rules:        opts.Rules,
	}, nil
}

// UnresolvedDoc 与报告相关的字段见 types.go。
// RunReport 是一轮增量运行的结果摘要（可观测）。
// RunReport summarizes one incremental run.
type RunReport struct {
	DocsScanned      int      // 候选文档总数（sidecar 文档 + 扫描文档 + file_states 文档）
	DocsChanged      int      // 细判有实质变更的文档数
	C1Edits          int      // C1 块内编辑数
	C2Deletions      int      // C2 块删除数
	C3Violations     int      // C3 只读区篡改数（忽略+还原）
	C4Segments       int      // C4 块外新增段数
	UnitsAnnotated   int      // I-3 产出的新标注单元数
	NodesMerged      int      // 融入已有 node 数
	NodesCreated     int      // 新建 node 数
	NodesDeleted     int      // 真删 node 数（引用计数归零）
	SubdomainsReflow int      // relation 重算的脏子域数
	DocsRewritten    int      // I-9 实际写盘的文档数
	PrimariesWritten int      // I-9 写盘的 primary 数
	PrimariesDeleted int      // I-9 删除的 primary 数
	AnnotateSkipped  []string // I-3 降级跳过的文档
	// UnresolvedDocs 是 C4 未完全吸收的文档及原因（问题 2：散文已保留、baseline 未前移、
	// 下一轮可重试——绝不被「成功回写」掩盖）。
	// UnresolvedDocs lists docs whose C4 additions were not fully absorbed.
	UnresolvedDocs []UnresolvedDoc
	Warnings       []string // 全部告警（C3 篡改、降级等）
	Version        string   // 本轮 Bump 的 KB 版本（无变更轮为当前版本）
}

// Run 执行一轮增量流水线（I-1..I-9）。repoPath 是仓库根；dbPath 是既有 KG 库
// （局部 upsert，绝不删除重建）。
//
// Run executes one incremental round over the existing KG at dbPath.
func (o *IncrementalOrchestrator) Run(ctx context.Context, repoPath, dbPath string) (*RunReport, error) {
	report := &RunReport{}
	rs := newRunState()

	// ══════════ 前置验证（问题 4：任何扫描/数据库写入/文档写入之前）══════════
	// DB 文件必须已存在（绝不自动创建空库——NewDB 会建 schema，必须先检查）。
	if err := preflightPaths(repoPath, dbPath); err != nil {
		return nil, err
	}

	// 先用严格只读连接验证 schema/版本/repo 归属；只有验证通过后才允许 NewDB
	// 执行 DDL/迁移。错误、空或拿错的 DB 在拒绝前必须保持字节与 mtime 不变。
	preflightDB, err := storage.OpenPreflightReadOnly(dbPath)
	if err != nil {
		return nil, fmt.Errorf("incremental: 只读验证 db %s: %w", dbPath, err)
	}
	preflightStores := newStores(preflightDB)
	preflightErr := o.preflightKG(ctx, preflightStores, repoPath)
	closeErr := preflightDB.Close()
	if preflightErr != nil {
		return nil, preflightErr
	}
	if closeErr != nil {
		return nil, fmt.Errorf("incremental: 关闭只读预检 db %s: %w", dbPath, closeErr)
	}

	// 验证通过后才以读写模式打开既有库（局部 upsert，绝不删除重建）。
	db, err := storage.NewDB(storage.DBOptions{Path: dbPath})
	if err != nil {
		return nil, fmt.Errorf("incremental: open db %s: %w", dbPath, err)
	}
	defer db.Close()
	st := newStores(db)
	repoURL := repoPath // file_states 的 repo 键（全量未写 file_states，增量首轮建 baseline）

	// ══════════ I-1 变化检测（粗筛 + 细判）══════════
	log.Printf("══════════ I-1 变化检测 / detect ══════════")
	changesets, scanned, err := o.detect(ctx, st, repoPath, repoURL)
	if err != nil {
		return nil, err
	}
	report.DocsScanned = len(scanned.all)
	var changed []*DocChangeSet
	for _, cs := range changesets {
		if cs.HasChanges() || cs.Deleted {
			changed = append(changed, cs)
		}
	}
	report.DocsChanged = len(changed)
	log.Printf("[incremental] I-1: 候选文档 %d，实质变更 %d", len(scanned.all), len(changed))

	// ══════════ I-2 变更分派（双路径闸门）══════════
	bp, pp := Dispatch(changed, scanned.skillByDoc)
	report.C1Edits = countEdits(bp.C1Edits)
	report.C2Deletions = countDeletions(bp.C2Deletions)
	report.C3Violations = len(bp.C3Warnings)
	report.C4Segments = countSegments(pp.C4ByDoc)
	log.Printf("[incremental] I-2: C1=%d C2=%d C3=%d C4=%d（旁路零 LLM / 管道仅 C4）",
		report.C1Edits, report.C2Deletions, report.C3Violations, report.C4Segments)

	// ══════════ 旁路 R/D/C3（直改 KG / 软删 / 告警还原）══════════
	if err := applyBypass(ctx, st, bp, rs); err != nil {
		return report, fmt.Errorf("incremental: bypass: %w", err)
	}
	if err := scheduleSidecarLedgerRepairs(ctx, st, bp.C3Warnings, rs); err != nil {
		return report, fmt.Errorf("incremental: schedule sidecar ledger repair: %w", err)
	}
	report.NodesDeleted = len(rs.deleted)

	// C4 文档的原始散文（per-doc 保留用，问题 2）：只有「完整、持久进入 KG」的
	// C4 才允许被结构化块替换；未解决的必须原样保留。
	c4ProseByDoc := make(map[string]string)
	for _, cs := range changed {
		if len(cs.C4) == 0 {
			continue
		}
		var parts []string
		for _, seg := range cs.C4 {
			parts = append(parts, seg.Text)
		}
		c4ProseByDoc[cs.Path] = strings.Join(parts, "\n\n")
	}

	// ══════════ I-3 标注（仅 C4 文档的块外新增片段）══════════
	var ao *alignOutcome
	if len(pp.C4ByDoc) > 0 {
		log.Printf("══════════ I-3 标注 / annotate（%d 篇）══════════", len(pp.C4ByDoc))
		ant := newAnnotator(o.client, o.maxTokens, o.maxRetries, o.maxRollbacks, o.maxNameRunes)
		c4TextByDoc := make(map[string]string, len(pp.C4ByDoc))
		for _, cs := range changed {
			if len(cs.C4) > 0 {
				c4TextByDoc[cs.Path] = cs.C4Text
			}
		}
		annotated := ant.annotateC4Docs(ctx, pp, c4TextByDoc)
		for _, s := range annotated.Skipped {
			report.AnnotateSkipped = append(report.AnnotateSkipped, s.Path+": "+s.Reason)
		}
		for _, d := range annotated.Docs {
			report.UnitsAnnotated += len(d.Items)
		}
		log.Printf("[incremental] I-3: 标注单元 %d（降级跳过 %d 篇）", report.UnitsAnnotated, len(annotated.Skipped))

		// ══════════ I-4 增量对齐（FTS5 召回 + alias 判归属）══════════
		if len(annotated.Docs) > 0 {
			log.Printf("══════════ I-4 增量对齐 / align ══════════")
			ao, err = newAligner(o.client, o.maxTokens, st, o.recallK).align(ctx, annotated.Docs, rs)
			if err != nil {
				return report, fmt.Errorf("incremental: align: %w", err)
			}
			logAlignStats(ao)
			// 置信度门控：新建 node 低于 MinConfidence 的不落库（与全量一致）；
			// 同步清理其 sourceRows（问题 6：绝不产生孤儿 node_sources）。
			o.gateByConfidence(ao, rs)
		}

		// ══════════ C4 per-doc 提交闭包（问题 2，真正 all-or-nothing）══════════
		// 先判定并裁剪，再生成任何 dirty/report 计数。某文档任一单元未接受时，
		// 该文档贡献的全部 pending/sourceRows 都不得进入 I-6/I-8；若一个 pending
		// 跨文档融合，失败会沿该 pending 传播到所有参与文档，避免半提交共享节点。
		resolved, unresolved := resolveAndPruneC4Docs(pp, annotated, ao)
		report.UnresolvedDocs = unresolved
		if ao != nil {
			for _, pn := range ao.pending {
				if pn.IsNew {
					report.NodesCreated++
				} else {
					report.NodesMerged++
				}
			}
			// ══════════ I-5 脏集收敛（管道 P 的脏标记）══════════
			for _, uuid := range sortedPendingKeys(ao) {
				pn := ao.pending[uuid]
				key := SubdomainKey{Domain: pn.Domain, Subdomain: pn.Subdomain}
				// 融入/新建都脏 node（description）；脏 node → 脏其 subdomain（relation）。
				rs.dirty.DirtyNodeFused(uuid, key)
			}
		}
		for path := range resolved {
			rs.affectDocs(path) // resolved：I-9 重写（散文→结构化块）
		}
		for _, u := range unresolved {
			rs.c4Preserve[u.Path] = c4ProseByDoc[u.Path] // 重写时追加保留（若有其它变更）
			// 若本轮同时有 C1/C2/C3，I-9 会重写 md+sidecar。保存“本轮开始前”
			// baseline 到 file_states，使下轮仍必然检测到 C4，而不是回退新 sidecar。
			for _, cs := range changed {
				if cs.Path == u.Path && (len(cs.C1) > 0 || len(cs.C2) > 0 || len(cs.C3) > 0) &&
					cs.Sidecar != nil && cs.Sidecar.DocHash != "" {
					rs.retryBaselines[u.Path] = cs.Sidecar.DocHash
					break
				}
			}
			log.Printf("[incremental] ⚠ C4 未解决（散文保留，下轮重试）: %s: %s", u.Path, u.Reason)
		}
	}

	// ══════════ I-6/I-7 脏集重算（仅脏集，R9）══════════
	rr := &reflowResult{
		descriptions: map[string]string{},
		relations:    map[SubdomainKey][]extract.Relation{},
		keptCross:    map[SubdomainKey][]*dktypes.Edge{},
	}
	if !rs.dirty.Empty() {
		log.Printf("══════════ I-6/I-7 重融合+relation 重算 / reflow（脏 node %d，脏子域 %d）══════════",
			len(rs.dirty.NodesFused), len(rs.dirty.Subdomains))
		rf := newReflower(o.client, o.maxTokens, st)
		if ao == nil {
			ao = &alignOutcome{pending: map[string]*pendingNode{}, unitsByID: map[string]*alignUnit{}}
		}
		rr.descriptions = rf.fuseDescriptions(ctx, rs.dirty, ao, rs)
		rr.relations, rr.keptCross = rf.recomputeRelations(ctx, rs.dirty, ao, rr.descriptions, rs)
		report.SubdomainsReflow = len(rs.dirty.Subdomains)
	}
	if err := requirePendingRelationResults(ao, rr); err != nil {
		return report, fmt.Errorf("incremental: reflow: %w", err)
	}

	// ══════════ I-8 增量入库（局部 upsert，非全量重建）══════════
	// C2 survivor 只改变来源引用，dirty 可为空，但仍必须进入 I-8 执行
	// syncProvenance/source_refs/provides 同步。
	if !rs.dirty.Empty() || len(rs.touched) > 0 || len(rs.deleted) > 0 || (ao != nil && len(ao.pending) > 0) {
		log.Printf("══════════ I-8 增量入库 / local upsert ══════════")
		if err := applyIncrementalIngest(ctx, st, rs.dirty, ao, rr, rs); err != nil {
			return report, fmt.Errorf("incremental: ingest: %w", err)
		}
		logIngestStats(rs.dirty, ao, rs)
	}

	// ══════════ I-9 增量回写（仅受影响文档 + primary/镜像 + sidecar）══════════
	var writebackErr error
	if len(rs.affectedDocs) > 0 || len(bp.DeletedDocs) > 0 {
		if o.beforeWriteback != nil {
			if err := o.beforeWriteback(ctx); err != nil {
				return report, fmt.Errorf("incremental: before I-9: %w", err)
			}
		}
		log.Printf("══════════ I-9 增量回写 / rewrite（受影响文档 %d）══════════", len(rs.affectedDocs))
		wrpt, werr := applyIncrementalWriteback(ctx, st, rs, ao, bp.DeletedDocs, repoPath)
		if werr != nil {
			// 部分失败：不中断（成功文档照常推进 baseline），但向上报告（问题 5：
			// 不得以 warning 掩盖失败）。
			writebackErr = werr
			log.Printf("[incremental] ⚠ I-9 部分失败（成功部分照常推进）: %v", werr)
		}
		if wrpt != nil {
			report.DocsRewritten = wrpt.DocsRewritten
			report.PrimariesWritten = wrpt.PrimariesWritten
			report.PrimariesDeleted = wrpt.PrimariesDeleted
			log.Printf("[incremental] I-9: 重写文档 %d（跳过未变 %d），primary 写 %d 删 %d，sidecar 删 %d",
				wrpt.DocsRewritten, wrpt.DocsSkipped, wrpt.PrimariesWritten, wrpt.PrimariesDeleted, wrpt.SidecarsDeleted)
		}
	}

	// ══════════ file_states baseline + 累计改动量（kg_manifest）══════════
	// Any I-9 error may be a fatal read/build failure that did not reach the
	// per-file failedDocs bookkeeping. Do not advance *any* baseline in that
	// run: successful files will be rechecked idempotently next round, while a
	// failed file cannot be swallowed by a newly recorded hash.
	if writebackErr != nil {
		log.Printf("[incremental] ⚠ I-9 有错误，跳过本轮全部 file_states baseline 推进（下轮重试）")
	} else if err := o.syncFileStates(ctx, st, repoURL, scanned, bp.DeletedDocs, repoPath, rs); err != nil {
		return report, err
	}
	recordManifestStats(ctx, st, len(changed), scanned.sidecarDocs, rs)

	report.Warnings = rs.warningsSnapshot()
	if v, err := st.version.Current(ctx); err == nil {
		report.Version = v
	}
	log.Printf("[incremental] ✓ 完成: 变更文档 %d，融入 %d 新建 %d 真删 %d，重写文档 %d，告警 %d",
		report.DocsChanged, report.NodesMerged, report.NodesCreated, report.NodesDeleted,
		report.DocsRewritten, len(report.Warnings))
	// 可观测性：告警若只报计数，降级原因就不可见（例如 I-4 的 alias 未命中被跳过、
	// I-6 素材缺失、I-7 越界过滤），排障时无从判断「少了一个节点/一条边」是设计
	// 行为还是静默失败。C3 明细已在旁路阶段逐条打印，此处补齐其余告警明细。
	const maxWarnLog = 50
	for i, w := range report.Warnings {
		if i >= maxWarnLog {
			log.Printf("[incremental] ⚠ …另有 %d 条告警未展开（详见 build-report）", len(report.Warnings)-maxWarnLog)
			break
		}
		log.Printf("[incremental] ⚠ %s", w)
	}
	return report, writebackErr
}

// requirePendingRelationResults makes I-7 terminal for a C4 commit. A missing
// map key means the subdomain read or LLM retries did not produce an
// authoritative replacement set. Existing-only reflows may still fail closed
// in I-8, but a pending node and its source rows must not be committed without
// the relation view that included that node.
func requirePendingRelationResults(ao *alignOutcome, rr *reflowResult) error {
	if ao == nil || len(ao.pending) == 0 {
		return nil
	}
	if rr == nil {
		return fmt.Errorf("I-7 produced no result for pending C4 nodes")
	}
	missing := make(map[SubdomainKey]bool)
	for _, uuid := range sortedPendingKeys(ao) {
		pn := ao.pending[uuid]
		if pn == nil {
			return fmt.Errorf("I-7 cannot verify nil pending node %s", uuid)
		}
		key := SubdomainKey{Domain: pn.Domain, Subdomain: pn.Subdomain}
		_, hasRelations := rr.relations[key]
		_, hasCross := rr.keptCross[key]
		if !hasRelations || !hasCross {
			missing[key] = true
		}
	}
	if len(missing) == 0 {
		return nil
	}
	keys := make([]SubdomainKey, 0, len(missing))
	for key := range missing {
		keys = append(keys, key)
	}
	sortSubdomainKeys(keys)
	return fmt.Errorf("I-7 incomplete for pending C4 subdomain %s; refusing node/source commit", keys[0])
}

// scheduleSidecarLedgerRepairs handles the durable fingerprint of a late I-8
// partial commit. If node/node_sources landed but syncProvenance, FTS, or version
// failed, the next real Run sees the old sidecar disagree with the authoritative
// KG ledger. Detection intentionally classifies that as C3 and does not trust the
// stale sidecar to reconstruct C4. Re-mark the ledger's current nodes for the
// idempotent provenance/FTS tail of I-8; the already-committed node content and
// relations remain authoritative and do not need another LLM pass.
//
// Ordinary read-only tampering (name/tag/structure/etc.) is deliberately ignored
// here and retains the normal C3 warning-and-restore behavior.
func scheduleSidecarLedgerRepairs(ctx context.Context, st *stores, warnings []C3Warning, rs *runState) error {
	repairDocs := make(map[string]bool)
	for _, warning := range warnings {
		if warning.Field != "sidecar_metadata" && warning.Field != "sidecar_ownership" {
			continue
		}
		if warning.Path == "" || IsSharedPrimaryPath(warning.Path) {
			continue
		}
		repairDocs[warning.Path] = true
	}
	seenNodes := make(map[string]bool)
	for docPath := range repairDocs {
		rows, err := st.sources.ListByFile(ctx, docPath)
		if err != nil {
			return fmt.Errorf("list sources for %s: %w", docPath, err)
		}
		for _, row := range rows {
			uuid := row.NodeUUID
			if uuid == "" || seenNodes[uuid] {
				continue
			}
			seenNodes[uuid] = true
			node, err := st.nodes.GetByID(ctx, uuid)
			if err != nil {
				return fmt.Errorf("load node %s: %w", uuid, err)
			}
			if node == nil {
				continue
			}
			allSources, err := st.sources.ListByNode(ctx, uuid)
			if err != nil {
				return fmt.Errorf("list sources for node %s: %w", uuid, err)
			}
			docs := distinctFilePaths(allSources)
			info := rs.touched[uuid]
			if info == nil {
				info = &touchedNodeInfo{}
				rs.touched[uuid] = info
			}
			info.SourceDocs = mergeDistinctPaths(info.SourceDocs, docs)
			info.WasShared = info.WasShared || len(docs) >= 2
			rs.dirty.DirtyNodeContent(uuid, SubdomainKey{Domain: node.Domain, Subdomain: node.Subdomain})
			rs.affectDocs(docs...)
		}
	}
	return nil
}

func mergeDistinctPaths(existing, incoming []string) []string {
	seen := make(map[string]bool, len(existing)+len(incoming))
	out := make([]string, 0, len(existing)+len(incoming))
	for _, paths := range [][]string{existing, incoming} {
		for _, path := range paths {
			if path == "" || seen[path] {
				continue
			}
			seen[path] = true
			out = append(out, path)
		}
	}
	sort.Strings(out)
	return out
}

// ——————————————————————————————————————————————————————————————————————————————
// 前置验证（问题 4）
// ——————————————————————————————————————————————————————————————————————————————

// preflightPaths 验证 repo 与 db 路径本身（在 storage.NewDB 之前——NewDB 会创建文件）：
//   - repoPath 必须存在且是目录；
//   - dbPath 必须已存在且是普通文件（绝不自动创建空库）。
//
// preflightPaths validates repo/db paths before storage.NewDB (which would
// create the file): the repo must be a directory; the DB must already exist.
func preflightPaths(repoPath, dbPath string) error {
	repoInfo, err := os.Stat(repoPath)
	if err != nil {
		return fmt.Errorf("incremental: repo 路径不可访问 %s: %w", repoPath, err)
	}
	if !repoInfo.IsDir() {
		return fmt.Errorf("incremental: repo 路径不是目录: %s", repoPath)
	}
	dbInfo, err := os.Stat(dbPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("incremental: db 文件不存在（增量只在既有 KG 上运行，绝不自动创建空库）: %s", dbPath)
		}
		return fmt.Errorf("incremental: db 路径不可访问 %s: %w", dbPath, err)
	}
	if !dbInfo.Mode().IsRegular() {
		return fmt.Errorf("incremental: db 路径不是普通文件: %s", dbPath)
	}
	return nil
}

// preflightKG 验证打开的是「这个 repo 的、已构建的 KG」：
//  1. kb_version 非空（空 schema / 未构建的库 → 拒绝）；
//  2. 若 repo 有可读 sidecar，其 uuid 与 DB 节点必须有交集——零交集说明
//     很可能拿错了另一个 repo 的库，拒绝运行而不是「修复」文档。
//
// preflightKG verifies the DB is a built KG for this repo: non-empty kb_version,
// and (when readable sidecars exist) a non-zero uuid intersection with DB nodes.
func (o *IncrementalOrchestrator) preflightKG(ctx context.Context, st *stores, repoPath string) error {
	version, err := st.version.Current(ctx)
	if err != nil {
		return fmt.Errorf("incremental: 读取 kb_version 失败: %w", err)
	}
	if version == "" {
		return fmt.Errorf("incremental: db 无有效 KB 版本（空库或未构建），拒绝在 %s 上运行增量", repoPath)
	}

	// sidecar ↔ DB 一致性抽查。
	sidecarDocs, err := listSidecarDocs(repoPath)
	if err != nil {
		return fmt.Errorf("incremental: 枚举 sidecar 失败: %w", err)
	}
	var uuids []string
	for _, rel := range sidecarDocs {
		if len(uuids) >= 50 { // 抽查上限 / sample cap
			break
		}
		sc, err := writeback.ReadSidecar(filepath.Join(repoPath, rel))
		if err != nil {
			continue // 损坏的 sidecar 跳过（由 I-1 的 fail-closed 处理）
		}
		for _, n := range sc.Nodes {
			if n.UUID != "" {
				uuids = append(uuids, n.UUID)
			}
		}
	}
	if len(uuids) == 0 {
		return nil // 无可读 sidecar：无法校验交集（kb_version 已把关），放行
	}
	found, err := st.nodes.GetByIDs(ctx, uuids)
	if err != nil {
		return fmt.Errorf("incremental: sidecar↔DB 一致性检查失败: %w", err)
	}
	if len(found) == 0 {
		return fmt.Errorf("incremental: repo 的 %d 个 sidecar uuid 与 DB 节点零交集——"+
			"该 DB 很可能属于另一个 repo，拒绝运行（绝不拿错库「修复」文档）", len(uuids))
	}
	return nil
}

// ——————————————————————————————————————————————————————————————————————————————
// I-1 变化检测
// ——————————————————————————————————————————————————————————————————————————————

// scanResult 是文档枚举的结果（候选全集 + skill 映射 + sidecar 文档计数）。
// scanResult is the doc enumeration result.
type scanResult struct {
	all         []string          // 候选文档全集（去重，相对 repo 根）
	skillByDoc  map[string]string // 文档 → 所属 skill（discovery 扫描）
	sidecarDocs int               // 有 sidecar 的文档数（manifest 分母）
	// primaryUUIDByPath 是 KG 权威的「primary 路径 → node UUID」映射。
	// 引入 file_slug 后 primary 文件名是可读 slug、不再等于 UUID，因此禁止再从
	// 路径 basename 反解身份（会把 slug 当 UUID 查库、误判 node 已删除）。
	// primaryUUIDByPath maps canonical primary paths to their KG node UUID.
	primaryUUIDByPath map[string]string
}

// detect 执行 I-1：枚举候选文档 → 粗筛（file_states / sidecar doc_hash）→ 细判（块级 diff）。
// detect runs I-1: enumerate candidates → coarse filter → block-level diff.
func (o *IncrementalOrchestrator) detect(ctx context.Context, st *stores, repoPath, repoURL string) ([]*DocChangeSet, *scanResult, error) {
	sr := &scanResult{
		skillByDoc:        make(map[string]string),
		primaryUUIDByPath: make(map[string]string),
	}

	// —— 枚举候选文档全集 ——
	docSet := make(map[string]bool)
	addDoc := func(p string) {
		if p != "" && !docSet[p] {
			docSet[p] = true
			sr.all = append(sr.all, p)
		}
	}
	// (a) 有 sidecar 的回写产物文档（含 _shared primary）。
	sidecarDocs, err := listSidecarDocs(repoPath)
	if err != nil {
		return nil, nil, fmt.Errorf("incremental: walk sidecars: %w", err)
	}
	for _, p := range sidecarDocs {
		addDoc(p)
	}
	sr.sidecarDocs = len(sidecarDocs)
	// _shared primary 的 sidecar 可能被删/损坏；仍须枚举 md 本体，才能从路径
	// 反解 UUID 并用 KG 权威值恢复，而不是因 primary 无 node_sources 行被跳过。
	primaryDocs, err := listSharedPrimaryDocs(repoPath)
	if err != nil {
		return nil, nil, fmt.Errorf("incremental: walk shared primaries: %w", err)
	}
	for _, p := range primaryDocs {
		addDoc(p)
	}
	// Include sidecar-only primaries and artifact paths represented by abnormal
	// directories (WalkDir intentionally skips ordinary directory entries).
	primaryArtifacts, err := listSharedPrimaryArtifacts(repoPath)
	if err != nil {
		return nil, nil, fmt.Errorf("incremental: walk shared primary artifacts: %w", err)
	}
	for _, a := range primaryArtifacts {
		addDoc(a.PrimaryRel)
	}
	// (b) discovery 扫描的 reference 文档（含全新文档；提供 skill 映射）。
	scanner := discovery.NewFSScanner()
	skills, serr := scanner.Scan(ctx, repoPath, o.rules)
	if serr != nil {
		// 扫描失败降级：不阻断（sidecar 文档仍可检测），仅告警。
		log.Printf("[incremental] ⚠ discovery 扫描失败（全新文档将无法发现，继续）: %v", serr)
	}
	for _, sk := range skills {
		for _, rel := range sk.ReferenceFiles {
			addDoc(rel)
			sr.skillByDoc[rel] = sk.Name
		}
	}
	// (c) file_states 已记录的文档（整篇删除的检测：记录在、文件系统无）。
	states, err := st.files.ListByRepo(ctx, repoURL)
	if err != nil {
		return nil, nil, fmt.Errorf("incremental: list file_states: %w", err)
	}
	for _, fs := range states {
		addDoc(fs.FilePath)
	}
	// (d) node_sources 中仍受管的文档。md 与 sidecar 可能被同时删除，且首轮
	// 尚无 file_state；若不从来源表枚举，这类整文档删除永远不会进入 C2。
	allSources, err := st.sources.ListAll(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("incremental: list managed source docs: %w", err)
	}
	for _, source := range allSources {
		addDoc(source.FilePath)
	}
	// A shared primary has no node_sources row by design. Enumerate the KG's
	// expected primary paths as well, so a primary whose md *and* sidecar were
	// both lost is still discovered and rebuilt from the fixed path + node UUID.
	expectedPrimaries, primaryUUIDs, err := listExpectedSharedPrimaryDocs(ctx, st)
	if err != nil {
		return nil, nil, fmt.Errorf("incremental: enumerate expected shared primaries: %w", err)
	}
	for _, p := range expectedPrimaries {
		addDoc(p)
	}
	// KG 权威身份映射：detectOne 据此识别 primary，而非从 slug 文件名反解 UUID。
	sr.primaryUUIDByPath = primaryUUIDs

	// —— 逐文档粗筛 + 细判 ——
	var changesets []*DocChangeSet
	for _, relPath := range sr.all {
		cs, err := o.detectOne(ctx, st, repoPath, repoURL, relPath, sr)
		if err != nil {
			return nil, nil, err
		}
		if cs != nil {
			changesets = append(changesets, cs)
		}
	}
	return changesets, sr, nil
}

// detectOne 对单个文档做变化检测：不存在 → 整篇删除（C2）；无 sidecar 的扫描文档
// → 全新（C4）；有 sidecar → 粗筛（file_states 优先，首轮回退 sidecar.doc_hash）
// 变了才细判。未变更返回 nil。
//
// detectOne detects changes for one doc: deleted / brand-new / coarse+detailed diff.
func (o *IncrementalOrchestrator) detectOne(ctx context.Context, st *stores,
	repoPath, repoURL, relPath string, sr *scanResult) (*DocChangeSet, error) {

	absPath := filepath.Join(repoPath, relPath)
	data, readErr := os.ReadFile(absPath)
	fileExists := readErr == nil
	var primaryReadProblem error
	if readErr != nil && !os.IsNotExist(readErr) {
		// Any unreadable _shared artifact (including a directory named *.md) is
		// reconciled as C3. Ordinary reference read errors remain fatal because
		// they cannot be safely reconstructed without first identifying the doc.
		if _, artifact := sharedPrimaryArtifactIdentity(relPath); artifact {
			fileExists = true
			data = nil
			primaryReadProblem = readErr
		} else {
			return nil, fmt.Errorf("incremental: read doc %s: %w", relPath, readErr)
		}
	}

	// sidecar 只在文档存在或曾回写过时有意义。
	sidecar, scErr := writeback.ReadSidecar(absPath)
	hasSidecar := scErr == nil && sidecar != nil
	if hasSidecar {
		// ReadSidecar validates on-disk YAML, and this second value-level check
		// also covers callers/filesystems that supplied a decoded legacy value.
		if err := writeback.ValidateSidecar(*sidecar); err != nil {
			hasSidecar = false
			scErr = fmt.Errorf("invalid sidecar semantics: %w", err)
		} else if filepath.ToSlash(sidecar.Doc) != filepath.ToSlash(relPath) {
			hasSidecar = false
			scErr = fmt.Errorf("sidecar doc %q does not match %q", sidecar.Doc, relPath)
		}
	}
	primaryUUID, isPrimary := sharedPrimaryUUID(relPath)
	artifactUUID, isArtifact := sharedPrimaryArtifactIdentity(relPath)
	pathShapePrimary := isPrimary
	if !isPrimary && isArtifact {
		primaryUUID = artifactUUID
		isPrimary = true
	}
	// KG 权威身份优先：引入 file_slug 后 primary 文件名是可读 slug，basename 不再
	// 等于 UUID。若仍用 basename 当 UUID 查库，健康的 slug 命名 primary 会被判成
	// 「node 已不在 KG」而误报 primary_stale 并被 I-9 删除。命中权威映射即为在册
	// primary，用映射里的真 UUID 建立身份；未命中的 _shared 产物才是真孤儿。
	if id, ok := sr.primaryUUIDByPath[canonicalPrimaryKey(relPath)]; ok && id != "" {
		primaryUUID = id
		isPrimary = true
		pathShapePrimary = true
	}
	// Primary identity comes from its canonical _shared/<domain>/<uuid>.md path
	// and the KG, never from node_sources or a possibly damaged sidecar. A
	// missing primary is repaired, not interpreted as C2 source deletion.
	if isPrimary {
		artifactPresent := fileExists || pathEntryExists(absPath) || pathEntryExists(absPath+".kg.yaml")
		node, err := st.nodes.GetByID(ctx, primaryUUID)
		if err != nil {
			return nil, fmt.Errorf("incremental: validate shared primary %s: %w", relPath, err)
		}
		if node != nil {
			sources, err := st.sources.ListByNode(ctx, primaryUUID)
			if err != nil {
				return nil, fmt.Errorf("incremental: validate primary sharedness %s: %w", relPath, err)
			}
			if len(distinctFilePaths(sources)) < 2 {
				if artifactPresent {
					return &DocChangeSet{Path: relPath, Changed: true, C3: []C3Violation{{
						UUID: primaryUUID, Field: "primary_stale", Want: "no primary for non-shared node", Got: relPath,
					}}}, nil
				}
				// A file_state may be the only remaining trace after a successful
				// shared→non-shared deletion. Publish an empty deleted change so
				// Dispatch/syncFileStates can remove that state without C3 loops.
				return &DocChangeSet{Path: relPath, Deleted: true, Changed: true}, nil
			}
			canonicalPrimary := pathShapePrimary && relPath == writeback.PrimaryRelPath(node.Domain, node.FileSlug, primaryUUID)
			if !canonicalPrimary {
				return &DocChangeSet{Path: relPath, Changed: true, C3: []C3Violation{{
					UUID: primaryUUID, Field: "primary_path", Want: writeback.PrimaryRelPath(node.Domain, node.FileSlug, primaryUUID), Got: relPath,
				}}}, nil
			}
			if primaryReadProblem != nil {
				return &DocChangeSet{Path: relPath, Changed: true, C3: []C3Violation{{
					UUID: primaryUUID, Field: "primary_unreadable", Want: "regular readable markdown file", Got: primaryReadProblem.Error(),
				}}}, nil
			}
			if !fileExists || !hasSidecar {
				field := "primary_missing"
				if fileExists && !hasSidecar {
					field = "sidecar_corrupt"
				}
				return &DocChangeSet{Path: relPath, Changed: true, C3: []C3Violation{{
					UUID: primaryUUID, Field: field,
					Want: "KG-authoritative primary + valid sidecar", Got: fmt.Sprintf("%v", scErr),
				}}}, nil
			}
		} else if artifactPresent {
			// The node was removed in an earlier run, but one or both artifacts
			// survived. Surface a C3 candidate so I-9 can converge stale files
			// even without a current node_sources row.
			return &DocChangeSet{Path: relPath, Changed: true, C3: []C3Violation{{
				UUID: primaryUUID, Field: "primary_stale", Want: "node exists in KG", Got: "node missing",
			}}}, nil
		}
	}
	// For ordinary source documents, node_sources is the authoritative
	// ownership ledger. A syntactically valid sidecar with an extra, missing, or
	// foreign UUID must not be trusted to manufacture C1/C2 decisions. Fail
	// closed and let I-9 rebuild from the ledger + KG. Primaries are excluded:
	// they intentionally have no node_sources rows and are keyed by fixed path.
	if fileExists && hasSidecar && !isPrimary {
		rows, err := st.sources.ListByFile(ctx, relPath)
		if err != nil {
			return nil, fmt.Errorf("incremental: sidecar ownership check %s: %w", relPath, err)
		}
		expected := make(map[string]bool)
		for _, row := range rows {
			if row.NodeUUID != "" {
				expected[row.NodeUUID] = true
			}
		}
		actual := make(map[string]bool)
		for _, node := range sidecar.Nodes {
			if node.UUID != "" {
				actual[node.UUID] = true
			}
		}
		if !sameUUIDSet(expected, actual) {
			return &DocChangeSet{Path: relPath, Skill: sr.skillByDoc[relPath], Changed: true, C3: []C3Violation{{
				UUID: "(doc)", Field: "sidecar_ownership",
				Want: formatUUIDSet(expected), Got: formatUUIDSet(actual),
			}}}, nil
		}
	}
	// A valid YAML shape and matching UUID set are not sufficient to trust a
	// sidecar as a C1/C2 baseline.  Every node entry must also agree with the
	// current KG and node_sources ledger (including editable hashes).  Perform
	// this reconciliation before header/file_state shortcuts for both ordinary
	// docs and shared primaries; otherwise a forged hash or a partially committed
	// old sidecar can suppress a real edit indefinitely.
	if fileExists && hasSidecar {
		expected, err := authoritativeSidecarViews(ctx, st, relPath, isPrimary, primaryUUID)
		if err != nil {
			return nil, fmt.Errorf("incremental: build authoritative sidecar view %s: %w", relPath, err)
		}
		if !writeback.SidecarMetadataMatches(*sidecar, relPath, expected) {
			uuid := "(doc)"
			if isPrimary {
				uuid = primaryUUID
			}
			return &DocChangeSet{Path: relPath, Skill: sr.skillByDoc[relPath], Changed: true, C3: []C3Violation{{
				UUID: uuid, Field: "sidecar_metadata",
				Want: "KG-authoritative node metadata and editable hashes",
				Got:  "sidecar differs from current KG/node_sources",
			}}}, nil
		}
	}

	if !fileExists {
		// 整篇删除：sidecar 可能同时缺失/损坏，不能再依赖其 nodes 列表。
		// node_sources 是 KG 权威来源，需由 file_path 反推全部 C2 UUID；否则会
		// 只删 file_state 而永久残留该文档贡献。
		rec, err := st.files.Get(ctx, repoURL, relPath)
		if err != nil {
			return nil, err
		}
		rows, err := st.sources.ListByFile(ctx, relPath)
		if err != nil {
			return nil, fmt.Errorf("incremental: list deleted doc sources %s: %w", relPath, err)
		}
		sidecarArtifactPresent := pathEntryExists(absPath + ".kg.yaml")
		if rec == nil && !sidecarArtifactPresent && len(rows) == 0 {
			return nil, nil // 与 KG 无关的文件消失，忽略
		}
		// Once the md is gone, node_sources is the only authoritative source for
		// C2. A stale/copy sidecar is merely a deletion artifact and must never
		// resurrect or delete UUIDs outside the ledger.
		seen := make(map[string]bool)
		cs := &DocChangeSet{Path: relPath, Deleted: true, Changed: true}
		for _, row := range rows {
			if row.NodeUUID != "" && !seen[row.NodeUUID] {
				seen[row.NodeUUID] = true
				cs.C2 = append(cs.C2, row.NodeUUID)
			}
		}
		sortStrings(cs.C2)
		cs.Skill = sr.skillByDoc[relPath]
		return cs, nil
	}

	if !hasSidecar {
		// shared primary 的受管身份由固定路径 + KG node 决定，不依赖正文标记或
		// node_sources（primary 天生没有来源行）。即使正文也被清空仍可恢复。
		if uuid, ok := sharedPrimaryUUID(relPath); ok {
			node, err := st.nodes.GetByID(ctx, uuid)
			if err != nil {
				return nil, fmt.Errorf("incremental: validate shared primary %s: %w", relPath, err)
			}
			if node != nil {
				log.Printf("[incremental] ⚠ shared primary %s sidecar 缺失/损坏（%v）→ C3 恢复", relPath, scErr)
				return &DocChangeSet{Path: relPath, Changed: true, C3: []C3Violation{{
					UUID: uuid, Field: "sidecar_corrupt", Want: "valid sidecar", Got: fmt.Sprintf("%v", scErr),
				}}}, nil
			}
		}
		// 安全边界 1：sidecar 缺失 vs 损坏必须区分。带 KG 标记（头注释/uuid 块）的
		// 已管理文档若 sidecar 损坏，绝不当「全新 C4 文档」重新标注：
		//   - KG 管着它（node_sources 有行）→ C3 fail-closed，I-9 从 KG 权威值重渲染
		//     （同时重建 sidecar，完成自愈）；
		//   - KG 不管它（node_sources 无行）→ 无法恢复：告警跳过，文档与 baseline 不动。
		managedMarker := strings.Contains(string(data), kgOpenPrefix) || strings.Contains(string(data), docHeaderLine)
		rows, err := st.sources.ListByFile(ctx, relPath)
		if err != nil {
			return nil, fmt.Errorf("incremental: check managed doc %s: %w", relPath, err)
		}
		if len(rows) > 0 {
			log.Printf("[incremental] ⚠ %s sidecar 损坏（%v）→ C3 fail-closed，将从 KG 恢复", relPath, scErr)
			cs := &DocChangeSet{Path: relPath, Skill: sr.skillByDoc[relPath], Changed: true}
			cs.C3 = append(cs.C3, C3Violation{
				UUID: "(doc)", Field: "sidecar_corrupt", Want: "valid sidecar", Got: fmt.Sprintf("%v", scErr),
			})
			return cs, nil
		}
		if managedMarker {
			log.Printf("[incremental] ⚠ %s 带 KG 标记但 sidecar 损坏且 KG 无其来源行（跳过，不重建 baseline）: %v", relPath, scErr)
			return nil, nil
		}
		// 全新文档：仅当它是 discovery 扫描到的 reference 文档（repo 里无关 .md 不入 KG）。
		if _, scanned := sr.skillByDoc[relPath]; !scanned {
			return nil, nil
		}
		cs := DiffDoc(relPath, nil, string(data), false)
		cs.Skill = sr.skillByDoc[relPath]
		return cs, nil
	}
	// Header integrity is checked before file_state/doc_hash coarse skipping.
	// This also repairs repositories where an older buggy run advanced a
	// baseline after the system header had already been removed or modified.
	if !managedHeaderCanonical(string(data)) {
		cs := DiffDoc(relPath, sidecar, string(data), false)
		cs.Skill = sr.skillByDoc[relPath]
		return cs, nil
	}

	// 有 sidecar：粗筛（file_states 记录优先；首轮无记录回退 sidecar.doc_hash，坑点 13）。
	contentHash := writeback.HashEditable(string(data))
	rec, err := st.files.Get(ctx, repoURL, relPath)
	if err != nil {
		return nil, err
	}
	baseline := sidecar.DocHash // 首轮 baseline：sidecar 的回写后 hash
	if rec != nil {
		baseline = rec.ContentHash // 之后各轮：file_states（I-8 补写）
	}
	if baseline == contentHash {
		// 兼容修复旧版本“未解决 C4 被写进新 sidecar baseline”的现场：首轮尚无
		// file_state 时，即使 doc_hash 相等也做一次细判；若块外仍有文本，必须重试。
		// Legacy shared sidecars without domain_slug also require one deep pass so
		// mirror source validation can trigger a canonical self-healing rewrite,
		// even when file_states already contains the current hash. A disagreement
		// between file_state and sidecar.doc_hash also invalidates the shortcut:
		// the state may have been advanced by an older partial failure while the
		// sidecar still carries the only usable editable baseline.
		if rec == nil || rec.ContentHash != sidecar.DocHash || sidecarNeedsDeepValidation(sidecar) {
			cs := DiffDoc(relPath, sidecar, string(data), false)
			cs.Skill = sr.skillByDoc[relPath]
			if cs.HasChanges() {
				return cs, nil
			}
		}
		return nil, nil // 未变，跳过（粗筛拦截）
	}

	cs := DiffDoc(relPath, sidecar, string(data), false)
	cs.Skill = sr.skillByDoc[relPath]
	return cs, nil
}

// authoritativeSidecarViews constructs the exact metadata baseline that I-9
// would write from the current KG.  Ordinary docs derive membership and spans
// from node_sources; primaries derive their sole node from the canonical path
// and intentionally carry no document-specific span.
func authoritativeSidecarViews(ctx context.Context, st *stores, relPath string,
	isPrimary bool, primaryUUID string) ([]writeback.NodeUpdate, error) {
	names, err := loadLayerDisplayNames(ctx, st)
	if err != nil {
		return nil, err
	}
	if !isPrimary {
		return buildDocNodeUpdates(ctx, st, relPath, names)
	}
	node, err := st.nodes.GetByID(ctx, primaryUUID)
	if err != nil {
		return nil, err
	}
	if node == nil {
		return nil, nil
	}
	sources, err := st.sources.ListByNode(ctx, primaryUUID)
	if err != nil {
		return nil, err
	}
	return []writeback.NodeUpdate{buildNodeUpdate(node, sources, names, "")}, nil
}

func sameUUIDSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for uuid := range a {
		if !b[uuid] {
			return false
		}
	}
	return true
}

func formatUUIDSet(set map[string]bool) string {
	uuids := make([]string, 0, len(set))
	for uuid := range set {
		uuids = append(uuids, uuid)
	}
	sort.Strings(uuids)
	return strings.Join(uuids, ",")
}

func pathEntryExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func sidecarNeedsDeepValidation(sidecar *writeback.SidecarFile) bool {
	if sidecar == nil {
		return true
	}
	for _, node := range sidecar.Nodes {
		if node.Shared && node.DomainSlug == "" {
			return true
		}
	}
	return false
}

// listSidecarDocs 遍历 repo 找全部有 sidecar（.md.kg.yaml）的文档（相对路径）。
// 跳过 .git 等隐藏目录。
// listSidecarDocs walks the repo for docs that have a sidecar.
func listSidecarDocs(repoPath string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(repoPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && path != repoPath {
				return filepath.SkipDir // 跳过 .git 等隐藏目录
			}
			if strings.HasSuffix(d.Name(), ".md.kg.yaml") {
				rel, rerr := filepath.Rel(repoPath, path)
				if rerr != nil {
					return rerr
				}
				out = append(out, strings.TrimSuffix(filepath.ToSlash(rel), ".kg.yaml"))
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".md.kg.yaml") {
			rel, rerr := filepath.Rel(repoPath, path)
			if rerr != nil {
				return rerr
			}
			out = append(out, strings.TrimSuffix(rel, ".kg.yaml"))
		}
		return nil
	})
	return out, err
}

// listSharedPrimaryDocs 枚举 _shared/<domain>/<uuid>.md，本体存在即纳入候选，
// 不依赖 sidecar/file_state（它们恰可能是本轮要修复的损坏对象）。
func listSharedPrimaryDocs(repoPath string) ([]string, error) {
	root := filepath.Join(repoPath, "_shared")
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		rel, err := filepath.Rel(repoPath, path)
		if err != nil {
			return err
		}
		if _, ok := sharedPrimaryUUID(rel); ok {
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sortStrings(out)
	return out, err
}

type primaryArtifact struct {
	PrimaryRel string
	UUID       string
}

// listSharedPrimaryArtifacts discovers either side of a primary pair.  It
// treats a directory named *.md or *.md.kg.yaml as an artifact too: partial
// delete failures commonly leave exactly that shape behind, and WalkDir would
// otherwise skip it before I-9 gets a chance to retry.
func listSharedPrimaryArtifacts(repoPath string) ([]primaryArtifact, error) {
	root := filepath.Join(repoPath, "_shared")
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	byPath := make(map[string]primaryArtifact)
	err := filepath.WalkDir(root, func(full string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() && strings.HasPrefix(d.Name(), ".") && full != root {
			return filepath.SkipDir
		}
		base := d.Name()
		isSidecar := strings.HasSuffix(base, ".md.kg.yaml")
		isMD := strings.HasSuffix(base, ".md") && !isSidecar
		if !isMD && !isSidecar {
			return nil
		}
		rel, err := filepath.Rel(repoPath, full)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		primaryRel := rel
		if isSidecar {
			primaryRel = strings.TrimSuffix(rel, ".kg.yaml")
		}
		uuid, ok := sharedPrimaryArtifactIdentity(primaryRel)
		if !ok {
			return nil
		}
		byPath[primaryRel] = primaryArtifact{PrimaryRel: primaryRel, UUID: uuid}
		if d.IsDir() {
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]primaryArtifact, 0, len(byPath))
	for _, a := range byPath {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PrimaryRel < out[j].PrimaryRel })
	return out, nil
}

// sharedPrimaryArtifactIdentity accepts canonical and legacy/nested artifact
// paths for cleanup.  Recovery writes only the canonical path; non-canonical
// artifacts are consequently stale and are removed after that write.
func sharedPrimaryArtifactIdentity(relPath string) (string, bool) {
	clean := filepath.ToSlash(filepath.Clean(relPath))
	if !strings.HasPrefix(clean, "_shared/") {
		return "", false
	}
	base := filepath.Base(clean)
	if strings.HasSuffix(base, ".md.kg.yaml") {
		base = strings.TrimSuffix(base, ".md.kg.yaml")
	} else if strings.HasSuffix(base, ".md") {
		base = strings.TrimSuffix(base, ".md")
	} else {
		return "", false
	}
	if base == "" || strings.ContainsAny(base, "/\\") {
		return "", false
	}
	return base, true
}

// listExpectedSharedPrimaryDocs enumerates canonical primary paths from KG
// state, including paths whose filesystem artifacts are completely absent.
// Sharedness is derived from distinct node_sources file paths, matching the
// writeback rule used by syncPrimaries.
//
// 同时返回「primary 路径 → node UUID」映射：引入 file_slug 后 primary 文件名不再
// 等于 UUID，调用方必须用该映射建立身份，不能从路径 basename 反解。
func listExpectedSharedPrimaryDocs(ctx context.Context, st *stores) ([]string, map[string]string, error) {
	nodes, err := st.nodes.ListAll(ctx)
	if err != nil {
		return nil, nil, err
	}
	seen := make(map[string]bool)
	uuidByPath := make(map[string]string)
	var out []string
	for _, node := range nodes {
		if node == nil || (node.Label != dktypes.LabelEntity && node.Label != dktypes.LabelConcept) {
			continue
		}
		srcs, err := st.sources.ListByNode(ctx, node.ID)
		if err != nil {
			return nil, nil, err
		}
		if len(distinctFilePaths(srcs)) < 2 {
			continue
		}
		// Domain is the canonical slug in KG. Reject traversal/empty values so
		// a corrupt node cannot make the scanner escape repoRoot.
		if node.Domain == "" || strings.ContainsAny(node.Domain, "/\\") ||
			strings.ContainsAny(node.ID, "/\\") || node.ID == "" {
			continue
		}
		rel := writeback.PrimaryRelPath(node.Domain, node.FileSlug, node.ID)
		if !IsSharedPrimaryPath(rel) || seen[rel] {
			continue
		}
		seen[rel] = true
		uuidByPath[rel] = node.ID
		out = append(out, rel)
	}
	sort.Strings(out)
	return out, uuidByPath, nil
}

func sharedPrimaryUUID(relPath string) (string, bool) {
	parts := strings.Split(filepath.ToSlash(filepath.Clean(relPath)), "/")
	if len(parts) != 3 || parts[0] != "_shared" || parts[1] == "" ||
		!strings.HasSuffix(parts[2], ".md") {
		return "", false
	}
	uuid := strings.TrimSuffix(parts[2], ".md")
	return uuid, uuid != ""
}

// canonicalPrimaryKey 把 _shared 产物路径归一为 primary md 路径，用于查 KG 权威
// 身份映射（primaryUUIDByPath 的 key 始终是 `_shared/<domain>/<name>.md`）。
// sidecar 路径 `<primary>.md.kg.yaml` 归一为 `<primary>.md`。
func canonicalPrimaryKey(relPath string) string {
	clean := filepath.ToSlash(filepath.Clean(relPath))
	return strings.TrimSuffix(clean, ".kg.yaml")
}

// resolveAndPruneC4Docs 建立 C4 文档提交闭包并原地裁剪 ao：任何文档只要有一个
// 单元未接受，其全部 pending/sourceRows 都不提交；跨文档 pending 把失败传播到
// 所有参与文档，直到固定点。返回后 ao 只包含可原子提交的 resolved 文档贡献。
func resolveAndPruneC4Docs(pp *PipelinePlan, annotated *annotateOutput, ao *alignOutcome) (map[string]bool, []UnresolvedDoc) {
	resolved := make(map[string]bool)
	invalid := make(map[string]bool)
	reasons := make(map[string]string)

	// 每篇文档的单元，以及每个单元涉及的全部来源文档（04 id 可跨文档复用）。
	unitsByDoc := make(map[string][]string)
	docsByUnit := make(map[string]map[string]bool)
	for _, d := range annotated.Docs {
		for _, it := range d.Items {
			if it.ID != "" {
				unitsByDoc[d.FilePath] = append(unitsByDoc[d.FilePath], it.ID)
				if docsByUnit[it.ID] == nil {
					docsByUnit[it.ID] = make(map[string]bool)
				}
				docsByUnit[it.ID][d.FilePath] = true
			}
		}
	}
	for _, s := range annotated.Skipped {
		invalid[s.Path] = true
		reasons[s.Path] = s.Reason
	}
	paths := make([]string, 0, len(pp.C4ByDoc))
	for p := range pp.C4ByDoc {
		paths = append(paths, p)
	}
	sortStrings(paths)
	for _, path := range paths {
		if ao != nil && ao.llFailed {
			invalid[path] = true
			reasons[path] = "I-4 增量对齐 LLM 失败（降级，未吸收任何单元）"
		} else if !invalid[path] && len(unitsByDoc[path]) == 0 {
			invalid[path] = true
			reasons[path] = "标注零产出（新增文本未含可吸收的 entity/concept 单元）"
		}
	}

	droppedPending := make(map[string]bool)
	for changed := true; changed; {
		changed = false
		accepted := make(map[string]bool)
		persistableSource := make(map[string]bool) // member\x00file
		if ao != nil {
			for uuid, pn := range ao.pending {
				if droppedPending[uuid] {
					continue
				}
				for _, member := range pn.NewMembers {
					accepted[member] = true
				}
			}
			for _, row := range ao.sourceRows {
				if !droppedPending[row.NodeUUID] {
					persistableSource[row.MemberID+"\x00"+row.FilePath] = true
				}
			}
		}
		// 未被当前可提交 pending 覆盖的任一单元使整篇文档失败。
		for _, path := range paths {
			if invalid[path] {
				continue
			}
			var missing []string
			for _, id := range unitsByDoc[path] {
				if !accepted[id] || !persistableSource[id+"\x00"+path] {
					missing = append(missing, id)
				}
			}
			if len(missing) > 0 {
				invalid[path] = true
				reasons[path] = fmt.Sprintf("%d/%d 个单元未被吸收（低置信拒绝/未物化）: %s",
					len(missing), len(unitsByDoc[path]), strings.Join(missing, "、"))
				changed = true
			}
		}
		// 一个 pending 只要涉及失败文档，就整体丢弃，并把同 pending 的其它文档
		// 也纳入失败闭包；下一轮会作为整体重新对齐，绝不半提交共享融合。
		if ao != nil {
			for uuid, pn := range ao.pending {
				if droppedPending[uuid] {
					continue
				}
				pendingDocs := make(map[string]bool)
				for _, member := range pn.NewMembers {
					for path := range docsByUnit[member] {
						pendingDocs[path] = true
					}
				}
				bad := false
				for path := range pendingDocs {
					bad = bad || invalid[path]
				}
				if !bad {
					continue
				}
				droppedPending[uuid] = true
				changed = true
				for path := range pendingDocs {
					if !invalid[path] {
						invalid[path] = true
						reasons[path] = "与未解决文档共享同一融合提交组（闭包回滚，下轮整体重试）"
					}
				}
			}
		}
	}

	if ao != nil {
		for uuid := range droppedPending {
			delete(ao.pending, uuid)
		}
		filteredRows := ao.sourceRows[:0]
		for _, row := range ao.sourceRows {
			if ao.pending[row.NodeUUID] != nil && !invalid[row.FilePath] {
				filteredRows = append(filteredRows, row)
			}
		}
		ao.sourceRows = filteredRows
	}

	var unresolved []UnresolvedDoc
	for _, path := range paths {
		if invalid[path] {
			reason := reasons[path]
			if reason == "" {
				reason = "C4 文档提交闭包未满足"
			}
			unresolved = append(unresolved, UnresolvedDoc{Path: path, Reason: reason})
		} else {
			resolved[path] = true
		}
	}
	return resolved, unresolved
}

// gateByConfidence 置信度门控：新建 node 低于 MinConfidence 的不落库（与全量 ingest
// 的门控语义一致）。问题 6：必须同步删除其 sourceRows（绝不产生孤儿 node_sources）；
// 被移除单元的来源文档由 resolveC4Docs 按「未被接受」标为未解决（散文保留）。
// gateByConfidence filters new nodes below MinConfidence and removes their
// source rows too (never orphan node_sources).
func (o *IncrementalOrchestrator) gateByConfidence(ao *alignOutcome, rs *runState) {
	rejected := make(map[string]bool)
	for uuid, pn := range ao.pending {
		if pn.IsNew && pn.Confidence < o.minConf {
			rs.warnf(fmt.Sprintf("新建 node %s（%s）置信度 %.2f < 门控 %.2f，不落库",
				uuid, pn.Name, pn.Confidence, o.minConf))
			delete(ao.pending, uuid)
			rejected[uuid] = true
		}
	}
	if len(rejected) == 0 {
		return
	}
	// 同步清理被拒绝 node 的 sourceRows（问题 6）。
	filtered := ao.sourceRows[:0]
	for _, row := range ao.sourceRows {
		if !rejected[row.NodeUUID] {
			filtered = append(filtered, row)
		}
	}
	ao.sourceRows = filtered
}

// syncFileStates 同步 file_states baseline：对本轮全部候选文档，文件存在 →
// Upsert 其当前内容 hash（I-9 回写后的最终内容）；整篇删除 → Delete 记录。
// 这同时完成了坑点 13 的「首轮 baseline 建立」（全量不写 file_states，增量首轮补写）。
//
// 问题 5（baseline 只能为「确认未变」或「本轮成功提交并成功回写」的文档推进）：
//   - C4 未解决文档（rs.c4Preserve）→ 跳过（保留旧 baseline，下轮重试）；
//   - I-9 写盘失败文档（rs.failedDocs）→ 跳过（同上）；
//   - 其余（未变 / 已成功回写 / 已成功建立）→ 推进。
//
// syncFileStates upserts file_states baselines for candidate docs (reading the
// post-writeback content) and deletes records for wholly deleted docs. Docs with
// unresolved C4 or failed writes keep their old baseline (retry next round).
func (o *IncrementalOrchestrator) syncFileStates(ctx context.Context, st *stores,
	repoURL string, sr *scanResult, deletedDocs []string, repoPath string, rs *runState) error {
	deletedSet := make(map[string]bool, len(deletedDocs))
	for _, p := range deletedDocs {
		deletedSet[p] = true
	}
	failedSet := rs.failedDocsSnapshot()
	for _, relPath := range sr.all {
		if deletedSet[relPath] {
			if failedSet[relPath] {
				continue // sidecar/primary 删除失败：保留 baseline，下轮继续重试
			}
			if err := st.files.Delete(ctx, repoURL, relPath); err != nil {
				return fmt.Errorf("incremental: delete file_state %s: %w", relPath, err)
			}
			continue
		}
		// 未解决/写失败的文档：baseline 不前移（下轮重试）。
		if _, unresolved := rs.c4Preserve[relPath]; unresolved {
			// 首轮增量通常尚无 file_state。若同文档因 C1/C2/C3 被回写并生成
			// 新 sidecar，必须钉住旧 baseline，确保下一轮仍检测到未解决 C4。
			rec, err := st.files.Get(ctx, repoURL, relPath)
			if err != nil {
				return fmt.Errorf("incremental: read retry file_state %s: %w", relPath, err)
			}
			if rec == nil && rs.retryBaselines[relPath] != "" {
				if err := st.files.Upsert(ctx, repoURL, relPath, rs.retryBaselines[relPath]); err != nil {
					return fmt.Errorf("incremental: pin retry file_state %s: %w", relPath, err)
				}
			}
			continue
		}
		if failedSet[relPath] {
			continue
		}
		data, err := os.ReadFile(filepath.Join(repoPath, relPath))
		if err != nil {
			if os.IsNotExist(err) && IsSharedPrimaryPath(relPath) {
				// A successfully reconciled shared→non-shared/true-delete primary
				// is expected to be absent. Remove its stale file_state; failed I-9
				// paths were filtered above and retain the baseline for retry.
				if delErr := st.files.Delete(ctx, repoURL, relPath); delErr != nil {
					return fmt.Errorf("incremental: delete absent primary file_state %s: %w", relPath, delErr)
				}
			}
			continue // other missing files keep their prior state
		}
		if err := st.files.Upsert(ctx, repoURL, relPath, writeback.HashEditable(string(data))); err != nil {
			return fmt.Errorf("incremental: upsert file_state %s: %w", relPath, err)
		}
	}
	return nil
}

// ——————————————————————————————————————————————————————————————————————————————
// 计数小工具 / Counting helpers
// ——————————————————————————————————————————————————————————————————————————————

func countEdits(m map[string][]BlockEdit) int {
	n := 0
	for _, v := range m {
		n += len(v)
	}
	return n
}

func countDeletions(m map[string][]string) int {
	n := 0
	for _, v := range m {
		n += len(v)
	}
	return n
}

func countSegments(m map[string][]TextSegment) int {
	n := 0
	for _, v := range m {
		n += len(v)
	}
	return n
}
