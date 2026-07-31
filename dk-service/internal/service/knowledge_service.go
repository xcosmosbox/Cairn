// Package service 的本文件定义「知识库 DB 操作元能力抽象」KnowledgeService。
//
// 设计意图（架构解耦）：
//   - KnowledgeService 是「单个知识库」的 DB 操作元能力抽象层。任何查询前端
//     （dk CLI / mcp-server / 未来的 HTTP / gRPC 等）都只依赖本抽象，不直接写 SQL。
//   - 元能力（本层提供）：Search（FTS5+查询改写+BM25/图深度混合排序）、Impact（正向
//     BFS 影响遍历）、Status（统计）、ListDomains（域列表+覆盖率）、Describe（清单）、
//     GetNode（节点详情）。这些是与前端无关的、可复用的 DB 操作原语。
//   - 非元能力（前端各自实现）：多库路由（kg 参数）、联邦查询（跨库 fan-out + 合并）、
//     文本/JSON 展示格式化、JSON-RPC/stdio 传输协议。
//
// 一个 KnowledgeService 实例对应一个知识库（一个 *storage.DB）。多库场景下，前端
// 持有 map[string]KnowledgeService 并自行路由/联邦，从而保持本层的单库纯粹性。
//
// This file defines KnowledgeService — the single-knowledge-base DB meta-capability
// abstraction. Any query frontend (CLI/MCP/HTTP/gRPC) depends only on this interface
// and never writes SQL. Multi-DB routing and federation are frontend concerns.
package service

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/xcosmosbox/domain-knowledge-layer/core/dktypes"
	"github.com/xcosmosbox/domain-knowledge-layer/core/storage"
)

// ——————————————————————————————————————————————————————————————————————————————
// KnowledgeService — DB 操作元能力抽象接口
// ——————————————————————————————————————————————————————————————————————————————

// KnowledgeService 是单个知识库的 DB 操作元能力抽象。
// KnowledgeService is the single-knowledge-base DB meta-capability abstraction.
type KnowledgeService interface {
	// Search 按关键词检索：查询改写 → FTS5 → BFS 图遍历 → BM25+图深度混合排序。
	// Search performs keyword retrieval with rewriting, FTS5, BFS, and hybrid ranking.
	Search(ctx context.Context, keyword string, opts SearchOptions) (*SearchResult, error)

	// Impact 从命中实体出发做正向 BFS 影响遍历，返回可达节点与边。
	// Impact performs forward BFS impact traversal from the matched entity.
	Impact(ctx context.Context, keyword string, opts ImpactOptions) (*ImpactResult, error)

	// Status 返回知识库的统计摘要（节点/边总数、按类型/域分布、版本）。
	// Status returns statistical summary of the knowledge base.
	Status(ctx context.Context) (*StatusResult, error)

	// ListDomains 返回域列表及其覆盖率。
	// ListDomains returns the list of domains with coverage.
	ListDomains(ctx context.Context) (*dktypes.ListDomainsResponse, error)

	// GetNode 返回单个节点及其邻接边（入边+出边）。
	// GetNode returns a single node with its incident edges.
	GetNode(ctx context.Context, id string) (*NodeDetail, error)

	// ListCrossKGLinks 返回所有标记为 external_kg 的跨知识图谱链接。
	// ListCrossKGLinks returns all cross-knowledge-graph links marked external_kg.
	ListCrossKGLinks(ctx context.Context) ([]dktypes.CrossKGLink, error)

	// Sentinel 采样复杂度哨兵三指标（Q/单例率/边节点比）+ 累计改动占比。
	// 纯 CPU、零 LLM；record=true 时把快照追加进 sentinel.history 时序
	// （只写该独立观测键，KG 结构与既有 manifest 键字节不动）。
	// Sentinel samples the LLM-free complexity sentinel signals; with record=true
	// it appends the snapshot to the sentinel.history time series.
	Sentinel(ctx context.Context, record bool) (*SentinelSnapshot, error)
}

// ——————————————————————————————————————————————————————————————————————————————
// 元能力的参数与结构化返回（与前端展示无关）
// Meta-capability options and structured results (frontend-agnostic)
// ——————————————————————————————————————————————————————————————————————————————

// SearchOptions 是 Search 元能力的参数。
// SearchOptions are the parameters for the Search meta-capability.
type SearchOptions struct {
	// Scope 限定搜索的域列表（空=不限）。
	Scope []string
	// Limit 是最大返回结果数（≤0 使用配置默认）。
	Limit int
	// MinConfidence 是节点置信度硬门控（0 使用配置默认）。
	MinConfidence float64
}

