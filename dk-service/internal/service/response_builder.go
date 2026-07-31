package service

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/xcosmosbox/domain-knowledge-layer/core/dktypes"
	"github.com/xcosmosbox/domain-knowledge-layer/core/storage"
)

// ——————————————————————————————————————————————————————————————————————————————
// ResponseBuilder — 查询响应构建器
// ——————————————————————————————————————————————————————————————————————————————

// ResponseBuilder 负责将 SearchContext 中的检索结果按不同的深度级别
// （summary / entity / neighborhood / subgraph）组装成对应的 QueryResponse。
// 每个深度级别对应不同的响应结构，从简单摘要到完整子图逐步递增信息密度。
//
// ResponseBuilder is responsible for assembling the retrieval results in a
// SearchContext into the appropriate QueryResponse for each depth level
// (summary / entity / neighborhood / subgraph). Each depth level maps to a
// different response structure, with information density increasing from
// simple summaries to complete subgraphs.
type ResponseBuilder struct {
	// config 是服务层配置，影响 token 估算和默认行为。
	// config is the service-layer configuration, influencing token estimation
	// and default behaviors.
	config *ServiceConfig
}

// NewResponseBuilder 创建响应构建器实例。
// cfg 是服务层配置，用于控制 token 上限等参数。
//
// NewResponseBuilder creates a new response builder instance.
// cfg is the service-layer configuration, used to control parameters such
// as token limits.
func NewResponseBuilder(cfg *ServiceConfig) *ResponseBuilder {
	return &ResponseBuilder{config: cfg}
}

// Build 根据指定的 depth 将 SearchContext 中的检索结果构建为 QueryResponse。
// 分发逻辑：
//   - summary     → buildSummaryResponse
//   - entity      → buildEntityResponse
//   - neighborhood → buildNeighborhoodResponse
//   - subgraph    → buildSubgraphResponse
// maxTokens 限制响应中 content 字段的总 token 数量。
//
// Build constructs a QueryResponse from the retrieval results in the
// SearchContext according to the specified depth.
// Dispatch logic:
//   - summary     → buildSummaryResponse
//   - entity      → buildEntityResponse
//   - neighborhood → buildNeighborhoodResponse
//   - subgraph    → buildSubgraphResponse
// maxTokens limits the total number of tokens in the response content fields.
func (b *ResponseBuilder) Build(
	sc *SearchContext,
	depth dktypes.Depth,
	maxTokens int,
) (*dktypes.QueryResponse, error) {
	if sc == nil {
		return nil, fmt.Errorf("search context must not be nil")
	}

	var results any
	var err error
	totalMatches := 0

	// 统计总命中数
	// Count total matches
	if sc.ScoredResults != nil {
		totalMatches = len(sc.ScoredResults)
	} else if sc.FTS5Hits != nil {
		totalMatches = len(sc.FTS5Hits)
	}

	switch depth {
	case dktypes.DepthSummary:
		results, err = b.buildSummaryResponse(sc, maxTokens)
	case dktypes.DepthEntity:
		results, err = b.buildEntityResponse(sc, maxTokens)
	case dktypes.DepthNeighborhood:
		results, err = b.buildNeighborhoodResponse(sc, maxTokens)
	case dktypes.DepthSubgraph:
		results, err = b.buildSubgraphResponse(sc, maxTokens)
	default:
		return nil, fmt.Errorf("unsupported depth: %s", depth)
	}

	if err != nil {
		return nil, fmt.Errorf("build response for depth %s: %w", depth, err)
	}

	// 构造元数据
	// Build metadata
	tokensUsed := estimateTokens(results)
	var queryMs int64
	if !sc.StartTime.IsZero() {
		queryMs = timeSinceMs(sc.StartTime)
	}

	return &dktypes.QueryResponse{
		Results: results,
		Meta: dktypes.QueryMeta{
			Depth:        depth,
			TotalMatches: totalMatches,
			TokensUsed:   tokensUsed,
			QueryMs:      queryMs,
		},
	}, nil
}

