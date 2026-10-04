package service

import (
	"context"
	"fmt"
	"log"
	"math"
	"time"

	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
)

// ——————————————————————————————————————————————————————————————————————————————
// SearchPipeline — 检索管线
// ——————————————————————————————————————————————————————————————————————————————

// SearchPipeline 编排完整的检索管线：查询改写 → FTS5 全文搜索 → BFS 图遍历 →
// 综合打分排序。每一步的输出作为下一步的输入，最终将所有中间状态汇总在
// SearchContext 中返回，供 ResponseBuilder 使用。
//
// SearchPipeline orchestrates the complete retrieval pipeline:
// query rewriting → FTS5 full-text search → BFS graph traversal →
// combined scoring and ranking. The output of each step feeds into the next,
// and all intermediate state is aggregated in a SearchContext for use
// by the ResponseBuilder.
type SearchPipeline struct {
	// db 是知识图谱数据库实例，提供 FTS5 搜索、图遍历和节点/边仓库访问。
	// db is the knowledge graph database instance, providing FTS5 search,
	// graph traversal, and node/edge repository access.
	db *storage.DB

	// rewriter 是查询改写器，负责缩写扩展、同义词扩展和分词。
	// rewriter is the query rewriter, responsible for abbreviation expansion,
	// synonym expansion, and tokenization.
	rewriter *QueryRewriter

	// config 是服务层配置，控制检索管线的各阶段参数。
	// config is the service-layer configuration, controlling parameters
	// for each stage of the retrieval pipeline.
	config *ServiceConfig
}

// NewSearchPipeline 创建检索管线实例。
// db 必须是已初始化的知识图谱数据库；rw 是查询改写器；cfg 是服务层配置。
//
// NewSearchPipeline creates a new search pipeline instance.
// db must be an initialized knowledge graph database; rw is the query rewriter;
// cfg is the service-layer configuration.
func NewSearchPipeline(db *storage.DB, rw *QueryRewriter, cfg *ServiceConfig) *SearchPipeline {
	return &SearchPipeline{
		db:       db,
		rewriter: rw,
		config:   cfg,
	}
}