// SearchHit 是一条检索命中（结构化，前端自行格式化为文本/JSON）。
// SearchHit is a single structured search hit.
type SearchHit struct {
	Node       *dktypes.Node
	BM25Score  float64
	GraphScore float64
	FinalScore float64
	Depth      int
	// Incoming 是该节点的入边（谁引用了它），用于 CLI 展示"被引用关系"。
	Incoming []*dktypes.Edge
}

// SearchResult 是 Search 的结构化结果。
// SearchResult is the structured result of Search.
type SearchResult struct {
	Keyword        string
	RewrittenQuery string
	Hits           []*SearchHit
	QueryMs        int64
}

// ImpactOptions 是 Impact 元能力的参数。
// ImpactOptions are the parameters for the Impact meta-capability.
type ImpactOptions struct {
	Scope         []string
	MaxDepth      int
	MinConfidence float64
}

// ImpactNode 是影响遍历中的一个可达节点及其遍历元数据。
// ImpactNode is a reachable node in impact traversal with metadata.
type ImpactNode struct {
	Node  *dktypes.Node
	Depth int
}

// ImpactResult 是 Impact 的结构化结果。
// ImpactResult is the structured result of Impact.
type ImpactResult struct {
	StartNodes []*dktypes.Node
	Nodes      []*ImpactNode
	Edges      []*dktypes.Edge
	QueryMs    int64
}

// StatusResult 是 Status 的结构化结果。
// StatusResult is the structured result of Status.
type StatusResult struct {
	KBVersion     string
	SchemaVersion int
	TotalNodes    int
	TotalEdges    int
	NodesByLabel  map[string]int
	EdgesByKind   map[string]int
	NodesByDomain map[string]int
}

// NodeDetail 是 GetNode 的结构化结果：节点 + 邻接边。
// NodeDetail is the structured result of GetNode: node plus incident edges.
type NodeDetail struct {
	Node     *dktypes.Node
	Incoming []*dktypes.Edge
	Outgoing []*dktypes.Edge
	// ResolvedFrom 非空表示发生了血缘重定向：调用方传入的旧 uuid 已在重整中
	// 被合并/拆分，本结果是其最新存活节点。零值 = 未重定向（向后兼容）。
	// ResolvedFrom is the caller-supplied stale uuid when a lineage redirect
	// occurred; empty means no redirect (backward compatible).
	ResolvedFrom string `json:"resolved_from,omitempty"`
	// RedirectReason 是人类可读的重定向说明（仅 ResolvedFrom 非空时有值）。
	// RedirectReason is a human-readable redirect explanation.
	RedirectReason string `json:"redirect_reason,omitempty"`
	// AlsoSplitInto 列出 split 源的其余存活后继（"uuid:name"），主后继为 Node 本身。
	// AlsoSplitInto lists the other living successors of a split source.
	AlsoSplitInto []string `json:"also_split_into,omitempty"`
}

// ——————————————————————————————————————————————————————————————————————————————
// knowledgeService — KnowledgeService 的默认实现
// ——————————————————————————————————————————————————————————————————————————————

// knowledgeService 是 KnowledgeService 的默认实现，封装单个 *storage.DB。
// knowledgeService is the default implementation wrapping a single *storage.DB.
type knowledgeService struct {
	db       *storage.DB
	nodeRepo *storage.NodeRepo
	edgeRepo *storage.EdgeRepo
	lineage  *storage.UUIDLineageRepo
	pipeline *SearchPipeline
	config   *ServiceConfig
}

// NewKnowledgeService 组装一个 KnowledgeService 实例。
// db 必须是已打开的知识库；cfg 提供检索参数（nil 使用 DefaultServiceConfig）；
// rewriter 是查询改写器（nil 使用不做缩写扩展的裸改写器）。
//
// NewKnowledgeService wires a KnowledgeService over a single knowledge base.
func NewKnowledgeService(db *storage.DB, cfg *ServiceConfig, rewriter *QueryRewriter) KnowledgeService {
	if cfg == nil {
		cfg = DefaultServiceConfig()
	}
	if rewriter == nil {
		rewriter = NewQueryRewriter(nil)
	}
	return &knowledgeService{
		db:       db,
		nodeRepo: storage.NewNodeRepo(db),
		edgeRepo: storage.NewEdgeRepo(db),
		lineage:  storage.NewUUIDLineageRepo(db),
		pipeline: NewSearchPipeline(db, rewriter, cfg),
		config:   cfg,
	}
}

// DefaultServiceConfig 返回一组合理的默认检索参数。
// DefaultServiceConfig returns sensible default retrieval parameters.
func DefaultServiceConfig() *ServiceConfig {
	return &ServiceConfig{
		DefaultDepth:         dktypes.DepthSummary,
		DefaultMaxTokens:     2000,
		DefaultMinConfidence: 0.0,
		BM25Weight:           0.6,
		GraphWeight:          0.4,
		MaxBFSDepth:          2,
		MaxSearchResults:     10,
		MaxTraversalNodes:    100,
	}
}

