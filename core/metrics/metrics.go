// Package metrics 实现全量重整（rebalance）的复杂度哨兵：三个零 LLM、零 Louvain
// 迭代的标量指标，多指标并集触发（无盲区），判据永远按「两端 label 均为
// Entity/Concept」过滤语义边（坑点 2：composes 既是层级边 kind 又是语义边 kind）。
//
// 三指标（R-sentinel-cheap：全部 O(E) 一次遍历）：
//   - 现状模块度 Q：把每个 node 的现有 subdomain 当「社区」，在语义边子图上算
//     一次模块度 Q = Σ_c [L_c/m − (d_c/2m)²]（无向化语义边，m=语义边数）。
//     Q 低 → 现有归属与真实连接脱节；
//   - 单例率：仅含 1 个 entity/concept 的 subdomain 数 / subdomain 总数。
//     高 → 碎片化；
//   - 边/节点比：语义边数 / (entity+concept 节点数)。高 → 树爆炸、冗余连边。
//
// 本包只依赖 core/storage 与 core/dktypes（§4.2：纯算法件，可独立单测）；
// 绝不 import incremental（避免环），绝不引入 embedding（R-no-embedding）。
//
// Package metrics implements the rebalance complexity sentinel: three cheap
// LLM-free signals (current modularity, singleton ratio, edge/node ratio) over
// the semantic subgraph (both endpoints Entity/Concept — the composes dual
// identity is filtered by endpoint labels, never by kind alone).
package metrics

