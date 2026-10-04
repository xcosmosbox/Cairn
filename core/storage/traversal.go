// Package storage 提供领域知识层的持久化存储层实现，包括数据库连接管理、
// 模式定义、数据迁移和 CRUD 仓储。
//
// 本文件包含应用层 BFS 图遍历引擎，在 SQLite 属性图之上执行广度优先遍历。
// 借鉴 CodeGraph 的 GraphTraverser 模式，每 BFS 步仅发 2 条简单 SQL，
// 不使用 WITH RECURSIVE，以获得可预测的性能和可控的内存占用。
//
// Package storage implements the persistence layer for the Cairn,
// including database connection management, schema definition, data migration,
// and CRUD repositories.
//
// This file contains the application-layer BFS graph traversal engine that performs
// breadth-first traversal over the SQLite property graph. Inspired by CodeGraph's
// GraphTraverser pattern, each BFS step issues only 2 simple SQL queries without
// WITH RECURSIVE, achieving predictable performance and controlled memory usage.
package storage

import (
	"context"
	"fmt"
	"strings"

	"github.com/xcosmosbox/cairn/core/dktypes"
)

// bfsQueueItem 是 BFS 队列元素，包含节点 ID 和当前深度。
// bfsQueueItem is a BFS queue element containing a node ID and its current depth.
type bfsQueueItem struct {
	id    string
	depth int
}

// TraversalOptions 定义 BFS 图遍历的参数。
// TraversalOptions defines the parameters for BFS graph traversal.
type TraversalOptions struct {
	// EntryIDs 是 FTS5 粗排返回的入口节点 ID 列表。
	// EntryIDs is the list of entry node IDs returned by FTS5 coarse ranking.
	EntryIDs []string
	// MaxDepth 是最大遍历深度: 1=邻域, 2=子图。
	// MaxDepth is the maximum traversal depth: 1=neighborhood, 2=subgraph.
	MaxDepth int
	// MinConfidence 是边的最小置信度阈值。
	// MinConfidence is the minimum confidence threshold for edges.
	MinConfidence float64
	// Limit 是最大返回节点数。
	// Limit is the maximum number of nodes to return.
	Limit int
	// ScopeDomains 是范围过滤的 domain 列表（空=不过滤）。
	// ScopeDomains is the list of domains for scope filtering (empty=no filter).
	ScopeDomains []string
}

// TraversalResult 是 BFS 遍历的结果，包含所有访问到的节点和边。
// TraversalResult is the result of a BFS traversal, containing all visited
// nodes and edges.
type TraversalResult struct {
	// Nodes 是遍历到的所有节点，key 为 node ID。
	// Nodes contains all nodes visited during traversal, keyed by node ID.
	Nodes map[string]*dktypes.Node
	// Edges 是遍历到的所有边。
	// Edges contains all edges visited during traversal.
	Edges []*dktypes.Edge
	// Depths 是每个节点从入口的最小深度。
	// Depths records the minimum depth of each node from the entry nodes.
	Depths map[string]int
	// FTS5Hits 由调用方填充归一化的 [0,1] BM25 相关性分数，越大越相关。
	// FTS5Hits contains caller-supplied normalized [0,1] BM25 relevance scores,
	// higher is better. BFS leaves this map empty; raw SQLite ranks must first
	// be converted by the caller. Nodes absent from the map score zero.
	FTS5Hits map[string]float64
}

