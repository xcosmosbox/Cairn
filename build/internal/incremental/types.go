// Package incremental 的本文件定义增量流水线各 stage 共享的运行态类型：
// 仓储聚合（stores）与跨阶段运行态（runState）。
//
// runState 在 I-1..I-9 之间携带脏集、删除快照、受影响文档集合与告警，
// 是「脏集封闭（R9）」与「镜像/primary 同步（I-9）」的数据载体。
package incremental

import (
	"sync"

	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

// stores 聚合本轮增量用到的全部仓储（由编排器从 *storage.DB 构造；测试用内存库）。
// stores aggregates every repository the incremental run uses.
type stores struct {
	nodes     *storage.NodeRepo
	edges     *storage.EdgeRepo
	sources   *storage.NodeSourceRepo
	fts       *storage.FTSIndex
	files     *storage.FileStateRepo
	version   *storage.KBVersionRepo
	manifest  *storage.ManifestRepo
	identity  *storage.RepositoryIdentityRepo
	mutations *storage.IncrementalMutationRepo
	lineage   *storage.UUIDLineageRepo // 重整 merge/split 血缘（第三块）/ rebalance lineage
}

// newStores 从 DB 构造全部仓储。
// newStores builds all repositories from a DB.
func newStores(db *storage.DB) *stores {
	return &stores{
		nodes:     storage.NewNodeRepo(db),
		edges:     storage.NewEdgeRepo(db),
		sources:   storage.NewNodeSourceRepo(db),
		fts:       storage.NewFTSIndex(db),
		files:     storage.NewFileStateRepo(db),
		version:   storage.NewKBVersionRepo(db),
		manifest:  storage.NewManifestRepo(db),
		identity:  storage.NewRepositoryIdentityRepo(db),
		mutations: storage.NewIncrementalMutationRepo(db),
		lineage:   storage.NewUUIDLineageRepo(db),
	}
}

// deletedNodeInfo 是被真删 node 的快照（I-9 需要：删 primary、重写镜像文档、
// 脏子域 relation 重算时排除已删端点）。
// deletedNodeInfo snapshots a truly deleted node for I-9 (primary removal,
// mirror-doc rewrite) and I-7 (dangling-endpoint exclusion).
type deletedNodeInfo struct {
	Node       *dktypes.Node // 删除前的 KG 快照（Domain/Subdomain 为 slug）/ pre-delete snapshot
	WasShared  bool          // 删除前是否 shared（≥2 来源文档）/ shared before deletion
	SourceDocs []string      // 删除前 distinct 来源文档 / pre-deletion distinct sources
	RowID      int64         // 删除前 nodes.rowid（FTS 残留清理用，问题 5/safety 4）
}

// UnresolvedDoc 是一篇「C4 新增未完全吸收」的文档及其原因（问题 2：可观测降级）。
// 未解决文档的人类散文必须保留、file_state baseline 不得前移（下一轮可重试）。
//
// UnresolvedDoc is a doc whose C4 additions were not fully absorbed, with the
// reason. Its prose is preserved and its baseline is NOT advanced.
type UnresolvedDoc struct {
	Path   string
	Reason string
}

// touchedNodeInfo 记录一个「被触碰但存活」的 node 的变化前状态
// （C2 存活 / C1 内容变）：I-9 据此重写其全部来源文档（含 shared 状态翻转：
// 镜像↔完整块转换、primary 建立/删除）。
// touchedNodeInfo records the pre-change state of a touched-but-surviving node
// (C2 survivor / C1 content change) so I-9 can rewrite all its source docs and
// handle shared-status flips.
type touchedNodeInfo struct {
	SourceDocs []string // 变化前 distinct 来源文档 / pre-change distinct sources
	WasShared  bool
}

// runState 是一次增量运行的跨阶段状态（I-1..I-9 逐步填充）。
//
// 并发安全（问题 8）：mu 保护 warnings/affectedDocs/failedDocs——I-6 按子域并发
// 重融合时多个 goroutine 会同时 warnf；其余字段（dirty/deleted/touched/c4Preserve）
// 只在顺序阶段写入（bypass/I-5/编排器），读取也发生在顺序阶段，故无需加锁。
// 读取共享字段一律走快照方法（warningsSnapshot 等），不直接读 slice/map。
//
// runState carries cross-stage state for one incremental run. The mutex guards
// warnings/affectedDocs/failedDocs (concurrently written by I-6's per-subdomain
// fan-out); all other fields are written and read in sequential phases.
type runState struct {
	mu sync.Mutex // 保护 warnings / affectedDocs / failedDocs（问题 8）

	dirty *DirtySet
	// deleted 是真删 node（引用计数归零）uuid → 快照。
	// deleted maps truly deleted node uuids to their snapshots.
	deleted map[string]*deletedNodeInfo
	// touched 是被触碰但存活的 node uuid → 变化前状态（C1/C2 存活/融入/新建不含）。
	// touched maps touched-but-surviving node uuids to pre-change states.
	touched map[string]*touchedNodeInfo
	// affectedDocs 是 I-9 需要重写的文档集合（相对 repo 根路径）。
	// affectedDocs is the set of docs I-9 must rewrite (repo-relative paths).
	affectedDocs map[string]bool
	// c4Preserve 是「C4 未解决」文档 → 必须原样保留的散文（问题 2）：
	// I-9 重写这些文档（因它们同时有 C1/C2/C3 变更）时，把未吸收散文追加回文档末尾，
	// 绝不静默删除尚未被 KG 持久化的人类输入。
	// c4Preserve maps docs with unresolved C4 to the prose that must be preserved
	// verbatim when the doc is rewritten for other (C1/C2/C3) reasons.
	c4Preserve map[string]string
	// retryBaselines 保存未解决 C4 文档在本轮开始前的 baseline。若同文档因
	// C1/C2/C3 必须回写并生成新 sidecar，仍把旧 baseline 写进 file_states，
	// 保证下一轮必然再次进入细判，而不会被新 sidecar 吞掉重试信号。
	retryBaselines map[string]string
	// failedDocs 是 I-9 写盘失败的文档（问题 5：baseline 不前移，下轮重试）。
	// failedDocs lists docs whose I-9 write failed (baseline not advanced).
	failedDocs map[string]bool
	// warnings 是全部可观测告警（C3 篡改、phantom uuid 等）。
	// warnings collects all observability warnings.
	warnings []string
}

// newRunState 构造空运行态。
// newRunState constructs an empty run state.
func newRunState() *runState {
	return &runState{
		dirty:          NewDirtySet(),
		deleted:        make(map[string]*deletedNodeInfo),
		touched:        make(map[string]*touchedNodeInfo),
		affectedDocs:   make(map[string]bool),
		c4Preserve:     make(map[string]string),
		retryBaselines: make(map[string]string),
		failedDocs:     make(map[string]bool),
	}
}

// warnf 追加一条告警（可观测）。并发安全（问题 8：I-6 并发子域会同时调用）。
// warnf appends one observability warning (concurrency-safe).
func (rs *runState) warnf(msg string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.warnings = append(rs.warnings, msg)
}

// warningsSnapshot 返回告警的安全快照（并发阶段结束后取用，问题 8）。
// warningsSnapshot returns a safe copy of the warnings.
func (rs *runState) warningsSnapshot() []string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return append([]string(nil), rs.warnings...)
}