// Search 实现检索元能力：复用 SearchPipeline（查询改写→FTS5→BFS→混合排序），
// 再为每个命中补齐入边（供前端展示"被引用关系"）。
// Search reuses SearchPipeline and backfills incoming edges per hit.
func (s *knowledgeService) Search(ctx context.Context, keyword string, opts SearchOptions) (*SearchResult, error) {
	start := time.Now()
	limit := opts.Limit
	if limit <= 0 {
		limit = s.config.MaxSearchResults
	}
	req := &dktypes.QueryRequest{
		Query:         keyword,
		Scope:         opts.Scope,
		MinConfidence: opts.MinConfidence,
	}
	sc, err := s.pipeline.Execute(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("knowledge service search: %w", err)
	}

	res := &SearchResult{
		Keyword:        keyword,
		RewrittenQuery: sc.RewrittenQuery,
		QueryMs:        time.Since(start).Milliseconds(),
	}
	for i, sn := range sc.ScoredResults {
		if i >= limit {
			break
		}
		if sn == nil || sn.Node == nil {
			continue
		}
		incoming, _ := s.edgeRepo.GetIncoming(ctx, sn.Node.ID, 0)
		res.Hits = append(res.Hits, &SearchHit{
			Node:       sn.Node,
			BM25Score:  sn.BM25Score,
			GraphScore: sn.GraphScore,
			FinalScore: sn.FinalScore,
			Depth:      sn.Depth,
			Incoming:   incoming,
		})
	}
	return res, nil
}

// Impact 实现影响遍历元能力：先用 FTS5 找起点，再用 storage.TraverseBFS 正向遍历。
// Impact finds entry nodes via FTS5 then runs forward BFS via storage.TraverseBFS.
func (s *knowledgeService) Impact(ctx context.Context, keyword string, opts ImpactOptions) (*ImpactResult, error) {
	start := time.Now()
	maxDepth := opts.MaxDepth
	if maxDepth < 1 {
		maxDepth = 1
	}
	if maxDepth > 5 {
		maxDepth = 5
	}

	// Step 1: FTS5 找起点（最多 3 个，避免入口过多）。
	ftsIdx := storage.NewFTSIndex(s.db)
	hits, err := ftsIdx.Search(ctx, keyword, opts.Scope, "", 3)
	if err != nil {
		return nil, fmt.Errorf("knowledge service impact fts: %w", err)
	}
	res := &ImpactResult{QueryMs: 0}
	if len(hits) == 0 {
		res.QueryMs = time.Since(start).Milliseconds()
		return res, nil
	}
	entryIDs := make([]string, 0, len(hits))
	for _, h := range hits {
		if h != nil && h.Node != nil {
			entryIDs = append(entryIDs, h.Node.ID)
			res.StartNodes = append(res.StartNodes, h.Node)
		}
	}

	// Step 2: 正向 BFS 遍历。
	tr, err := storage.TraverseBFS(ctx, s.db, storage.TraversalOptions{
		EntryIDs:      entryIDs,
		MaxDepth:      maxDepth,
		MinConfidence: opts.MinConfidence,
		Limit:         s.config.MaxTraversalNodes,
		ScopeDomains:  opts.Scope,
	})
	if err != nil {
		return nil, fmt.Errorf("knowledge service impact bfs: %w", err)
	}
	for id, node := range tr.Nodes {
		res.Nodes = append(res.Nodes, &ImpactNode{Node: node, Depth: tr.Depths[id]})
	}
	// 稳定排序：按深度升序、ID 升序，保证输出确定性。
	sort.Slice(res.Nodes, func(i, j int) bool {
		if res.Nodes[i].Depth != res.Nodes[j].Depth {
			return res.Nodes[i].Depth < res.Nodes[j].Depth
		}
		return res.Nodes[i].Node.ID < res.Nodes[j].Node.ID
	})
	res.Edges = tr.Edges
	res.QueryMs = time.Since(start).Milliseconds()
	return res, nil
}

