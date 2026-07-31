// Package evolve 是演化观测套件的捕获层（E-2）：每次 cairn-ingest / cairn-incremental /
// cairn-rebalance 跑完后，作为旁路收尾（R-ev-1）完成——
//  1. 打开/建立独立的 evolution.db（演化层自己的持久化，绝不写 KG 库，R-ev-2）；
//  2. 把当前 KG 以 VACUUM INTO 归档为 snapshots/kg-<seq>-<kbver>.db（全部保留，不滚动淘汰）；
//  3. 取上一条 changeset 的快照作 parent，用 observe.Diff 产出血缘感知变更；
//  4. 采样 core/metrics 哨兵指标 + incremental.stats 的累计改动占比；
//  5. 写 changesets + metrics 各一行，生成人读 summary 供终端即时打印。
//
// 铁律：零 LLM、零向量依赖（R-ev-4）；diff 分类与排序确定性（R-ev-5）；
// 复用 storage 仓储与 core/metrics（R-ev-6），不重造。
//
// Package evolve is the capture layer of the evolution suite (E-2): after each
// build run it archives a full KG snapshot, diffs it against the previous
// snapshot lineage-aware, appends one changeset + one metrics row to the
// standalone evolution.db, and returns a human-readable summary.
package evolve

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite" // SQLite driver（与 core/storage 同源，纯 Go）

	"github.com/xcosmosbox/cairn/core/metrics"
	"github.com/xcosmosbox/cairn/core/observe"
	"github.com/xcosmosbox/cairn/core/storage"
)

// changesetsSchema / metricsSchema 是 evolution.db 的独立 schema（与 KG schema 无关）。
const changesetsSchema = `CREATE TABLE IF NOT EXISTS changesets (
  seq            INTEGER PRIMARY KEY AUTOINCREMENT,
  ts             TEXT NOT NULL,
  tool           TEXT NOT NULL,
  kb_version     TEXT,
  parent_version TEXT,
  snapshot_file  TEXT NOT NULL,
  summary        TEXT,
  trigger_reason TEXT,
  diff_json      TEXT,
  docs_affected  TEXT,
  n_added INT, n_deleted INT, n_merged INT, n_split INT,
  n_migrated INT, n_renamed INT, n_content INT,
  n_edge_added INT, n_edge_dropped INT
)`

const metricsSchema = `CREATE TABLE IF NOT EXISTS metrics (
  seq            INTEGER,
  ts             TEXT,
  kb_version     TEXT,
  modularity_q   REAL, singleton_ratio REAL, edge_node_ratio REAL,
  node_count     INT,  edge_count      INT,  cumulative_ratio REAL
)`

// summaryMaxLines 是终端摘要的事件流截断上限（完整事件流由 cairn show / 前端渲染）。
const summaryMaxLines = 80

// DefaultDir 返回演化产物目录的默认值：<KG 库所在目录>/evolution（E-3 各 CLI
// --evolution-dir 的缺省）。
//
// DefaultDir returns the default evolution workspace next to the KG db file.
func DefaultDir(kgDBPath string) string {
	return filepath.Join(filepath.Dir(kgDBPath), "evolution")
}

// docsAffectedCap 是 docs_affected 的去重文件路径上限（防爆宽）。
const docsAffectedCap = 200