// TraverseBFS 执行应用层 BFS 图遍历。
//
// 算法（借鉴 CodeGraph src/graph/traversal.ts → GraphTraverser.traverseBFS()）:
//  1. 将所有 EntryIDs 入队（depth=0），标记 visited
//  2. 从队列头部取出元素，检查 depth 上限
//  3. 取当前节点的所有出边（1 条 SQL，按 kind 优先级排序）
//  4. 收集未访问的邻居 ID，批量查询邻居节点（1 条 SQL，IN 子句）
//  5. 将邻居入队（depth+1），标记 visited
//  6. 队列为空或达到 Limit 时结束
//
// 每 BFS 步仅发 2 条简单 SQL（取边 + 批量取节点），零 WITH RECURSIVE。
// 性能预期（500 节点，depth≤2）: < 5ms
//
// TraverseBFS performs application-layer BFS graph traversal.
//
// Algorithm (inspired by CodeGraph src/graph/traversal.ts → GraphTraverser.traverseBFS()):
//  1. Enqueue all EntryIDs (depth=0), mark as visited
//  2. Dequeue from the front, check depth limit
//  3. Fetch all outgoing edges for the current node (1 SQL, ordered by kind priority)
//  4. Collect unvisited neighbor IDs, batch-fetch neighbor nodes (1 SQL, IN clause)
//  5. Enqueue neighbors (depth+1), mark as visited
//  6. Terminate when queue is empty or Limit is reached
//
// Each BFS step issues only 2 simple SQL queries (fetch edges + batch fetch nodes),
// zero WITH RECURSIVE usage. Expected performance (500 nodes, depth≤2): < 5ms.
func TraverseBFS(ctx context.Context, db *DB, opts TraversalOptions) (*TraversalResult, error) {
	if len(opts.EntryIDs) == 0 {
		return &TraversalResult{
			Nodes:    make(map[string]*dktypes.Node),
			Edges:    nil,
			Depths:   make(map[string]int),
			FTS5Hits: make(map[string]float64),
		}, nil
	}
	if opts.MaxDepth <= 0 {
		opts.MaxDepth = 2
	}
	if opts.Limit <= 0 {
		opts.Limit = 100
	}

	nodeRepo := NewNodeRepo(db)
	edgeRepo := NewEdgeRepo(db)

	visited := make(map[string]struct{})
	result := &TraversalResult{
		Nodes:    make(map[string]*dktypes.Node),
		Depths:   make(map[string]int),
		FTS5Hits: make(map[string]float64),
	}

	// 初始化队列，将所有入口节点入队 / Initialize queue with all entry nodes
	queue := make([]bfsQueueItem, 0, len(opts.EntryIDs))
	for _, eid := range opts.EntryIDs {
		queue = append(queue, bfsQueueItem{id: eid, depth: 0})
	}

	// 批量加载入口节点 / Batch-load entry nodes
	entryNodes, err := nodeRepo.GetByIDs(ctx, opts.EntryIDs)
	if err != nil {
		return nil, fmt.Errorf("load entry nodes: %w", err)
	}
	for id, node := range entryNodes {
		result.Nodes[id] = node
		result.Depths[id] = 0
	}

	for len(queue) > 0 && len(result.Nodes) < opts.Limit {
		item := queue[0]
		queue = queue[1:]

		if _, seen := visited[item.id]; seen {
			continue
		}
		visited[item.id] = struct{}{}

		if item.depth >= opts.MaxDepth {
			continue
		}

		// 步骤 1：取当前节点的所有出边（按 kind 优先级排序）
		// Step 1: Get outgoing edges (priority-ordered by kind)
		outgoing, err := edgeRepo.GetOutgoing(ctx, item.id, opts.MinConfidence)
		if err != nil {
			return nil, fmt.Errorf("get edges for %s: %w", item.id, err)
		}

		// 收集未访问的邻居 ID / Collect unvisited neighbor IDs
		var neighborIDs []string
		for _, e := range outgoing {
			if _, seen := visited[e.TargetID]; !seen {
				neighborIDs = append(neighborIDs, e.TargetID)
			}
		}
		if len(neighborIDs) == 0 {
			continue
		}

		// 步骤 2：批量查询邻居节点 / Step 2: Batch fetch neighbor nodes
		neighbors, err := nodeRepo.GetByIDs(ctx, neighborIDs)
		if err != nil {
			return nil, fmt.Errorf("batch get neighbors: %w", err)
		}

		for _, e := range outgoing {
			nextID := e.TargetID
			if _, seen := visited[nextID]; seen {
				continue
			}
			nextNode, ok := neighbors[nextID]
			if !ok {
				continue
			}

			// 应用 scope 过滤 / Apply scope filter
			if len(opts.ScopeDomains) > 0 {
				inScope := false
				for _, d := range opts.ScopeDomains {
					if nextNode.Domain == d {
						inScope = true
						break
					}
				}
				if !inScope {
					continue
				}
			}

			result.Nodes[nextID] = nextNode
			result.Edges = append(result.Edges, e)
			newDepth := item.depth + 1
			if existing, ok := result.Depths[nextID]; !ok || newDepth < existing {
				result.Depths[nextID] = newDepth
			}
			queue = append(queue, bfsQueueItem{id: nextID, depth: newDepth})
		}
	}
	return result, nil
}

// BuildNodeID 使用三段式格式构造节点 ID: "Domain::Subdomain::EntityName"。
// 注意：仅用于 skill/domain/subdomain 层节点；entity/concept 已改用 UUID 主键
// （由 extract.AssignNodeUUIDs 分配），不再使用此格式。
//
// BuildNodeID constructs a node ID in the three-segment format: "Domain::Subdomain::EntityName".
// NOTE: used only for skill/domain/subdomain layer nodes; entity/concept now use UUID
// primary keys (assigned by extract.AssignNodeUUIDs) and no longer use this format.
func BuildNodeID(domain, subdomain, entityName string) string {
	return fmt.Sprintf("%s::%s::%s", domain, subdomain, entityName)
}

// ParseNodeID 将三段式节点 ID 解析为 domain, subdomain, entityName。
// 注意：仅用于 skill/domain/subdomain 层节点；entity/concept 已改用 UUID 主键。
//
// ParseNodeID parses a three-segment node ID into domain, subdomain, and entityName.
// NOTE: used only for skill/domain/subdomain layer nodes; entity/concept now use UUID.
func ParseNodeID(id string) (domain, subdomain, entityName string, err error) {
	parts := strings.Split(id, "::")
	if len(parts) != 3 {
		return "", "", "", fmt.Errorf("invalid node id: %s", id)
	}
	return parts[0], parts[1], parts[2], nil
}
