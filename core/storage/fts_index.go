// Package storage 提供领域知识层的持久化存储层实现，包括数据库连接管理、
// 模式定义、数据迁移和 CRUD 仓储。
//
// 本文件包含 FTSIndex 结构体，封装对 SQLite FTS5 全文搜索虚拟表的操作。
// Package storage implements the persistence layer for the Domain Knowledge Layer,
// including database connection management, schema definition, data migration,
// and CRUD repositories.
//
// This file contains the FTSIndex struct, which encapsulates operations on
// the SQLite FTS5 full-text search virtual table.
package storage

import (
	"context"
	"fmt"

	"github.com/xcosmosbox/domain-knowledge-layer/core/dktypes"
)

// FTSIndex 封装对 nodes_fts 虚拟表的全文搜索操作。
// 通过 FTS5 MATCH 查询实现跨节点名称、摘要、描述、标签等字段的模糊搜索。
//
// FTSIndex encapsulates full-text search operations on the nodes_fts virtual table.
// Implements fuzzy search across node names, summaries, descriptions, tags, etc.
// via FTS5 MATCH queries.
type FTSIndex struct {
	db *DB
}

// FTS5Hit 表示一条 FTS5 全文搜索的命中结果，包含匹配到的节点及 BM25 相关性评分。
//
// FTS5Hit represents a single FTS5 full-text search hit, containing the matched
// node and its BM25 relevance ranking score.
type FTS5Hit struct {
	// Node 是匹配到的知识图谱节点。
	// Node is the matched knowledge graph node.
	Node *dktypes.Node
	// BM25Rank 是 BM25 相关性排名分数，数值越高表示匹配度越高。
	// BM25Rank is the BM25 relevance ranking score; higher values indicate better matches.
	BM25Rank float64
}

// NewFTSIndex 创建新的 FTSIndex 实例。
//
// NewFTSIndex creates a new FTSIndex instance.
func NewFTSIndex(db *DB) *FTSIndex {
	return &FTSIndex{db: db}
}