// Record 是演化层的旁路收尾入口。失败仅由调用方告警，绝不影响构建退出码（R-ev-1）。
// 返回的 summary 供调用方直接打印到终端（即时性，零额外操作）。
//
// Record captures one evolution changeset after a build run and returns the
// human-readable summary for immediate terminal printing.
func Record(ctx context.Context, kgDBPath, evolutionDir, tool string) (summary string, err error) {
	if kgDBPath == "" || evolutionDir == "" {
		return "", fmt.Errorf("evolve.Record: kgDBPath 与 evolutionDir 均不可为空")
	}
	absDir, err := filepath.Abs(evolutionDir)
	if err != nil {
		return "", fmt.Errorf("evolve.Record: 解析演化目录失败: %w", err)
	}
	snapDir := filepath.Join(absDir, "snapshots")
	if err := os.MkdirAll(snapDir, 0o755); err != nil {
		return "", fmt.Errorf("evolve.Record: 建快照目录失败: %w", err)
	}

	// 1. 打开/建 evolution.db（独立库；演化层唯一允许写的库）。
	evDB, err := openEvolutionDB(filepath.Join(absDir, "evolution.db"))
	if err != nil {
		return "", err
	}
	defer evDB.Close()

	// 上一条 changeset → parent（首次运行为空 → Diff(nil, cur) 全 added）。
	parentSeq, parentFile, parentVersion, err := lastChangeset(ctx, evDB)
	if err != nil {
		return "", err
	}
	seq := parentSeq + 1

	// 2. 打开当前 KG（只读意图：NewDB 迁移幂等不改数据，仅执行只读查询与
	//    VACUUM INTO 归档——VACUUM INTO 写的是新快照文件，不是 KG 本身）。
	kg, err := storage.NewDB(storage.DBOptions{Path: kgDBPath})
	if err != nil {
		return "", fmt.Errorf("evolve.Record: 打开 KG 失败: %w", err)
	}
	defer kg.Close()

	kbVersion, err := storage.NewKBVersionRepo(kg).Current(ctx)
	if err != nil {
		return "", fmt.Errorf("evolve.Record: 读 KB 版本失败: %w", err)
	}

	// 3. 归档当前 KG 终态快照（全部保留）+ 完整性校验。
	snapFile, err := archiveSnapshot(ctx, kg, snapDir, seq, kbVersion)
	if err != nil {
		return "", err
	}

	// 4. 血缘感知 diff：parent 快照 vs 当前 KG。
	var from *storage.DB
	if parentFile != "" {
		from, err = storage.OpenReadOnly(filepath.Join(snapDir, parentFile))
		if err != nil {
			return "", fmt.Errorf("evolve.Record: 打开 parent 快照 %s 失败: %w", parentFile, err)
		}
		defer from.Close()
	}
	diff, err := observe.Diff(ctx, from, kg)
	if err != nil {
		return "", fmt.Errorf("evolve.Record: diff 失败: %w", err)
	}
	diffJSON, err := diff.JSON()
	if err != nil {
		return "", fmt.Errorf("evolve.Record: diff 序列化失败: %w", err)
	}

	// 5. 指标采样 + 累计改动占比 + 触发原因 + 受影响文档。
	snap, cumulative, err := sampleMetrics(ctx, kg)
	if err != nil {
		return "", err
	}
	trigger := triggerReason(ctx, kg, tool)
	docs := docsAffected(ctx, kg, diff)
	docsJSON, err := json.Marshal(docs)
	if err != nil {
		return "", fmt.Errorf("evolve.Record: docs_affected 序列化失败: %w", err)
	}

	ts := time.Now().UTC().Format(time.RFC3339)
	summary = buildSummary(seq, tool, ts, kbVersion, parentVersion, trigger, diff)

	// 6. 写 changesets + metrics（一个事务；只写 evolution.db）。
	tx, err := evDB.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("evolve.Record: 开事务失败: %w", err)
	}
	defer tx.Rollback() // 已提交则无操作

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO changesets (seq, ts, tool, kb_version, parent_version, snapshot_file,
		   summary, trigger_reason, diff_json, docs_affected,
		   n_added, n_deleted, n_merged, n_split, n_migrated, n_renamed, n_content,
		   n_edge_added, n_edge_dropped)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		seq, ts, tool, kbVersion, parentVersion, snapFile, summary, trigger,
		string(diffJSON), string(docsJSON),
		diff.Counts["added"], diff.Counts["deleted"], diff.Counts["merged"],
		diff.Counts["split"], diff.Counts["migrated"], diff.Counts["renamed"],
		diff.Counts["content"], diff.Counts["edge_added"], diff.Counts["edge_dropped"]); err != nil {
		return "", fmt.Errorf("evolve.Record: 写 changesets 失败: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO metrics (seq, ts, kb_version, modularity_q, singleton_ratio,
		   edge_node_ratio, node_count, edge_count, cumulative_ratio)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		seq, ts, kbVersion, snap.ModularityQ, snap.SingletonRatio, snap.EdgeNodeRatio,
		snap.NodeCount, snap.EdgeCount, cumulative); err != nil {
		return "", fmt.Errorf("evolve.Record: 写 metrics 失败: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("evolve.Record: 提交失败: %w", err)
	}
	return summary, nil
}

// openEvolutionDB 打开（不存在则创建）独立演化库并建表。
func openEvolutionDB(path string) (*sql.DB, error) {
	conn, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("evolve: 打开 evolution.db 失败: %w", err)
	}
	conn.SetMaxOpenConns(1)
	for _, ddl := range []string{changesetsSchema, metricsSchema} {
		if _, err := conn.Exec(ddl); err != nil {
			conn.Close()
			return nil, fmt.Errorf("evolve: 建演化表失败: %w", err)
		}
	}
	return conn, nil
}