// affectDocs 把若干文档加入 I-9 受影响集合。并发安全。
// affectDocs adds docs to the I-9 affected set (concurrency-safe).
func (rs *runState) affectDocs(paths ...string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	for _, p := range paths {
		if p != "" {
			rs.affectedDocs[p] = true
		}
	}
}

// affectedDocsSnapshot 返回受影响文档集合的安全快照。
// affectedDocsSnapshot returns a safe copy of the affected-docs set.
func (rs *runState) affectedDocsSnapshot() map[string]bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	out := make(map[string]bool, len(rs.affectedDocs))
	for p := range rs.affectedDocs {
		out[p] = true
	}
	return out
}

// failDoc 记录一篇 I-9 写盘失败的文档（问题 5：baseline 不前移，下轮重试）。
// 并发安全。
// failDoc records a doc whose I-9 write failed (baseline not advanced).
func (rs *runState) failDoc(path string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.failedDocs[path] = true
}

// failedDocsSnapshot 返回写盘失败文档集合的安全快照。
// failedDocsSnapshot returns a safe copy of the failed-docs set.
func (rs *runState) failedDocsSnapshot() map[string]bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	out := make(map[string]bool, len(rs.failedDocs))
	for p := range rs.failedDocs {
		out[p] = true
	}
	return out
}
