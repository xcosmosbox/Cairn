// Package community 是全量重整（rebalance）的 Louvain 社区检测封装（纯 CPU、
// 纯 Go、零 embedding）：把 gonum 的 Louvain 模块度聚类包装成「nodeID → 社区 ID +
// 划分 Q」的简明接口，产出仅作 LLM 语义微调的先验骨架（不命名、不分层）。
//
// 铁律 R-louvain-readonly：本包只读输入邻接、只产建议社区划分，绝不改图——
// 改图只由 LLM patch + 代码应用完成。输入图以字符串 nodeID 标识（编排器传
// node uuid），内部映射为 gonum 的 int64 节点（uuid 绝不进 LLM prompt，alias
// 翻译在编排器侧完成，R1）。
//
// 确定性：Louvain 内部有随机 shuffle，本封装固定随机种子（同输入必同输出），
// 保证重整可复现、测试可断言。
//
// Package community wraps gonum's Louvain modularity clustering as a pure
// read-only community detector (R-louvain-readonly) with a fixed RNG seed for
// determinism. No CGO, no embeddings.
package community

import (
	"sort"

	"golang.org/x/exp/rand"
	"gonum.org/v1/gonum/graph/community"
	"gonum.org/v1/gonum/graph/simple"
)

// Edge 是语义子图的一条无向边（端点为外部 nodeID；Weight ≤0 按 1 处理）。
// Edge is one undirected edge of the semantic subgraph (Weight ≤ 0 means 1).
type Edge struct {
	SourceID string
	TargetID string
	Weight   float64
}

// fixedSeed 是 Louvain 的固定随机种子（确定性：同输入同输出）。
// fixedSeed fixes Louvain's RNG seed so runs are reproducible.
const fixedSeed = 42

// louvainResolution 是 Louvain 的模块度分辨率 γ（1.0 = 经典 Newman 模块度）。
// louvainResolution is the modularity resolution γ (1.0 = classic Newman).
const louvainResolution = 1.0

// Result 是一次社区检测的产出。
// Result is one community detection output.
type Result struct {
	// CommunityOf 是 nodeID → 社区 ID（从 0 起连续编号；孤立节点自成社区）。
	// CommunityOf maps nodeID → community ID (0-based contiguous; isolated
	// nodes form singleton communities).
	CommunityOf map[string]int
	// Communities 是社区 ID → 成员 nodeID 列表（成员按字典序，确定性）。
	// Communities maps community ID → sorted member nodeIDs.
	Communities map[int][]string
	// Q 是该划分的模块度（无边图为 0）。
	// Q is the modularity of this partition (0 for edgeless graphs).
	Q float64
}

// Detect 在给定节点集 + 无向语义边上跑 Louvain 社区检测（只读，绝不改图）。
// nodeIDs 中无边相连的节点也纳入结果（各自成单例社区）；edges 中端点不在
// nodeIDs 的边被忽略（防御脏输入）；自环忽略。
//
// Detect runs Louvain over the given nodes and undirected semantic edges.
// Read-only: it never mutates any external state.
func Detect(nodeIDs []string, edges []Edge) *Result {
	res := &Result{
		CommunityOf: make(map[string]int, len(nodeIDs)),
		Communities: make(map[int][]string),
	}
	// 去重 + 稳定序（确定性）。
	uniq := make([]string, 0, len(nodeIDs))
	seen := make(map[string]bool, len(nodeIDs))
	for _, id := range nodeIDs {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		uniq = append(uniq, id)
	}
	sort.Strings(uniq)
	if len(uniq) == 0 {
		return res
	}

	// nodeID ↔ gonum int64 节点映射（内部整数 ID，uuid 不出本包）。
	indexOf := make(map[string]int64, len(uniq))
	idOf := make([]string, len(uniq))
	g := simple.NewWeightedUndirectedGraph(0, 0)
	for i, id := range uniq {
		indexOf[id] = int64(i)
		idOf[i] = id
		g.AddNode(simple.Node(i))
	}
	hasEdges := false
	for _, e := range edges {
		src, okS := indexOf[e.SourceID]
		tgt, okT := indexOf[e.TargetID]
		if !okS || !okT || src == tgt {
			continue // 脏端点 / 自环忽略
		}
		w := e.Weight
		if w <= 0 {
			w = 1
		}
		g.SetWeightedEdge(g.NewWeightedEdge(simple.Node(src), simple.Node(tgt), w))
		hasEdges = true
	}
	if !hasEdges {
		// 无边图：Louvain 无意义，每节点单例、Q=0（哨兵语义一致）。
		for i, id := range idOf {
			res.CommunityOf[id] = i
			res.Communities[i] = []string{id}
		}
		return res
	}

	// Louvain 模块度聚类（固定种子 → 确定性）。
	// Communities() 递归展开到原图节点（Structure() 是层级中间结构，不可直接用）。
	reduced := community.Modularize(g, louvainResolution, rand.NewSource(uint64(fixedSeed)))
	structure := reduced.Communities()
	for cid, members := range structure {
		ids := make([]string, 0, len(members))
		for _, n := range members {
			id := idOf[n.ID()]
			res.CommunityOf[id] = cid
			ids = append(ids, id)
		}
		sort.Strings(ids)
		res.Communities[cid] = ids
	}
	// 防御：Louvain 未覆盖的节点（不应发生）补为单例社区。
	next := len(structure)
	for _, id := range uniq {
		if _, ok := res.CommunityOf[id]; !ok {
			res.CommunityOf[id] = next
			res.Communities[next] = []string{id}
			next++
		}
	}
	res.Q = community.Q(g, structure, louvainResolution)
	return res
}
