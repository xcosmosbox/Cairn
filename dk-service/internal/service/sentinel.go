// Package service 的本文件实现 W-B「复杂度哨兵持续可观测」：把第三块一次性
// dk-rebalance --check 的哨兵三指标（现状模块度 Q / 单例率 / 边节点比）变成
// 查询端可随时采样的只读能力 + 可选时序留存（sentinel.history）。
//
// 铁律遵守：
//   - 纯 CPU、零 LLM：只走 metrics.LoadGraph → metrics.Compute → metrics.Breach
//     （全部纯函数、O(N+E)、零 Louvain 迭代），绝不触碰 RebalanceOrchestrator。
//   - 只读 KG 结构：不 Insert/Update/Delete nodes/edges/node_sources；
//     record=true 时仅写 kg_manifest 的独立观测键 sentinel.history，
//     不动 incremental.stats / rebalance.last 的既有语义与字节。
//   - 累计改动占比直接读第二块写的 incremental.stats（cumulative_change_ratio），
//     不重算；无记录按 0。
//
// This file implements W-B continuous sentinel observability: on-demand
// LLM-free sampling of the three complexity signals, plus optional time-series
// retention in kg_manifest under the dedicated key sentinel.history.
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/xcosmosbox/domain-knowledge-layer/core/metrics"
	"github.com/xcosmosbox/domain-knowledge-layer/core/storage"
)

// sentinelHistoryKey 是哨兵时序在 kg_manifest 中的独立观测键。
// 与 incremental.stats（第二块写）/ rebalance.last（第三块写）互不干扰。
//
// sentinelHistoryKey is the dedicated kg_manifest key for sentinel history.
const sentinelHistoryKey = "sentinel.history"

// sentinelHistoryCap 是时序环形容量（超出淘汰最旧）。
// sentinelHistoryCap is the ring capacity of retained snapshots.
const sentinelHistoryCap = 50

// manifestIncrementalStatsKey 是第二块增量统计的键（本文件只读不写）。
// manifestIncrementalStatsKey is the incremental stats key (read-only here).
const manifestIncrementalStatsKey = "incremental.stats"

// SentinelSnapshot 是一次哨兵采样结果（三指标 + 累计改动占比 + 越界原因）。
// SentinelSnapshot is one sentinel sampling result.
type SentinelSnapshot struct {
	Timestamp       time.Time `json:"ts"`
	ModularityQ     float64   `json:"q"`
	SingletonRatio  float64   `json:"singleton_ratio"`
	EdgeNodeRatio   float64   `json:"edge_node_ratio"`
	CumulativeRatio float64   `json:"cumulative_ratio"`
	BreachReasons   []string  `json:"breach_reasons,omitempty"`
}

// Sentinel 实现哨兵采样元能力（接口方法见 knowledge_service.go）。
// record=false 纯只读；record=true 把本次快照追加进 sentinel.history 时序。
// 采样值在任何情况下都返回；时序写失败显式报错（R-fail-visible）。
//
// Sentinel samples the three complexity signals over the semantic subgraph.
// With record=true the snapshot is appended to the sentinel.history ring.
func (s *knowledgeService) Sentinel(ctx context.Context, record bool) (*SentinelSnapshot, error) {
	g, err := metrics.LoadGraph(ctx, s.nodeRepo, s.edgeRepo)
	if err != nil {
		return nil, fmt.Errorf("sentinel loadgraph: %w", err)
	}
	snap := metrics.Compute(g)
	manifest := storage.NewManifestRepo(s.db)
	cum, cumDocs := readCumulativeRatio(ctx, manifest)
	out := &SentinelSnapshot{
		Timestamp:       time.Now().UTC(),
		ModularityQ:     snap.ModularityQ,
		SingletonRatio:  snap.SingletonRatio,
		EdgeNodeRatio:   snap.EdgeNodeRatio,
		CumulativeRatio: cum,
		BreachReasons:   metrics.Breach(snap, cum, cumDocs, metrics.Thresholds{}), // 零值 → 默认阈值
	}
	if record {
		if err := appendSentinelHistory(ctx, manifest, out, sentinelHistoryCap); err != nil {
			return out, fmt.Errorf("sentinel record: %w", err)
		}
	}
	return out, nil
}

// readCumulativeRatio 读第二块写入的 incremental.stats 的累计改动占比与累计改动
// 文档数（JSON 字段 cumulative_change_ratio / cumulative_docs_changed，snake_case
// 以第二块实际写入结构为准）。缺键/解析失败按 0（无记录语义），绝不动该键字节。
//
// readCumulativeRatio reads cumulative_change_ratio and cumulative_docs_changed
// from incremental.stats; zeros when absent or unparsable.
func readCumulativeRatio(ctx context.Context, m *storage.ManifestRepo) (float64, int) {
	raw, err := m.Get(ctx, manifestIncrementalStatsKey)
	if err != nil || raw == "" {
		return 0, 0
	}
	var st struct {
		CumulativeChangeRatio float64 `json:"cumulative_change_ratio"`
		CumulativeDocsChanged int     `json:"cumulative_docs_changed"`
	}
	if json.Unmarshal([]byte(raw), &st) != nil {
		return 0, 0
	}
	return st.CumulativeChangeRatio, st.CumulativeDocsChanged
}

// appendSentinelHistory 把一次快照追加进 sentinel.history 环形时序（超 cap 淘汰最旧）。
// 只写独立观测键 sentinel.history，不触碰任何既有 manifest 键。
//
// appendSentinelHistory appends one snapshot to the sentinel.history ring,
// evicting the oldest entries beyond cap. Only the dedicated key is written.
func appendSentinelHistory(ctx context.Context, m *storage.ManifestRepo, pt *SentinelSnapshot, cap int) error {
	var hist []SentinelSnapshot
	if raw, err := m.Get(ctx, sentinelHistoryKey); err == nil && raw != "" {
		// 历史损坏不从零丢弃：解析失败按空历史重启（观测数据，可重建）。
		_ = json.Unmarshal([]byte(raw), &hist)
	}
	hist = append(hist, *pt)
	if cap > 0 && len(hist) > cap {
		hist = hist[len(hist)-cap:]
	}
	b, err := json.Marshal(hist)
	if err != nil {
		return fmt.Errorf("appendSentinelHistory marshal: %w", err)
	}
	if err := m.Set(ctx, sentinelHistoryKey, string(b)); err != nil {
		return fmt.Errorf("appendSentinelHistory set: %w", err)
	}
	return nil
}