// ——————————————————————————————————————————————————————————————————————————————
// buildSummaryResponse — 摘要级别响应
// ——————————————————————————————————————————————————————————————————————————————

// buildSummaryResponse 构建 depth=summary 的查询响应。
// 仅返回每个匹配节点的基本信息：ID、名称、摘要、类型、所属域/子域、相关性评分、
// 关联数量及同义词。
//
// buildSummaryResponse builds the query response for depth=summary.
// Returns only basic information for each matched node: ID, name, summary,
// type, domain/subdomain, relevance score, related count, and synonyms.
func (b *ResponseBuilder) buildSummaryResponse(
	sc *SearchContext,
	maxTokens int,
) ([]dktypes.SummaryResult, error) {
	results := make([]dktypes.SummaryResult, 0)

	// 从打分结果中提取摘要信息
	// Extract summary information from scored results
	for _, sn := range sc.ScoredResults {
		if sn == nil || sn.Node == nil {
			continue
		}
		node := sn.Node

		// 应用置信度过滤
		// Apply confidence filter
		if node.Confidence < b.config.DefaultMinConfidence {
			continue
		}

		// 解析同义词列表
		// Parse synonym list
		synonyms := parseCommaSep(node.Synonyms)

		// 估算关联实体数量
		// Estimate related entity count
		relatedCount := len(parseCommaSep(node.RelatedEntities))

		sr := dktypes.SummaryResult{
			EntityID:     node.ID,
			Name:         node.Name,
			Summary:      node.Summary,
			Type:         node.Label,
			Domain:       node.Domain,
			Subdomain:    node.Subdomain,
			Relevance:    sn.FinalScore,
			RelatedCount: relatedCount,
			Synonyms:     synonyms,
		}

		results = append(results, sr)

		// 检查 token 上限
		// Check token limit
		if estimateTokens(results) > maxTokens && maxTokens > 0 {
			break
		}
	}

	return results, nil
}

// ——————————————————————————————————————————————————————————————————————————————
// buildEntityResponse — 实体级别响应
// ——————————————————————————————————————————————————————————————————————————————

// buildEntityResponse 构建 depth=entity 的查询响应。
// 在每个匹配实体的摘要信息基础上，额外返回完整内容描述、所有出边关系、
// 置信度、来源出处及引用列表。
//
// buildEntityResponse builds the query response for depth=entity.
// In addition to summary information for each matched entity, it returns
// the full content description, all outgoing relationships, confidence,
// provenance, and source references.
func (b *ResponseBuilder) buildEntityResponse(
	sc *SearchContext,
	maxTokens int,
) ([]dktypes.EntityResult, error) {
	results := make([]dktypes.EntityResult, 0)

	for _, sn := range sc.ScoredResults {
		if sn == nil || sn.Node == nil {
			continue
		}
		node := sn.Node

		if node.Confidence < b.config.DefaultMinConfidence {
			continue
		}

		// 构建该节点的关系列表
		// Build the relationship list for this node
		relations := b.buildRelationPaths(node.ID, sc.TraversalResult)

		er := dktypes.EntityResult{
			EntityID:   node.ID,
			Name:       node.Name,
			Summary:    node.Summary,
			Type:       node.Label,
			Domain:     node.Domain,
			Subdomain:  node.Subdomain,
			Content:    node.Description,
			Synonyms:   parseCommaSep(node.Synonyms),
			Relations:  relations,
			Confidence: node.Confidence,
			Provenance: node.Provenance,
			SourceRefs: parseCommaSep(node.SourceRefs),
		}

		results = append(results, er)

		if estimateTokens(results) > maxTokens && maxTokens > 0 {
			break
		}
	}

	return results, nil
}

// ——————————————————————————————————————————————————————————————————————————————
// buildNeighborhoodResponse — 邻域级别响应
// ——————————————————————————————————————————————————————————————————————————————

