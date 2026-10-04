// Package service 提供领域知识层（Cairn）的服务层实现，
// 负责查询管线的编排、响应构建、域覆盖追踪以及 MCP 协议适配。
//
// 本文件定义服务层中跨文件共享的核心数据结构，包括查询上下文、服务配置、
// 以及外部依赖（同义词扩展、分词器）的接口抽象。
package service

import (
	"time"

	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

// ——————————————————————————————————————————————————————————————————————————————
// SearchContext — 查询管线执行上下文
// ——————————————————————————————————————————————————————————————————————————————

// SearchContext 承载一次查询在整个检索管线中流转的全部状态信息。
// 从原始查询改写开始，经过 FTS5 全文搜索、BFS 图遍历，最终到打分排序，
// 每一步的中间结果都保存在该上下文中，供后续步骤和响应构建使用。
//
// SearchContext carries the full state of a query as it flows through the
// entire retrieval pipeline — from query rewriting, through FTS5 full-text
// search and BFS graph traversal, to final scoring and ranking.
type SearchContext struct {
	// OriginalQuery 是用户输入的原始查询字符串。
	// OriginalQuery is the raw query string entered by the user.
	OriginalQuery string

	// RewrittenQuery is rewritten user text, before safe MATCH compilation.
	RewrittenQuery  string
	MatchExpression string
	QuerySyntax     dktypes.QuerySyntax
	TextProfile     storage.FTSTextProfile

	// FTS5Hits 是 FTS5 全文搜索返回的命中列表，按 BM25 相关性排序。
	// FTS5Hits is the list of hits returned by FTS5 full-text search,
	// ordered by BM25 relevance.
	FTS5Hits []*storage.FTS5Hit

	// TraversalResult 是 BFS 图遍历的结果，包含从 FTS5 命中节点出发
	// 按配置深度遍历到的所有节点与边。
	// TraversalResult is the result of BFS graph traversal, containing all
	// nodes and edges reached from the FTS5 hit nodes up to the configured depth.
	TraversalResult *storage.TraversalResult

	// ScoredResults 是经过综合打分和排序后的节点列表。
	// ScoredResults is the list of nodes after combined scoring and ranking.
	ScoredResults []*storage.ScoredNode

	// Config 是当前查询使用的服务层配置。
	// Config is the service-layer configuration used for this query.
	Config *ServiceConfig

	// StartTime 记录查询开始时间，用于计算总耗时。
	// StartTime records the query start time for total duration calculation.
	StartTime time.Time
}

// ——————————————————————————————————————————————————————————————————————————————
// ServiceConfig — 服务层配置
// ——————————————————————————————————————————————————————————————————————————————

// ServiceConfig 保存服务层所有可配置参数，控制查询行为、检索算法权重、
// 图遍历限制及结果集大小。
//
// ServiceConfig holds all configurable parameters for the service layer,
// controlling query behavior, retrieval algorithm weights, graph traversal
// limits, and result set sizes.
type ServiceConfig struct {
	// TextProfile is the paired index/query profile. Empty means literal.
	// han-v1 is only for explicitly prepared static experimental index copies.
	TextProfile storage.FTSTextProfile
	// DefaultDepth 是默认查询深度（summary / entity / neighborhood / subgraph）。
	// DefaultDepth is the default query depth.
	DefaultDepth dktypes.Depth

	// DefaultMaxTokens 是返回结果的最大 token 数量。
	// DefaultMaxTokens is the maximum number of tokens in the response.
	DefaultMaxTokens int

	// DefaultMinConfidence 是节点置信度的默认最低阈值。
	// DefaultMinConfidence is the default minimum confidence threshold for nodes.
	DefaultMinConfidence float64

	// BM25Weight 是 BM25 文本检索得分在综合评分中的权重。
	// BM25Weight is the weight of the BM25 text retrieval score in the combined score.
	BM25Weight float64

	// GraphWeight 是图结构得分在综合评分中的权重。
	// GraphWeight is the weight of the graph structure score in the combined score.
	GraphWeight float64

	// MaxBFSDepth 是 BFS 图遍历的最大深度限制。
	// MaxBFSDepth is the maximum depth limit for BFS graph traversal.
	MaxBFSDepth int

	// MaxSearchResults 是 FTS5 全文搜索返回的最大结果数。
	// MaxSearchResults is the maximum number of results returned by FTS5 search.
	MaxSearchResults int

	// MaxTraversalNodes 是图遍历过程中访问的最大节点数。
	// MaxTraversalNodes is the maximum number of nodes visited during graph traversal.
	MaxTraversalNodes int
}

// ——————————————————————————————————————————————————————————————————————————————
// NeighborhoodResult — 邻域查询结果
// ——————————————————————————————————————————————————————————————————————————————

// NeighborhoodResult 表示 depth=neighborhood 时单个实体的邻域查询结果，
// 在 EntityResult 基础上增加了直接邻居节点的概要信息。
//
// NeighborhoodResult represents the neighborhood query result for a single
// entity when depth=neighborhood, extending EntityResult with summary
// information about immediate neighbor nodes.
type NeighborhoodResult struct {
	// EntityResult 包含实体本身的基础信息。
	// EntityResult contains the basic information of the entity itself.
	dktypes.EntityResult

	// Neighbors 是直接邻居节点的摘要信息列表。
	// Neighbors is a list of summary information for immediate neighbor nodes.
	Neighbors []NeighborInfo `json:"neighbors"`
}

// NeighborInfo 表示邻居节点的摘要信息及到达该邻居的关系路径。
// NeighborInfo represents summary information about a neighbor node
// and the relationship path to reach it.
type NeighborInfo struct {
	// NodeID 是邻居节点的唯一标识符。
	// NodeID is the unique identifier of the neighbor node.
	NodeID string `json:"node_id"`
	// Name 是邻居节点的可读名称。
	// Name is the human-readable name of the neighbor node.
	Name string `json:"name"`
	// Summary 是邻居节点的简短摘要。
	// Summary is a short summary of the neighbor node.
	Summary string `json:"summary"`
	// Type 表示邻居节点类型（Entity 或 Concept）。
	// Type indicates the neighbor node type (Entity or Concept).
	Type dktypes.Label `json:"type"`
	// RelationKind 表示当前实体与该邻居之间的语义关系类型。
	// RelationKind indicates the semantic relationship type between the
	// current entity and this neighbor.
	RelationKind dktypes.RelationKind `json:"relation_kind"`
	// RelationDesc 是对该关系的描述。
	// RelationDesc is a description of the relationship.
	RelationDesc string `json:"relation_desc,omitempty"`
	// HopCount 是从中心节点到该邻居的跳数。
	// HopCount is the number of hops from the center node to this neighbor.
	HopCount int `json:"hop_count"`
}