// Status 实现统计元能力：节点/边总数、按 label/kind/domain 分布、KB 版本。
// Status aggregates node/edge counts by label/kind/domain plus KB version.
func (s *knowledgeService) Status(ctx context.Context) (*StatusResult, error) {
	nodes, err := s.nodeRepo.ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("knowledge service status list nodes: %w", err)
	}
	res := &StatusResult{
		NodesByLabel:  make(map[string]int),
		EdgesByKind:   make(map[string]int),
		NodesByDomain: make(map[string]int),
		TotalNodes:    len(nodes),
	}
	for _, n := range nodes {
		res.NodesByLabel[string(n.Label)]++
		res.NodesByDomain[n.Domain]++
	}

	verRepo := storage.NewKBVersionRepo(s.db)
	if v, err := verRepo.Current(ctx); err == nil {
		res.KBVersion = v
	}
	if sv, err := s.db.SchemaVersion(); err == nil {
		res.SchemaVersion = sv
	}

	// 边统计：单条 GROUP BY 聚合，避免逐节点 O(N) 查询。
	// Edge stats: single GROUP BY aggregation instead of O(N) per-node queries.
	byKind, err := s.edgeRepo.CountByKind(ctx)
	if err != nil {
		return nil, fmt.Errorf("knowledge service status count edges: %w", err)
	}
	for kind, cnt := range byKind {
		res.EdgesByKind[kind] = cnt
		res.TotalEdges += cnt
	}
	return res, nil
}

// ListDomains 实现域列表元能力：复用 ListDomainsHandler + CoverageTracker。
// ListDomains reuses ListDomainsHandler + CoverageTracker.
func (s *knowledgeService) ListDomains(ctx context.Context) (*dktypes.ListDomainsResponse, error) {
	ct := NewCoverageTracker(s.nodeRepo)
	h := NewListDomainsHandler(s.nodeRepo, ct)
	return h.Handle(ctx)
}

// GetNode 实现节点详情元能力：节点 + 入边 + 出边。
// 按 UUID 直查落空时走血缘兜底（读侧只读，不写 KG 结构）：旧 uuid 顺
// uuid_lineage 链解析到最新存活节点，并在 NodeDetail 中如实标注重定向
// （ResolvedFrom/RedirectReason）；split 一对多源附 AlsoSplitInto 其余后继。
//
// GetNode returns a node with its incoming and outgoing edges. When the direct
// lookup misses, a read-only lineage fallback resolves a stale uuid to its
// latest living node and marks the redirect explicitly.
func (s *knowledgeService) GetNode(ctx context.Context, id string) (*NodeDetail, error) {
	node, err := s.nodeRepo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("knowledge service getnode: %w", err)
	}
	if node != nil {
		// —— 既有成功路径（零重定向），行为不变 ——
		incoming, _ := s.edgeRepo.GetIncoming(ctx, id, 0)
		outgoing, _ := s.edgeRepo.GetOutgoing(ctx, id, 0)
		return &NodeDetail{Node: node, Incoming: incoming, Outgoing: outgoing}, nil
	}

	// —— 血缘兜底：node==nil → 顺链解析旧 uuid → 最新存活 uuid ——
	latest, rerr := s.lineage.ResolveLatest(ctx, id)
	if rerr != nil {
		return nil, fmt.Errorf("knowledge service getnode resolve: %w", rerr)
	}
	if latest == id {
		// 无血缘记录：确实不存在（不误报重定向）。
		return nil, nil
	}
	n2, e2 := s.nodeRepo.GetByID(ctx, latest)
	if e2 != nil {
		return nil, fmt.Errorf("knowledge service getnode resolved: %w", e2)
	}
	if n2 == nil {
		// 链末端仍无存活节点（罕见脏数据）：如实按不存在处理。
		return nil, nil
	}
	incoming, _ := s.edgeRepo.GetIncoming(ctx, latest, 0)
	outgoing, _ := s.edgeRepo.GetOutgoing(ctx, latest, 0)
	detail := &NodeDetail{
		Node:           n2,
		Incoming:       incoming,
		Outgoing:       outgoing,
		ResolvedFrom:   id,
		RedirectReason: "该 UUID 已在重整中被合并/拆分，已解析到最新存活节点",
	}
	// split 一对多：展开旧 uuid 的全部存活终端后继，把非主后继附给调用方。
	if succ, serr := s.lineage.ResolveSuccessors(ctx, id); serr == nil && len(succ) > 1 {
		for _, u := range succ {
			if u == latest {
				continue
			}
			if sn, _ := s.nodeRepo.GetByID(ctx, u); sn != nil {
				detail.AlsoSplitInto = append(detail.AlsoSplitInto, u+":"+sn.Name)
			}
		}
	}
	return detail, nil
}

// ListCrossKGLinks 实现跨 KG 链接元能力：返回所有标记为 external_kg 的交叉引用。
// ListCrossKGLinks returns all cross-references marked as external_kg.
func (s *knowledgeService) ListCrossKGLinks(ctx context.Context) ([]dktypes.CrossKGLink, error) {
	repo := storage.NewCrossRefRepo(s.db)
	links, err := repo.ListCrossKGLinks(ctx)
	if err != nil {
		return nil, fmt.Errorf("knowledge service list cross kg links: %w", err)
	}
	return links, nil
}