// buildNeighborhoodResponse 构建 depth=neighborhood 的查询响应。
// 对每个匹配实体，除自身完整信息外，还返回直接邻居节点的摘要信息、
// 关系类型、关系描述及跳数，以便快速了解实体周边环境。
//
// buildNeighborhoodResponse builds the query response for depth=neighborhood.
// For each matched entity, in addition to its own complete information,
// it returns summary information for immediate neighbor nodes, relationship
// types, relationship descriptions, and hop counts, enabling a quick overview
// of the entity's surrounding context.
func (b *ResponseBuilder) buildNeighborhoodResponse(
	sc *SearchContext,
	maxTokens int,
) ([]NeighborhoodResult, error) {
	results := make([]NeighborhoodResult, 0)
	traversal := sc.TraversalResult

	for _, sn := range sc.ScoredResults {
		if sn == nil || sn.Node == nil {
			continue
		}
		node := sn.Node

		if node.Confidence < b.config.DefaultMinConfidence {
			continue
		}

		// 构建关系列表
		// Build relationship list
		relations := b.buildRelationPaths(node.ID, traversal)

		// 构建邻居信息列表
		// Build neighbor information list
		neighbors := b.collectNeighbors(node.ID, traversal)

		nr := NeighborhoodResult{
			EntityResult: dktypes.EntityResult{
				EntityID:   node.ID,
				Name:       node.Name,
				Summary:    node.Summary,
				Type:       node.Label,
				Domain:     node.Domain,
				Subdomain:  node.Subdomain,
				Content:    node.Description,
				Synonyms:   parseCommaSep(node.Synonyms),
				Relations:  relations,
				Confidence: node.Confidence,
				Provenance: node.Provenance,
				SourceRefs: parseCommaSep(node.SourceRefs),
			},
			Neighbors: neighbors,
		}

		results = append(results, nr)

		if estimateTokens(results) > maxTokens && maxTokens > 0 {
			break
		}
	}

	return results, nil
}

// ——————————————————————————————————————————————————————————————————————————————
// buildSubgraphResponse — 子图级别响应
// ——————————————————————————————————————————————————————————————————————————————

// buildSubgraphResponse 构建 depth=subgraph 的查询响应。
// 将所有 BFS 遍历到的节点和边组织为一系列 SubgraphComponent，按类型区分为
// entity、concept、relation 和 conflict，每条记录包含路径、跳数和描述信息。
//
// buildSubgraphResponse builds the query response for depth=subgraph.
// All nodes and edges visited during BFS traversal are organized into a series
// of SubgraphComponents, distinguished by type into entity, concept, relation,
// and conflict, with each record containing path, hop count, and descriptions.
func (b *ResponseBuilder) buildSubgraphResponse(
	sc *SearchContext,
	maxTokens int,
) (*dktypes.SubgraphResponse, error) {
	traversal := sc.TraversalResult
	if traversal == nil {
		return &dktypes.SubgraphResponse{Components: []dktypes.SubgraphComponent{}}, nil
	}

	components := make([]dktypes.SubgraphComponent, 0)

	// 处理已访问的节点 — 从打分结果中取排名靠前的节点
	// Process visited nodes — take top-ranked nodes from scored results
	seenNodes := make(map[string]bool)
	for _, sn := range sc.ScoredResults {
		if sn == nil || sn.Node == nil {
			continue
		}
		node := sn.Node
		if seenNodes[node.ID] {
			continue
		}
		seenNodes[node.ID] = true

		compType := "entity"
		if node.Label == dktypes.LabelConcept {
			compType = "concept"
		}

		// 从深度映射中获取该节点的跳数
		// Get the hop count for this node from the depth map
		hopCount := 0
		if traversal.Depths != nil {
			hopCount = traversal.Depths[node.ID]
		}

		comp := dktypes.SubgraphComponent{
			Type:       compType,
			EntityID:   node.ID,
			Name:       node.Name,
			Content:    node.Description,
			Domain:     node.Domain,
			Subdomain:  node.Subdomain,
			Confidence: node.Confidence,
			Provenance: node.Provenance,
			HopCount:   hopCount,
		}
		components = append(components, comp)

		if estimateTokens(components) > maxTokens && maxTokens > 0 {
			break
		}
	}

	// 处理边 — 为每条边生成 relation 类型的组件
	// Process edges — generate relation-type components for each edge
	for _, edge := range traversal.Edges {
		comp := dktypes.SubgraphComponent{
			Type:         "relation",
			RelationDesc: edge.Description,
			Confidence:   edge.Confidence,
			Provenance:   edge.Provenance,
			Path:         []string{edge.SourceID, edge.TargetID},
		}
		// 计算跳数 — 取源节点和目标节点中较大的跳数 + 1
		// Calculate hop count — max of source and target depth + 1
		if traversal.Depths != nil {
			srcDepth := traversal.Depths[edge.SourceID]
			tgtDepth := traversal.Depths[edge.TargetID]
			if srcDepth > tgtDepth {
				comp.HopCount = srcDepth
			} else {
				comp.HopCount = tgtDepth
			}
		}
		components = append(components, comp)

		if estimateTokens(components) > maxTokens && maxTokens > 0 {
			break
		}
	}

	return &dktypes.SubgraphResponse{Components: components}, nil
}