// Search 执行 FTS5 全文搜索查询。
//
// 参数说明：
//   - query: FTS5 MATCH 查询字符串（支持布尔运算符和前缀搜索）
//   - scope: 限定搜索的域列表（空列表表示不限定）
//   - label: 限定节点标签（零值表示不限定）
//   - limit: 最大返回结果数
//
// 结果按 BM25 rank 降序排列。
//
// Search executes an FTS5 full-text search query.
//
// Parameters:
//   - query: FTS5 MATCH query string (supports boolean operators and prefix searches)
//   - scope: domain list to restrict search (empty means no restriction)
//   - label: node label filter (zero value means no filter)
//   - limit: maximum number of results to return
//
// Results are ordered by descending BM25 rank.
func (idx *FTSIndex) Search(ctx context.Context, query string, scope []string, label dktypes.Label, limit int) ([]*FTS5Hit, error) {
	if query == "" {
		return nil, nil
	}

	idx.db.mu.RLock()
	defer idx.db.mu.RUnlock()

	// 构建 SELECT 列表和动态 WHERE 子句 / Build SELECT list and dynamic WHERE clause
	selectCols := `SELECT nodes.id, nodes.label, nodes.name, nodes.summary,
	                      nodes.synonyms, nodes.domain, nodes.subdomain,
	                      nodes.description, nodes.properties, nodes.tags,
	                      nodes.related_entities,
	                      nodes.confidence, nodes.provenance,
	                      nodes.source_refs, nodes.created_at, nodes.updated_at,
	                      nodes_fts.rank`

	baseClauses := []string{"nodes_fts MATCH ?"}
	args := []interface{}{query}

	// 可选：按域范围过滤 / Optional: filter by domain scope
	if len(scope) > 0 {
		placeholders := make([]string, len(scope))
		for i, d := range scope {
			placeholders[i] = "?"
			args = append(args, d)
		}
		baseClauses = append(baseClauses,
			fmt.Sprintf("nodes.domain IN (%s)", joinPlaceholders(placeholders)))
	}

	// 可选：按标签过滤 / Optional: filter by label
	if label != "" {
		baseClauses = append(baseClauses, "nodes.label = ?")
		args = append(args, string(label))
	}

	// 构建完整 SQL / Build full SQL
	whereClause := ""
	for i, clause := range baseClauses {
		if i == 0 {
			whereClause = "WHERE " + clause
		} else {
			whereClause += " AND " + clause
		}
	}

	querySQL := fmt.Sprintf(
		`%s
		   FROM nodes_fts
		   JOIN nodes ON nodes_fts.rowid = nodes.rowid
		 %s
		  ORDER BY rank
		  LIMIT ?`, selectCols, whereClause)
	args = append(args, limit)

	rows, err := idx.db.Conn().QueryContext(ctx, querySQL, args...)
	if err != nil {
		return nil, fmt.Errorf("FTSIndex.Search: %w", err)
	}
	defer rows.Close()

	var hits []*FTS5Hit
	for rows.Next() {
		n := &dbNode{}
		var rank float64
		if err := rows.Scan(
			&n.ID, &n.Label, &n.Name, &n.Summary, &n.Synonyms,
			&n.Domain, &n.Subdomain, &n.Description, &n.Properties,
			&n.Tags, &n.RelatedEntities, &n.Confidence,
			&n.Provenance, &n.SourceRefs, &n.CreatedAt, &n.UpdatedAt,
			&rank,
		); err != nil {
			return nil, fmt.Errorf("FTSIndex.Search scan: %w", err)
		}
		hits = append(hits, &FTS5Hit{
			Node:     n.ToPublic(),
			BM25Rank: rank,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("FTSIndex.Search rows: %w", err)
	}
	return hits, nil
}

// RebuildForSubdomain 重建指定域和子域下所有节点的 FTS 索引。
// 先删除旧的索引条目，再重新插入。
//
// RebuildForSubdomain rebuilds the FTS index for all nodes under the specified
// domain and subdomain. Old index entries are deleted first, then re-inserted.
func (idx *FTSIndex) RebuildForSubdomain(ctx context.Context, domain, subdomain string) error {
	idx.db.mu.Lock()
	defer idx.db.mu.Unlock()

	// 步骤 1：删除该子域下的旧 FTS 条目 / Step 1: Delete old FTS entries for this subdomain
	_, err := idx.db.Conn().ExecContext(ctx,
		`DELETE FROM nodes_fts WHERE rowid IN (
		    SELECT rowid FROM nodes WHERE domain = ? AND subdomain = ?
		)`, domain, subdomain)
	if err != nil {
		return fmt.Errorf("FTSIndex.RebuildForSubdomain delete: %w", err)
	}

	// 步骤 2：重新插入 / Step 2: Re-insert
	_, err = idx.db.Conn().ExecContext(ctx,
		`INSERT INTO nodes_fts (rowid, name, summary, synonyms, tags, description, domain, subdomain)
		 SELECT rowid, name, summary, synonyms, tags, description, domain, subdomain
		   FROM nodes
		  WHERE domain = ? AND subdomain = ?`, domain, subdomain)
	if err != nil {
		return fmt.Errorf("FTSIndex.RebuildForSubdomain insert: %w", err)
	}

	return nil
}

// RebuildAll 清空后重建全部节点的 FTS 索引。
//
// RebuildAll clears and rebuilds the FTS index for all nodes.
func (idx *FTSIndex) RebuildAll(ctx context.Context) error {
	idx.db.mu.Lock()
	defer idx.db.mu.Unlock()

	// 步骤 1：清空所有 FTS 条目 / Step 1: Delete all FTS entries
	_, err := idx.db.Conn().ExecContext(ctx, `DELETE FROM nodes_fts`)
	if err != nil {
		return fmt.Errorf("FTSIndex.RebuildAll delete: %w", err)
	}

	// 步骤 2：重新插入全部节点 / Step 2: Re-insert all nodes
	_, err = idx.db.Conn().ExecContext(ctx,
		`INSERT INTO nodes_fts (rowid, name, summary, synonyms, tags, description, domain, subdomain)
		 SELECT rowid, name, summary, synonyms, tags, description, domain, subdomain
		   FROM nodes`)
	if err != nil {
		return fmt.Errorf("FTSIndex.RebuildAll insert: %w", err)
	}

	return nil
}

// DeleteByRowID 删除指定 rowid 列表对应的 FTS 索引条目。
// rowID 对应 nodes 表的 rowid，用于在删除节点后清理残留的 FTS 记录。
//
// DeleteByRowID deletes FTS index entries for the specified rowid list.
// rowID corresponds to the nodes table rowid, used to clean up orphaned
// FTS records after node deletion.
func (idx *FTSIndex) DeleteByRowID(ctx context.Context, rowIDs []int64) error {
	if len(rowIDs) == 0 {
		return nil
	}

	idx.db.mu.Lock()
	defer idx.db.mu.Unlock()

	placeholders := make([]string, len(rowIDs))
	args := make([]interface{}, len(rowIDs))
	for i, id := range rowIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	querySQL := fmt.Sprintf(`DELETE FROM nodes_fts WHERE rowid IN (%s)`,
		joinPlaceholders(placeholders))

	_, err := idx.db.Conn().ExecContext(ctx, querySQL, args...)
	if err != nil {
		return fmt.Errorf("FTSIndex.DeleteByRowID: %w", err)
	}
	return nil
}

// DeleteOrphans 删除所有「rowid 已不在 nodes 表」的 FTS 残留条目（幂等自愈）。
// 重整（rebalance）在上一轮中途失败重入时，被删 node 的 FTS 行可能因上轮未到
// I-8 的 DeleteByRowID 而残留；本方法一条 SQL 收敛全部孤儿行，保证重跑可收敛。
// 全量/增量正常路径不需要它（各自的删除路径已精确清理）。
//
// DeleteOrphans removes every FTS row whose rowid no longer exists in nodes
// (idempotent self-healing, used by rebalance reentry after a mid-run failure).
func (idx *FTSIndex) DeleteOrphans(ctx context.Context) error {
	idx.db.mu.Lock()
	defer idx.db.mu.Unlock()

	if _, err := idx.db.Conn().ExecContext(ctx,
		`DELETE FROM nodes_fts WHERE rowid NOT IN (SELECT rowid FROM nodes)`); err != nil {
		return fmt.Errorf("FTSIndex.DeleteOrphans: %w", err)
	}
	return nil
}

// ——————————————————————————————————————————————————————————————————————————————
// 内部辅助函数 / Internal helper functions
// ——————————————————————————————————————————————————————————————————————————————

// joinPlaceholders 将占位符字符串连接为逗号分隔的列表。
// joinPlaceholders joins placeholder strings into a comma-separated list.
func joinPlaceholders(placeholders []string) string {
	result := ""
	for i, p := range placeholders {
		if i > 0 {
			result += ", "
		}
		result += p
	}
	return result
}
