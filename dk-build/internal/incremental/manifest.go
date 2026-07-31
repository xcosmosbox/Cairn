// Package incremental 的本文件实现「累计改动量」记录：每轮增量结束把
// 「本轮改动文档数 / 累计改动文档占比」写入 kg_manifest（KV 表，value 为 JSON），
// 供第三块（全量重整 dk-rebalance）的保底触发读取。本块只写不消费。
//
// This file records per-run and cumulative change statistics into kg_manifest
// for the rebalance trigger to consume later (write-only in this block).
package incremental

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// manifestStatsKey 是增量统计在 kg_manifest 中的键。
// manifestStatsKey is the kg_manifest key for incremental statistics.
const manifestStatsKey = "incremental.stats"

// incrementalStats 是写入 kg_manifest 的统计值（JSON）。
// incrementalStats is the JSON value written to kg_manifest.
type incrementalStats struct {
	// Runs 是累计增量运行轮数。
	// Runs counts incremental runs so far.
	Runs int `json:"runs"`
	// LastRunAt 是本轮运行时间（RFC3339）。
	// LastRunAt is this run's timestamp (RFC3339).
	LastRunAt string `json:"last_run_at"`
	// DocsChangedThisRun 是本轮改动（细判有实质变更）的文档数。
	// DocsChangedThisRun counts docs with substantive changes this run.
	DocsChangedThisRun int `json:"docs_changed_this_run"`
	// DocsTotal 是回写产物文档总数（有 sidecar 的文档数，分母）。
	// DocsTotal is the total number of written-back docs (the denominator).
	DocsTotal int `json:"docs_total"`
	// CumulativeDocsChanged 是历轮改动文档数累计（同一文档多轮改动重复计数——
	// 作为「改动热度」信号供重整触发参考，不去重）。
	// CumulativeDocsChanged accumulates per-run changed-doc counts (a doc changed
	// in multiple runs counts multiple times — a change-heat signal).
	CumulativeDocsChanged int `json:"cumulative_docs_changed"`
	// CumulativeChangeRatio 是累计改动占比（CumulativeDocsChanged / DocsTotal，封顶 1.0）。
	// CumulativeChangeRatio is CumulativeDocsChanged / DocsTotal (capped at 1.0).
	CumulativeChangeRatio float64 `json:"cumulative_change_ratio"`
}

// recordManifestStats 把本轮统计写入 kg_manifest（读出旧值累加后覆盖写）。
// docsChanged 是本轮细判有实质变更的文档数；docsTotal 是回写产物文档总数。
// 写失败只告警不失败（统计是可观测产物，不阻断流水线）。
//
// recordManifestStats accumulates and writes this run's stats to kg_manifest.
// A write failure degrades to a warning (stats are observability, never fatal).
func recordManifestStats(ctx context.Context, st *stores, docsChanged, docsTotal int, rs *runState) {
	stats := &incrementalStats{
		Runs:               1,
		LastRunAt:          time.Now().UTC().Format(time.RFC3339),
		DocsChangedThisRun: docsChanged,
		DocsTotal:          docsTotal,
	}
	// 读旧值累加（首轮无旧值）。
	if old, err := st.manifest.Get(ctx, manifestStatsKey); err == nil && old != "" {
		var prev incrementalStats
		if json.Unmarshal([]byte(old), &prev) == nil {
			stats.Runs = prev.Runs + 1
			stats.CumulativeDocsChanged = prev.CumulativeDocsChanged
		}
	}
	stats.CumulativeDocsChanged += docsChanged
	if docsTotal > 0 {
		stats.CumulativeChangeRatio = float64(stats.CumulativeDocsChanged) / float64(docsTotal)
		if stats.CumulativeChangeRatio > 1.0 {
			stats.CumulativeChangeRatio = 1.0
		}
	}
	data, err := json.Marshal(stats)
	if err != nil {
		rs.warnf(fmt.Sprintf("manifest 统计序列化失败: %v", err))
		return
	}
	if err := st.manifest.Set(ctx, manifestStatsKey, string(data)); err != nil {
		rs.warnf(fmt.Sprintf("manifest 统计写入失败: %v", err))
	}
}