import (
	"context"
	"fmt"

	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

// SubdomainKey 标识一个 subdomain（domain slug + subdomain slug）。
// SubdomainKey identifies one subdomain by its ownership slugs.
type SubdomainKey struct {
	Domain    string
	Subdomain string
}

// Edge 是语义边子图中的一条边（两端均为 entity/concept 节点）。
// Edge is one edge of the semantic subgraph (both endpoints entity/concept).
type Edge struct {
	SourceID   string
	TargetID   string
	Kind       string
	Confidence float64
}

// SemanticGraph 是哨兵与 Louvain 的共享输入：entity/concept 节点集合及其归属
// + 两端均 entity/concept 的语义边（层级边已排除）。
// SemanticGraph is the shared input of the sentinel and Louvain: entity/concept
// nodes with ownership, plus semantic edges (hierarchy edges excluded).
type SemanticGraph struct {
	// Nodes 是 entity/concept 节点 uuid → 其归属 subdomain。
	// Nodes maps entity/concept uuid → owning subdomain.
	Nodes map[string]SubdomainKey
	// Edges 是语义边（两端均在 Nodes 中）。
	// Edges holds semantic edges (both endpoints present in Nodes).
	Edges []Edge
}

// Snapshot 是一次哨兵计算的输出（三指标 + 计数原料，可观测）。
// Snapshot is one sentinel computation result (three signals + raw counts).
type Snapshot struct {
	ModularityQ    float64 // 现状模块度（现有 subdomain 当社区，一次遍历）
	SingletonRatio float64 // 单例子域占比
	EdgeNodeRatio  float64 // 语义边 / entity+concept 节点
	NodeCount      int     // entity/concept 节点数
	EdgeCount      int     // 语义边数（无向化后计数，含多重边）
	SubdomainCount int     // 含 entity/concept 的 subdomain 数
	SingletonCount int     // 仅含 1 个 entity/concept 的 subdomain 数
}

// Thresholds 是触发阈值配置（全部可配；零值取默认）。
// Thresholds configures trigger thresholds (all configurable; zero = default).
type Thresholds struct {
	QFloor         float64 // 现状 Q 下限：Q < QFloor → 触发（默认 0.3）
	SCeil          float64 // 单例率上限：ratio > SCeil → 触发（默认 0.4）
	ECeil          float64 // 边/节点比上限：ratio > ECeil → 触发（默认 3.0，待实测调整）
	CumulativeCeil float64 // 累计改动占比上限：> CumulativeCeil → 触发（默认 0.20）
	// CumulativeMinDocs 是「累计改动」保底触发的绝对量下限：占比越界之外，还须
	// 累计改动文档数达到该值才触发（默认 5）。
	//
	// 为什么需要它：占比的分母是候选文档总数，比例型阈值在小基数下会失真——
	// 仓库仅 5 篇候选文档时，改 1 篇即 0.20、改 2 篇即 0.40，于是每次小改动都会
	// 顶穿保底阈值、触发全图重整（LLM + 大量文档重写），PR 因此周期性剧烈抖动。
	// 加绝对量下限后：大库仍按占比触发（行为不变），小库不再被单篇改动误触发。
	CumulativeMinDocs int
}

// DefaultThresholds 返回设计定稿的默认阈值。
// DefaultThresholds returns the spec defaults.
func DefaultThresholds() Thresholds {
	return Thresholds{QFloor: 0.3, SCeil: 0.4, ECeil: 3.0, CumulativeCeil: 0.20, CumulativeMinDocs: 5}
}

// withDefaults 把零值字段填为默认值（允许部分配置）。
// withDefaults fills zero fields with defaults (partial config allowed).
func (t Thresholds) withDefaults() Thresholds {
	d := DefaultThresholds()
	if t.QFloor == 0 {
		t.QFloor = d.QFloor
	}
	if t.SCeil == 0 {
		t.SCeil = d.SCeil
	}
	if t.ECeil == 0 {
		t.ECeil = d.ECeil
	}
	if t.CumulativeCeil == 0 {
		t.CumulativeCeil = d.CumulativeCeil
	}
	if t.CumulativeMinDocs == 0 {
		t.CumulativeMinDocs = d.CumulativeMinDocs
	}
	return t
}

// LoadGraph 从存储层装配语义子图（R-1 快照；哨兵与 Louvain 共用输入）：
// 全部 entity/concept 节点（含归属）+ 两端均为 entity/concept 的 6 枚举语义边。
// 层级 composes 边（domain→subdomain、subdomain→node）天然被「两端 label」判据
// 排除——判据永远不是 kind==composes（坑点 2）。
//
// LoadGraph assembles the semantic subgraph from storage: all entity/concept
// nodes plus 6-kind semantic edges whose BOTH endpoints are entity/concept.
func LoadGraph(ctx context.Context, nodes *storage.NodeRepo, edges *storage.EdgeRepo) (*SemanticGraph, error) {
	all, err := nodes.ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("metrics: 读取全部节点失败: %w", err)
	}
	g := &SemanticGraph{Nodes: make(map[string]SubdomainKey)}
	for _, n := range all {
		if n == nil || (n.Label != dktypes.LabelEntity && n.Label != dktypes.LabelConcept) {
			continue
		}
		g.Nodes[n.ID] = SubdomainKey{Domain: n.Domain, Subdomain: n.Subdomain}
	}
	seen := make(map[string]bool)
	for uuid := range g.Nodes {
		out, err := edges.GetOutgoing(ctx, uuid, 0)
		if err != nil {
			return nil, fmt.Errorf("metrics: 读取节点出边失败: %w", err)
		}
		for _, e := range out {
			if !isSemanticKind(e.Kind) {
				continue // provides 等非语义 kind
			}
			if _, ok := g.Nodes[e.TargetID]; !ok {
				continue // 目标不是 entity/concept（层级边或悬空）→ 排除
			}
			if e.TargetID == uuid {
				continue // 自环不参与模块度/边计数
			}
			key := e.SourceID + "\x00" + e.TargetID + "\x00" + string(e.Kind)
			if seen[key] {
				continue // 防御重复行（每源节点只遍历一次，正常不会重复）
			}
			seen[key] = true
			g.Edges = append(g.Edges, Edge{
				SourceID: e.SourceID, TargetID: e.TargetID,
				Kind: string(e.Kind), Confidence: e.Confidence,
			})
		}
	}
	return g, nil
}

// isSemanticKind 报告 kind 是否为 6 枚举语义 kind 之一（不含 provides）。
// isSemanticKind reports whether kind is one of the 6 semantic kinds.
func isSemanticKind(kind dktypes.RelationKind) bool {
	switch kind {
	case dktypes.KindTriggers, dktypes.KindDependsOn, dktypes.KindReferences,
		dktypes.KindGeneralizes, dktypes.KindComposes, dktypes.KindContradicts:
		return true
	default:
		return false
	}
}