// Execute 执行完整的检索管线并返回包含所有中间状态的 SearchContext。
// 管线步骤：
//  1. 查询改写（Rewrite）
//  2. FTS5 全文搜索
//  3. BFS 图遍历（从 FTS5 命中节点出发）
//  4. 综合打分排序（ScoreAndRank）
//
// 任意步骤失败时返回对应的错误，并附加上下文信息。
//
// Execute runs the complete retrieval pipeline and returns a SearchContext
// containing all intermediate state.
// Pipeline steps:
//  1. Query rewriting (Rewrite)
//  2. FTS5 full-text search
//  3. BFS graph traversal (starting from FTS5 hit nodes)
//  4. Combined scoring and ranking (ScoreAndRank)
//
// Any step failure returns the corresponding error with contextual information.
func (p *SearchPipeline) Execute(ctx context.Context, req *dktypes.QueryRequest) (*SearchContext, error) {
	sc := &SearchContext{
		OriginalQuery: req.Query,
		Config:        p.config,
		StartTime:     time.Now(),
	}

	// Step 1: 查询改写 — 缩写扩展 + 同义词扩展 + 分词语
	// Step 1: Query rewriting — abbreviation expansion + synonym expansion + tokenization
	rewritten, err := p.rewriter.Rewrite(ctx, req.Query)
	if err != nil {
		return nil, fmt.Errorf("query rewriting failed: %w", err)
	}
	sc.RewrittenQuery = rewritten
	log.Printf("[search-pipeline] query rewritten: %q → %q", req.Query, rewritten)

	// Step 2: FTS5 全文搜索
	// Step 2: FTS5 full-text search
	ftsIndex := storage.NewFTSIndex(p.db)
	ftsHits, err := ftsIndex.Search(ctx, sc.RewrittenQuery, req.Scope, "", p.config.MaxSearchResults)
	if err != nil {
		return nil, fmt.Errorf("%w: FTS5 search failed: %v", ErrInternal.Wrap(err), err)
	}
	sc.FTS5Hits = ftsHits
	log.Printf("[search-pipeline] FTS5 returned %d hits", len(ftsHits))

	// 如果没有命中，直接返回空结果
	// If no hits, return empty results immediately
	if len(ftsHits) == 0 {
		sc.TraversalResult = &storage.TraversalResult{}
		sc.ScoredResults = nil
		return sc, nil
	}
	textScores, err := normalizeFTS5Ranks(ftsHits)
	if err != nil {
		return nil, fmt.Errorf("%w: normalize FTS5 ranks: %v", ErrInternal.Wrap(err), err)
	}

	// Step 3: BFS 图遍历 — 从 FTS5 命中节点出发，按配置深度展开
	// Step 3: BFS graph traversal — expand from FTS5 hit nodes up to configured depth
	entryIDs := make([]string, 0, len(ftsHits))
	for _, h := range ftsHits {
		if h != nil && h.Node != nil {
			entryIDs = append(entryIDs, h.Node.ID)
		}
	}
	traversalResult, err := storage.TraverseBFS(ctx, p.db, storage.TraversalOptions{
		EntryIDs:      entryIDs,
		MaxDepth:      p.config.MaxBFSDepth,
		MinConfidence: p.config.DefaultMinConfidence,
		Limit:         p.config.MaxTraversalNodes,
		ScopeDomains:  req.Scope,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: BFS traversal failed: %v", ErrInternal.Wrap(err), err)
	}
	// BFS carries graph structure only. Supply normalized text relevance before
	// scoring; SQLite's raw rank is negative and lower means a better match.
	traversalResult.FTS5Hits = textScores
	sc.TraversalResult = traversalResult
	log.Printf("[search-pipeline] BFS traversal visited %d nodes, %d edges",
		len(traversalResult.Nodes), len(traversalResult.Edges))

	// Step 4: 综合打分排序 — 融合 BM25 文本得分与图结构得分
	// Step 4: Combined scoring and ranking — fuse BM25 text score with graph structure score
	scored := storage.ScoreAndRank(traversalResult, p.config.MaxSearchResults, p.config.BM25Weight, p.config.GraphWeight)
	sc.ScoredResults = scored
	log.Printf("[search-pipeline] scoring produced %d ranked results", len(scored))

	return sc, nil
}

// normalizeFTS5Ranks converts SQLite's non-positive, lower-is-better ranks
// into [0,1] relevance scores for the hybrid scorer. Dividing by the strongest
// match preserves relative strengths without letting the raw BM25 scale
// determine the balance with graph scores. Nodes outside this map score zero.
func normalizeFTS5Ranks(hits []*storage.FTS5Hit) (map[string]float64, error) {
	scores := make(map[string]float64, len(hits))
	var strongest float64
	for _, hit := range hits {
		if hit == nil || hit.Node == nil {
			continue
		}
		rank := hit.BM25Rank
		if rank > 0 || math.IsNaN(rank) || math.IsInf(rank, 0) {
			return nil, fmt.Errorf("node %q has invalid SQLite BM25 rank %g", hit.Node.ID, rank)
		}
		strength := -rank
		scores[hit.Node.ID] = strength
		if strength > strongest {
			strongest = strength
		}
	}
	for id, strength := range scores {
		if strongest == 0 {
			// Equal zero ranks still represent direct matches; avoid division by
			// zero while keeping them distinguishable from graph-only neighbors.
			scores[id] = 1
		} else {
			scores[id] = strength / strongest
		}
	}
	return scores, nil
}

// ——————————————————————————————————————————————————————————————————————————————
// CreateServiceConfig 辅助函数：从 config.ServiceConfig 转换为 service.ServiceConfig
// ——————————————————————————————————————————————————————————————————————————————

// CreateServiceConfig 将来自 config 包的配置值转换为服务层使用的 ServiceConfig。
// 该函数桥接了外部配置格式与服务层内部类型。
//
// CreateServiceConfig converts configuration values from the config package
// into the ServiceConfig used by the service layer. It bridges the external
// configuration format with the service-layer internal type.
func CreateServiceConfig(
	defaultDepth dktypes.Depth,
	defaultMaxTokens int,
	defaultMinConfidence float64,
	bm25Weight float64,
	graphWeight float64,
	maxBFSDepth int,
	maxSearchResults int,
	maxTraversalNodes int,
) *ServiceConfig {
	return &ServiceConfig{
		DefaultDepth:         defaultDepth,
		DefaultMaxTokens:     defaultMaxTokens,
		DefaultMinConfidence: defaultMinConfidence,
		BM25Weight:           bm25Weight,
		GraphWeight:          graphWeight,
		MaxBFSDepth:          maxBFSDepth,
		MaxSearchResults:     maxSearchResults,
		MaxTraversalNodes:    maxTraversalNodes,
	}
}