// ——————————————————————————————————————————————————————————————————————————————
// buildRelationPaths — 关系路径构建
// ——————————————————————————————————————————————————————————————————————————————

// buildRelationPaths 从 BFS 遍历结果中提取指定节点的所有出边关系，
// 返回按 RelationKind 遍历优先级排序的 RelationOnEdge 列表。
// 如果遍历结果为空或该节点无边，返回空切片。
//
// buildRelationPaths extracts all outgoing edge relationships for the specified
// node from the BFS traversal result, returning a list of RelationOnEdge
// sorted by RelationKind traversal priority. Returns an empty slice if the
// traversal result is nil or the node has no edges.
func (b *ResponseBuilder) buildRelationPaths(
	nodeID string,
	traversal *storage.TraversalResult,
) []dktypes.RelationOnEdge {
	if traversal == nil || traversal.Edges == nil {
		return nil
	}

	relations := make([]dktypes.RelationOnEdge, 0)
	for _, edge := range traversal.Edges {
		if edge.SourceID != nodeID {
			continue
		}

		targetName := ""
		if targetNode, ok := traversal.Nodes[edge.TargetID]; ok && targetNode != nil {
			targetName = targetNode.Name
		}

		rel := dktypes.RelationOnEdge{
			Kind:        edge.Kind,
			TargetName:  targetName,
			TargetID:    edge.TargetID,
			Description: edge.Description,
		}
		relations = append(relations, rel)
	}

	// 按遍历优先级排序
	// Sort by traversal priority
	sort.Slice(relations, func(i, j int) bool {
		return relations[i].Kind.TraversalPriority() < relations[j].Kind.TraversalPriority()
	})

	return relations
}

// ——————————————————————————————————————————————————————————————————————————————
// collectNeighbors — 邻居信息收集
// ——————————————————————————————————————————————————————————————————————————————