// Compute 计算三指标（纯函数，O(N+E)，空图安全）。
// Compute computes the three signals (pure, O(N+E), empty-graph safe).
func Compute(g *SemanticGraph) Snapshot {
	var s Snapshot
	if g == nil || len(g.Nodes) == 0 {
		return s
	}
	s.NodeCount = len(g.Nodes)
	s.EdgeCount = len(g.Edges)

	// —— 子域计数与单例率 ——
	sizeBySub := make(map[SubdomainKey]int)
	for _, key := range g.Nodes {
		sizeBySub[key]++
	}
	s.SubdomainCount = len(sizeBySub)
	for _, size := range sizeBySub {
		if size == 1 {
			s.SingletonCount++
		}
	}
	if s.SubdomainCount > 0 {
		s.SingletonRatio = float64(s.SingletonCount) / float64(s.SubdomainCount)
	}
	if s.NodeCount > 0 {
		s.EdgeNodeRatio = float64(s.EdgeCount) / float64(s.NodeCount)
	}
	s.ModularityQ = modularity(g)
	return s
}

// modularity 以「现有 subdomain 为社区」计算无向模块度（一次 O(E) 遍历，不迭代）：
//
//	Q = Σ_c [ L_c/m − (d_c/2m)² ]
//
// 其中 m=语义边数（无向化），L_c=社区 c 的内部边数，d_c=社区 c 的度数和。
// 每条存储边计为一条无向边（多重边按 multiplicity 计入度与 m——哨兵只需单调可比）。
// 无边图返回 0（模块度无定义，取保守值 0 → Q<QFloor 倾向触发，交由其它指标并集裁决）。
//
// modularity computes undirected modularity with current subdomains as
// communities in a single O(E) pass (no Louvain iteration, R-sentinel-cheap).
func modularity(g *SemanticGraph) float64 {
	m := len(g.Edges)
	if m == 0 {
		return 0
	}
	// degree 是每个节点的（无向）度数；internal 是每个社区的内部边数。
	degree := make(map[string]int, len(g.Nodes))
	internal := make(map[SubdomainKey]int)
	for _, e := range g.Edges {
		degree[e.SourceID]++
		degree[e.TargetID]++
		srcKey, okS := g.Nodes[e.SourceID]
		tgtKey, okT := g.Nodes[e.TargetID]
		if okS && okT && srcKey == tgtKey {
			internal[srcKey]++
		}
	}
	// 每个社区的度数和（孤立节点度为 0，不影响结果）。
	degreeByComm := make(map[SubdomainKey]int)
	for uuid, key := range g.Nodes {
		degreeByComm[key] += degree[uuid]
	}
	mf := float64(m)
	var q float64
	for comm := range degreeByComm {
		lc := float64(internal[comm])
		dc := float64(degreeByComm[comm])
		q += lc/mf - (dc/(2*mf))*(dc/(2*mf))
	}
	return q
}

// Breach 判定是否越阈值（多指标并集，无盲区），返回全部越界原因（可观测）。
// cumulativeChangeRatio / cumulativeDocsChanged 是 kg_manifest 的累计改动占比与
// 累计改动文档数（无记录传 0）。零原因 = 无需重整。
// 本函数零 LLM、零 Louvain 迭代（R-sentinel-cheap）。
//
// Breach reports which thresholds are violated (union semantics — any breach
// triggers), returning human-readable reasons for observability.
func Breach(s Snapshot, cumulativeChangeRatio float64, cumulativeDocsChanged int, th Thresholds) []string {
	th = th.withDefaults()
	var reasons []string
	if s.ModularityQ < th.QFloor {
		reasons = append(reasons, fmt.Sprintf("现状模块度 Q=%.3f < 下限 %.3f（现有归属与真实连接脱节）", s.ModularityQ, th.QFloor))
	}
	if s.SingletonRatio > th.SCeil {
		reasons = append(reasons, fmt.Sprintf("单例子域占比 %.3f > 上限 %.3f（碎片化）", s.SingletonRatio, th.SCeil))
	}
	if s.EdgeNodeRatio > th.ECeil {
		reasons = append(reasons, fmt.Sprintf("边/节点比 %.3f > 上限 %.3f（连边爆炸）", s.EdgeNodeRatio, th.ECeil))
	}
	// 保底触发需占比与绝对量双双达标：小文档集下单篇改动即可顶穿占比阈值，
	// 若仅看占比会让每次小改动都触发全图重整（PR 周期性剧烈抖动）。
	if cumulativeChangeRatio > th.CumulativeCeil && cumulativeDocsChanged >= th.CumulativeMinDocs {
		reasons = append(reasons, fmt.Sprintf("累计改动占比 %.3f > 上限 %.3f（累计改动 %d 篇 ≥ 下限 %d，结构漂移保底触发）",
			cumulativeChangeRatio, th.CumulativeCeil, cumulativeDocsChanged, th.CumulativeMinDocs))
	}
	return reasons
}