// lastChangeset 返回最新一条 changeset 的 seq / snapshot_file / kb_version；
// 首次运行（空表）返回零值与空串。
func lastChangeset(ctx context.Context, evDB *sql.DB) (seq int64, snapshotFile, kbVersion string, err error) {
	err = evDB.QueryRowContext(ctx,
		`SELECT seq, snapshot_file, COALESCE(kb_version, '')
		   FROM changesets ORDER BY seq DESC LIMIT 1`).Scan(&seq, &snapshotFile, &kbVersion)
	if err == sql.ErrNoRows {
		return 0, "", "", nil
	}
	if err != nil {
		return 0, "", "", fmt.Errorf("evolve: 读上一条 changeset 失败: %w", err)
	}
	return seq, snapshotFile, kbVersion, nil
}

// archiveSnapshot 用 VACUUM INTO 把 KG 终态复制为独立快照文件（WAL 安全、原子、
// 不写源库），随后以读写方式打开一次快照做完整性校验（节点/边计数与源一致）。
// VACUUM INTO 的输出是全新文件；校验打开只触碰快照，不触碰 KG。
func archiveSnapshot(ctx context.Context, kg *storage.DB, snapDir string, seq int64, kbVersion string) (string, error) {
	fileName := fmt.Sprintf("kg-%06d-%s.db", seq, sanitizeFilePart(kbVersion))
	absPath := filepath.Join(snapDir, fileName)
	if _, err := os.Stat(absPath); err == nil {
		return "", fmt.Errorf("evolve: 快照文件已存在（seq 冲突）: %s", fileName)
	}
	quoted := "'" + strings.ReplaceAll(absPath, "'", "''") + "'"
	if _, err := kg.Conn().ExecContext(ctx, `VACUUM INTO `+quoted); err != nil {
		return "", fmt.Errorf("evolve: 归档快照失败: %w", err)
	}

	// 完整性校验：快照行数必须与源一致（防 WAL 归档残缺静默入账）。
	srcNodes, err := storage.NewNodeRepo(kg).ListAll(ctx)
	if err != nil {
		return "", fmt.Errorf("evolve: 源节点计数失败: %w", err)
	}
	srcEdges, err := storage.NewEdgeRepo(kg).ListAll(ctx)
	if err != nil {
		return "", fmt.Errorf("evolve: 源边计数失败: %w", err)
	}
	snapDB, err := storage.NewDB(storage.DBOptions{Path: absPath})
	if err != nil {
		return "", fmt.Errorf("evolve: 校验打开快照失败: %w", err)
	}
	defer snapDB.Close()
	snapNodes, err := storage.NewNodeRepo(snapDB).ListAll(ctx)
	if err != nil {
		return "", fmt.Errorf("evolve: 快照节点计数失败: %w", err)
	}
	snapEdges, err := storage.NewEdgeRepo(snapDB).ListAll(ctx)
	if err != nil {
		return "", fmt.Errorf("evolve: 快照边计数失败: %w", err)
	}
	if len(snapNodes) != len(srcNodes) || len(snapEdges) != len(srcEdges) {
		return "", fmt.Errorf("evolve: 快照不完整（节点 %d/%d 边 %d/%d）",
			len(snapNodes), len(srcNodes), len(snapEdges), len(srcEdges))
	}
	return fileName, nil
}