// collectNeighbors 从 BFS 遍历结果中收集指定节点的所有邻居信息。
// 通过遍历所有边来查找与给定 nodeID 相邻的节点（包括出边和入边），
// 返回按跳数排序的 NeighborInfo 列表。
//
// collectNeighbors collects neighbor information for the specified node
// from the BFS traversal result. It scans all edges to find nodes adjacent
// to the given nodeID (both outgoing and incoming), returning a list of
// NeighborInfo sorted by hop count.
func (b *ResponseBuilder) collectNeighbors(
	nodeID string,
	traversal *storage.TraversalResult,
) []NeighborInfo {
	if traversal == nil || traversal.Edges == nil {
		return nil
	}

	neighborMap := make(map[string]NeighborInfo)
	for _, edge := range traversal.Edges {
		// 出边：nodeID → target
		// Outgoing: nodeID → target
		if edge.SourceID == nodeID {
			if _, exists := neighborMap[edge.TargetID]; !exists {
				neighborNode := traversal.Nodes[edge.TargetID]
				name := ""
				summary := ""
				label := dktypes.LabelEntity
				if neighborNode != nil {
					name = neighborNode.Name
					summary = neighborNode.Summary
					label = neighborNode.Label
				}
				hopCount := 1
				if traversal.Depths != nil {
					hopCount = traversal.Depths[edge.TargetID]
				}
				neighborMap[edge.TargetID] = NeighborInfo{
					NodeID:       edge.TargetID,
					Name:         name,
					Summary:      summary,
					Type:         label,
					RelationKind: edge.Kind,
					RelationDesc: edge.Description,
					HopCount:     hopCount,
				}
			}
		}

		// 入边：source → nodeID（nodeID 作为目标）
		// Incoming: source → nodeID (nodeID is the target)
		if edge.TargetID == nodeID {
			if _, exists := neighborMap[edge.SourceID]; !exists {
				neighborNode := traversal.Nodes[edge.SourceID]
				name := ""
				summary := ""
				label := dktypes.LabelEntity
				if neighborNode != nil {
					name = neighborNode.Name
					summary = neighborNode.Summary
					label = neighborNode.Label
				}
				hopCount := 1
				if traversal.Depths != nil {
					hopCount = traversal.Depths[edge.SourceID]
				}
				neighborMap[edge.SourceID] = NeighborInfo{
					NodeID:       edge.SourceID,
					Name:         name,
					Summary:      summary,
					Type:         label,
					RelationKind: edge.Kind,
					RelationDesc: fmt.Sprintf("incoming: %s", edge.Description),
					HopCount:     hopCount,
				}
			}
		}
	}

	// 转换为切片并按跳数排序
	// Convert to slice and sort by hop count
	neighbors := make([]NeighborInfo, 0, len(neighborMap))
	for _, ni := range neighborMap {
		neighbors = append(neighbors, ni)
	}

	sort.Slice(neighbors, func(i, j int) bool {
		return neighbors[i].HopCount < neighbors[j].HopCount
	})

	return neighbors
}

// ——————————————————————————————————————————————————————————————————————————————
// estimateTokens — Token 数量估算
// ——————————————————————————————————————————————————————————————————————————————

// estimateTokens 使用启发式方法估算给定值的 token 数量。
// 基于粗略的中英文混合估算：每个中文字符约 1.5 token，每个英文词约 1.3 token，
// 最终取整数向上取整并应用最小为 1 的约束。
//
// estimateTokens estimates the number of tokens for the given value using
// a heuristic approach. Based on a rough mixed Chinese-English estimate:
// ~1.5 tokens per Chinese character, ~1.3 tokens per English word,
// rounding up with a minimum of 1.
func estimateTokens(v any) int {
	if v == nil {
		return 0
	}

	text := fmt.Sprintf("%+v", v)

	// 统计中文字符数量（粗略：Unicode 范围 一-鿿）
	// Count Chinese characters (rough: Unicode range 一-鿿)
	chineseChars := 0
	englishWords := 0
	inWord := false

	for _, r := range text {
		if r >= 0x4e00 && r <= 0x9fff {
			chineseChars++
			inWord = false
		} else if r == ' ' || r == '\n' || r == '\t' || r == ',' || r == '.' ||
			r == ';' || r == ':' || r == '{' || r == '}' || r == '[' || r == ']' {
			inWord = false
		} else {
			if !inWord {
				englishWords++
				inWord = true
			}
		}
	}

	// 估算：中文 ~1.5 token/字，英文 ~1.3 token/词
	// Estimate: Chinese ~1.5 tokens/char, English ~1.3 tokens/word
	estimated := int(float64(chineseChars)*1.5 + float64(englishWords)*1.3)
	if estimated < 1 {
		estimated = 1
	}
	return estimated
}

// ——————————————————————————————————————————————————————————————————————————————
// 内部辅助函数
// ——————————————————————————————————————————————————————————————————————————————

// parseCommaSep 将以逗号分隔的字符串解析为字符串切片，
// 去除每个元素的空白。空字符串返回 nil。
//
// parseCommaSep parses a comma-separated string into a string slice,
// trimming whitespace from each element. Returns nil for empty strings.
func parseCommaSep(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// timeSinceMs 返回自给定时间以来的毫秒数。
// timeSinceMs returns the number of milliseconds since the given time.
func timeSinceMs(t time.Time) int64 {
	return time.Since(t).Milliseconds()
}
