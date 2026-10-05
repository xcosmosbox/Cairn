// Package storage 提供领域知识层的持久化存储层实现，包括数据库连接管理、
// 模式定义、数据迁移和 CRUD 仓储。
//
// 本文件包含混合排序引擎，对 BFS 遍历结果按 BM25 + 图深度的加权公式进行排序。
//
// Package storage implements the persistence layer for the Cairn,
// including database connection management, schema definition, data migration,
// and CRUD repositories.
//
// This file contains the hybrid scoring engine that ranks BFS traversal results
// using a weighted formula combining BM25 relevance and graph depth.
package storage

import (
	"sort"

	"github.com/xcosmosbox/cairn/core/dktypes"
)

// ScoredNode 是加权排序后的节点，包含各维度分数。
// ScoredNode is a weighted and ranked node containing per-dimension scores.
type ScoredNode struct {
	// Node 是排序后的知识图谱节点。
	// Node is the ranked knowledge graph node.
	Node *dktypes.Node
	// FinalScore 是最终的加权综合分数。
	// FinalScore is the final weighted composite score.
	FinalScore float64
	// BM25Score 是归一化的 [0,1] 文本相关性分数，越大越相关。
	// BM25Score is normalized [0,1] text relevance, higher is better;
	// it is not SQLite's raw negative BM25 rank.
	BM25Score float64
	// GraphScore 是基于图深度的结构相关性分数。
	// GraphScore is the structural relevance score based on graph depth.
	GraphScore float64
	// Depth 是该节点从入口节点出发的最短路径深度。
	// Depth is the shortest path depth of this node from the entry nodes.
	Depth int
}

// ScoreAndRank 对遍历结果进行 BM25 + 图深度的混合排序。
//
// 排序公式: finalScore = bm25Weight * bm25Score + graphWeight * (1.0 / max(depth, 1))
//
// 设计理由:
//   - BM25 权重 0.6: 关键词匹配是最主要的信号
//   - 图深度权重 0.4: 越靠近入口节点的实体越相关
//   - 未被 FTS5 直接命中的节点 bm25Score = 0
//   - 权重可在生产观察后通过配置调整（BM25Weight / GraphWeight）
//
// 按 finalScore 降序排列，截断至 topK。
//
// ScoreAndRank performs hybrid scoring on traversal results using BM25 + graph depth.
//
// Scoring formula: finalScore = bm25Weight * bm25Score + graphWeight * (1.0 / max(depth, 1))
//
// Design rationale:
//   - BM25 weight 0.6: keyword matching is the primary signal
//   - Graph depth weight 0.4: entities closer to entry nodes are more relevant
//   - Nodes not directly hit by FTS5 have bm25Score = 0
//   - Weights can be adjusted via configuration after production observation
//     (BM25Weight / GraphWeight)
//
// Results are sorted by finalScore descending and truncated to topK.
// Ties break by node ID ascending — the input Nodes map has randomized
// iteration order, so without a tie-break equal scores would leak that
// nondeterminism to callers (retrieval evaluation needs reproducible ranks).
func ScoreAndRank(result *TraversalResult, topK int, bm25Weight, graphWeight float64) []*ScoredNode {
	if bm25Weight == 0 {
		bm25Weight = 0.6
	}
	if graphWeight == 0 {
		graphWeight = 0.4
	}

	var scored []*ScoredNode
	for nodeID, node := range result.Nodes {
		bm25 := result.FTS5Hits[nodeID] // 未被直接命中则为 0.0 / 0.0 if not directly hit
		depth := result.Depths[nodeID]
		if depth < 1 {
			depth = 1
		}
		graph := 1.0 / float64(depth)
		final := bm25Weight*bm25 + graphWeight*graph

		scored = append(scored, &ScoredNode{
			Node:       node,
			FinalScore: final,
			BM25Score:  bm25,
			GraphScore: graph,
			Depth:      depth,
		})
	}

	// 按 finalScore 降序排列；同分按 Node.ID 升序兜底。
	// result.Nodes 是 map（迭代序随机），无 tie-break 时同分元素顺序不确定，
	// 会破坏调用方的结果可复现性（W-C 评测门禁依赖确定性排名）。
	// Sort by finalScore descending; ties break by Node.ID ascending for
	// determinism (the Nodes map iteration order is randomized).
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].FinalScore != scored[j].FinalScore {
			return scored[i].FinalScore > scored[j].FinalScore
		}
		return scored[i].Node.ID < scored[j].Node.ID
	})

	// 截断至 topK / Truncate to topK
	if topK > 0 && len(scored) > topK {
		scored = scored[:topK]
	}
	return scored
}