// sanitizeFilePart 把版本号清洗为文件名片段（RFC3339 的 ':' 等替换成 '-'）。
func sanitizeFilePart(s string) string {
	if s == "" {
		return "noversion"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// sampleMetrics 采样哨兵指标与累计改动占比（incremental.stats，缺省 0）。
func sampleMetrics(ctx context.Context, kg *storage.DB) (metrics.Snapshot, float64, error) {
	g, err := metrics.LoadGraph(ctx, storage.NewNodeRepo(kg), storage.NewEdgeRepo(kg))
	if err != nil {
		return metrics.Snapshot{}, 0, fmt.Errorf("evolve: 装载指标图失败: %w", err)
	}
	snap := metrics.Compute(g)

	cumulative := 0.0
	raw, err := storage.NewManifestRepo(kg).Get(ctx, "incremental.stats")
	if err != nil {
		return metrics.Snapshot{}, 0, fmt.Errorf("evolve: 读 incremental.stats 失败: %w", err)
	}
	if raw != "" {
		var st struct {
			CumulativeChangeRatio float64 `json:"cumulative_change_ratio"`
		}
		if err := json.Unmarshal([]byte(raw), &st); err == nil {
			cumulative = st.CumulativeChangeRatio
		}
	}
	return snap, cumulative, nil
}

// triggerReason 取本次运行的触发原因：rebalance 读 rebalance.last 的
// trigger_reasons；incremental 给变更分类概述；ingest 留空（全量重建无需原因）。
func triggerReason(ctx context.Context, kg *storage.DB, tool string) string {
	mf := storage.NewManifestRepo(kg)
	switch tool {
	case "rebalance":
		raw, err := mf.Get(ctx, "rebalance.last")
		if err != nil || raw == "" {
			return ""
		}
		var v struct {
			TriggerReasons []string `json:"trigger_reasons"`
		}
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return ""
		}
		return strings.Join(v.TriggerReasons, "; ")
	case "incremental":
		raw, err := mf.Get(ctx, "incremental.stats")
		if err != nil || raw == "" {
			return ""
		}
		var st struct {
			DocsChangedThisRun int `json:"docs_changed_this_run"`
			DocsTotal          int `json:"docs_total"`
		}
		if err := json.Unmarshal([]byte(raw), &st); err != nil {
			return ""
		}
		return fmt.Sprintf("本轮实质变更文档 %d / 共 %d", st.DocsChangedThisRun, st.DocsTotal)
	default:
		return ""
	}
}

// docsAffected 汇总本次变更节点（非 unchanged）的来源文件路径，去重升序、截断。
func docsAffected(ctx context.Context, kg *storage.DB, diff *observe.DiffResult) []string {
	set := map[string]bool{}
	src := storage.NewNodeSourceRepo(kg)
	for _, nc := range diff.Nodes {
		if nc.Change == observe.ChangeUnchanged || nc.Change == observe.ChangeDeleted {
			continue // 删除节点的来源已随节点消失；unchanged 无影响
		}
		sources, err := src.ListByNode(ctx, nc.ID)
		if err != nil {
			continue // 来源缺失不阻塞记录（旁路收尾）
		}
		for _, s := range sources {
			if s.FilePath != "" {
				set[s.FilePath] = true
			}
		}
		if len(set) >= docsAffectedCap {
			break
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	if len(out) > docsAffectedCap {
		out = out[:docsAffectedCap]
	}
	return out
}

// buildSummary 生成人读摘要（写 changesets.summary，也由调用方打印到终端）。
// 事件流超 summaryMaxLines 截断，完整内容经 diff_json 由 cairn show / 前端渲染。
func buildSummary(seq int64, tool, ts, kbVersion, parentVersion, trigger string, diff *observe.DiffResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Changeset #%d · %s · %s\n", seq, tool, ts)
	if parentVersion == "" {
		fmt.Fprintf(&b, "版本: %s（首次记录，无上一版本）\n", kbVersion)
	} else {
		fmt.Fprintf(&b, "版本: %s → %s\n", parentVersion, kbVersion)
	}
	if trigger != "" {
		fmt.Fprintf(&b, "触发: %s\n", trigger)
	}
	md := diff.Markdown()
	lines := strings.Split(strings.TrimRight(md, "\n"), "\n")
	if len(lines) > summaryMaxLines {
		b.WriteString(strings.Join(lines[:summaryMaxLines], "\n"))
		fmt.Fprintf(&b, "\n  …（截断 %d 行，完整事件流见 cairn show %d）\n", len(lines)-summaryMaxLines, seq)
	} else {
		b.WriteString(md)
	}
	return strings.TrimRight(b.String(), "\n")
}

// ────────────────────────────────────────────────────────────────────────────
// 读取侧（cairn-evolve 的 /api/manifest 与 cairn timeline/show 共用，R-ev-6）
// ────────────────────────────────────────────────────────────────────────────

// ChangesetRow 是 changesets 表一行的读取视图。
type ChangesetRow struct {
	Seq           int64           `json:"seq"`
	Ts            string          `json:"ts"`
	Tool          string          `json:"tool"`
	KBVersion     string          `json:"kb_version"`
	ParentVersion string          `json:"parent_version,omitempty"`
	SnapshotFile  string          `json:"snapshot_file"`
	Summary       string          `json:"summary"`
	TriggerReason string          `json:"trigger_reason,omitempty"`
	Diff          json.RawMessage `json:"diff,omitempty"`
	DocsAffected  []string        `json:"docs_affected,omitempty"`
	NAdded        int             `json:"n_added"`
	NDeleted      int             `json:"n_deleted"`
	NMerged       int             `json:"n_merged"`
	NSplit        int             `json:"n_split"`
	NMigrated     int             `json:"n_migrated"`
	NRenamed      int             `json:"n_renamed"`
	NContent      int             `json:"n_content"`
	NEdgeAdded    int             `json:"n_edge_added"`
	NEdgeDropped  int             `json:"n_edge_dropped"`
}

// MetricRow 是 metrics 表一行的读取视图。
type MetricRow struct {
	Seq             int64   `json:"seq"`
	Ts              string  `json:"ts"`
	KBVersion       string  `json:"kb_version"`
	ModularityQ     float64 `json:"modularity_q"`
	SingletonRatio  float64 `json:"singleton_ratio"`
	EdgeNodeRatio   float64 `json:"edge_node_ratio"`
	NodeCount       int     `json:"node_count"`
	EdgeCount       int     `json:"edge_count"`
	CumulativeRatio float64 `json:"cumulative_ratio"`
}

// Manifest 是 /api/manifest 的载荷：时间轴 + 曲线数据一次拉齐。
type Manifest struct {
	Changesets []ChangesetRow `json:"changesets"`
	Metrics    []MetricRow    `json:"metrics"`
}

// ReadManifest 只读打开演化目录下的 evolution.db，按 seq 升序返回全部
// changesets 与 metrics。evolution.db 不存在时报错（调用方决定降级策略）。
//
// ReadManifest reads the full timeline + metric series from evolution.db.
func ReadManifest(ctx context.Context, evolutionDir string) (*Manifest, error) {
	path := filepath.Join(evolutionDir, "evolution.db")
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("evolve.ReadManifest: evolution.db 不存在: %w", err)
	}
	conn, err := sql.Open("sqlite", path+"?mode=ro&_query_only=true&_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("evolve.ReadManifest: 打开失败: %w", err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)

	m := &Manifest{Changesets: []ChangesetRow{}, Metrics: []MetricRow{}}

	rows, err := conn.QueryContext(ctx,
		`SELECT seq, ts, tool, COALESCE(kb_version,''), COALESCE(parent_version,''),
		        snapshot_file, COALESCE(summary,''), COALESCE(trigger_reason,''),
		        COALESCE(diff_json,''), COALESCE(docs_affected,''),
		        n_added, n_deleted, n_merged, n_split, n_migrated, n_renamed, n_content,
		        n_edge_added, n_edge_dropped
		   FROM changesets ORDER BY seq`)
	if err != nil {
		return nil, fmt.Errorf("evolve.ReadManifest: 读 changesets 失败: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var c ChangesetRow
		var diffJSON, docsJSON string
		if err := rows.Scan(&c.Seq, &c.Ts, &c.Tool, &c.KBVersion, &c.ParentVersion,
			&c.SnapshotFile, &c.Summary, &c.TriggerReason, &diffJSON, &docsJSON,
			&c.NAdded, &c.NDeleted, &c.NMerged, &c.NSplit, &c.NMigrated,
			&c.NRenamed, &c.NContent, &c.NEdgeAdded, &c.NEdgeDropped); err != nil {
			return nil, fmt.Errorf("evolve.ReadManifest: 扫描 changesets 失败: %w", err)
		}
		if diffJSON != "" {
			c.Diff = json.RawMessage(diffJSON)
		}
		if docsJSON != "" {
			var docs []string
			if err := json.Unmarshal([]byte(docsJSON), &docs); err == nil {
				c.DocsAffected = docs
			}
		}
		m.Changesets = append(m.Changesets, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("evolve.ReadManifest: 遍历 changesets 失败: %w", err)
	}

	mrows, err := conn.QueryContext(ctx,
		`SELECT seq, ts, COALESCE(kb_version,''), modularity_q, singleton_ratio,
		        edge_node_ratio, node_count, edge_count, cumulative_ratio
		   FROM metrics ORDER BY seq`)
	if err != nil {
		return nil, fmt.Errorf("evolve.ReadManifest: 读 metrics 失败: %w", err)
	}
	defer mrows.Close()
	for mrows.Next() {
		var r MetricRow
		if err := mrows.Scan(&r.Seq, &r.Ts, &r.KBVersion, &r.ModularityQ,
			&r.SingletonRatio, &r.EdgeNodeRatio, &r.NodeCount, &r.EdgeCount,
			&r.CumulativeRatio); err != nil {
			return nil, fmt.Errorf("evolve.ReadManifest: 扫描 metrics 失败: %w", err)
		}
		m.Metrics = append(m.Metrics, r)
	}
	if err := mrows.Err(); err != nil {
		return nil, fmt.Errorf("evolve.ReadManifest: 遍历 metrics 失败: %w", err)
	}
	return m, nil
}
