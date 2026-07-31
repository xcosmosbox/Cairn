// Package storage 提供领域知识层的持久化存储层实现，包括数据库连接管理、
// 模式定义、数据迁移和 CRUD 仓储。
//
// 本文件包含 NodeRepo 仓储，提供 nodes 表的完整 CRUD 操作。
// Package storage implements the persistence layer for the Domain Knowledge Layer,
// including database connection management, schema definition, data migration,
// and CRUD repositories.
//
// This file contains the NodeRepo repository, providing full CRUD operations
// for the nodes table.
package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/xcosmosbox/domain-knowledge-layer/core/dktypes"
)

// NodeRepo 封装对 nodes 表的所有 CRUD 操作。
// 所有方法均通过嵌入的 *DB 访问数据库，并使用互斥锁保证线程安全。
//
// NodeRepo encapsulates all CRUD operations on the nodes table.
// All methods access the database through the embedded *DB and
// use mutex locks to ensure thread safety.
type NodeRepo struct {
	db *DB
}

// NewNodeRepo 创建新的 NodeRepo 实例。
//
// NewNodeRepo creates a new NodeRepo instance.
func NewNodeRepo(db *DB) *NodeRepo {
	return &NodeRepo{db: db}
}

// nodeColumns 是 nodes 表的公共查询列（无 visibility 列）。
// nodeColumns is the shared SELECT column list for the nodes table (no visibility).
const nodeColumns = `id, label, name, summary, synonyms, domain, subdomain,
        description, properties, tags, related_entities,
        confidence, provenance, source_refs, file_slug,
        created_at, updated_at`

// GetByID 通过主键 ID 查询单个节点。
// 若未找到匹配节点，返回 nil, nil。
//
// GetByID retrieves a single node by its primary key ID.
// Returns nil, nil if no matching node is found.
func (r *NodeRepo) GetByID(ctx context.Context, id string) (*dktypes.Node, error) {
	r.db.mu.RLock()
	defer r.db.mu.RUnlock()

	row := r.db.Conn().QueryRowContext(ctx,
		`SELECT `+nodeColumns+` FROM nodes WHERE id = ?`, id)

	n := &dbNode{}
	if err := scanNodeRow(row, n); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("NodeRepo.GetByID %s: %w", id, err)
	}
	return n.ToPublic(), nil
}

// GetByIDs 批量查询多个节点，返回以 ID 为键的 map。
// 若节点不存在，该 ID 不会出现在结果 map 中。
//
// GetByIDs fetches multiple nodes by their IDs, returning a map keyed by ID.
// If a node does not exist, its ID will not appear in the result map.
func (r *NodeRepo) GetByIDs(ctx context.Context, ids []string) (map[string]*dktypes.Node, error) {
	if len(ids) == 0 {
		return map[string]*dktypes.Node{}, nil
	}

	r.db.mu.RLock()
	defer r.db.mu.RUnlock()

	// 构建 IN 子句的占位符 / Build IN clause placeholders
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}

	query := fmt.Sprintf(
		`SELECT `+nodeColumns+` FROM nodes WHERE id IN (%s)`, strings.Join(placeholders, ","))

	rows, err := r.db.Conn().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("NodeRepo.GetByIDs: %w", err)
	}
	defer rows.Close()

	nodes, err := scanNodes(rows)
	if err != nil {
		return nil, err
	}
	result := make(map[string]*dktypes.Node, len(nodes))
	for _, n := range nodes {
		result[n.ID] = n
	}
	return result, nil
}

// ListByDomain 查询指定业务域下的所有节点。
//
// ListByDomain retrieves all nodes within the specified business domain.
func (r *NodeRepo) ListByDomain(ctx context.Context, domain string) ([]*dktypes.Node, error) {
	r.db.mu.RLock()
	defer r.db.mu.RUnlock()

	rows, err := r.db.Conn().QueryContext(ctx,
		`SELECT `+nodeColumns+` FROM nodes WHERE domain = ?`, domain)
	if err != nil {
		return nil, fmt.Errorf("NodeRepo.ListByDomain: %w", err)
	}
	defer rows.Close()

	return scanNodes(rows)
}

// ListBySubdomain 查询指定业务域和子域下的所有节点。
//
// ListBySubdomain retrieves all nodes within the specified business domain and subdomain.
func (r *NodeRepo) ListBySubdomain(ctx context.Context, domain, subdomain string) ([]*dktypes.Node, error) {
	r.db.mu.RLock()
	defer r.db.mu.RUnlock()

	rows, err := r.db.Conn().QueryContext(ctx,
		`SELECT `+nodeColumns+` FROM nodes WHERE domain = ? AND subdomain = ?`, domain, subdomain)
	if err != nil {
		return nil, fmt.Errorf("NodeRepo.ListBySubdomain: %w", err)
	}
	defer rows.Close()

	return scanNodes(rows)
}

// ListAll 返回数据库中所有节点。
//
// ListAll returns all nodes in the database.
func (r *NodeRepo) ListAll(ctx context.Context) ([]*dktypes.Node, error) {
	r.db.mu.RLock()
	defer r.db.mu.RUnlock()
	rows, err := r.db.Conn().QueryContext(ctx,
		`SELECT `+nodeColumns+` FROM nodes`)
	if err != nil {
		return nil, fmt.Errorf("NodeRepo.ListAll: %w", err)
	}
	defer rows.Close()
	return scanNodes(rows)
}

// Insert inserts a new node record into the nodes table.
func (r *NodeRepo) Insert(ctx context.Context, node *dktypes.Node) error {
	r.db.mu.Lock()
	defer r.db.mu.Unlock()

	_, err := r.db.Conn().ExecContext(ctx,
		`INSERT INTO nodes (id, label, name, summary, synonyms, domain, subdomain,
		                    description, properties, tags, related_entities,
		                    confidence, provenance, source_refs, file_slug,
		                    created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		node.ID, string(node.Label), node.Name, node.Summary,
		nullString(node.Synonyms), node.Domain, node.Subdomain,
		nullString(node.Description), nullString(node.Properties),
		nullString(node.Tags), nullString(node.RelatedEntities),
		node.Confidence, string(node.Provenance),
		nullString(node.SourceRefs), nullString(node.FileSlug), node.CreatedAt, node.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("NodeRepo.Insert %s: %w", node.ID, err)
	}
	return nil
}

// InsertBatch 在一个事务内批量插入节点。
// 若任一条插入失败，整个事务将回滚。
//
// InsertBatch inserts multiple nodes within a single transaction.
// If any insert fails, the entire transaction is rolled back.
func (r *NodeRepo) InsertBatch(ctx context.Context, nodes []*dktypes.Node) error {
	if len(nodes) == 0 {
		return nil
	}

	r.db.mu.Lock()
	defer r.db.mu.Unlock()

	tx, err := r.db.Conn().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("NodeRepo.InsertBatch begin tx: %w", err)
	}
	defer tx.Rollback() // 无操作（若已提交）/ no-op if already committed

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO nodes (id, label, name, summary, synonyms, domain, subdomain,
		                    description, properties, tags, related_entities,
		                    confidence, provenance, source_refs, file_slug,
		                    created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("NodeRepo.InsertBatch prepare: %w", err)
	}
	defer stmt.Close()

	for _, node := range nodes {
		_, err := stmt.ExecContext(ctx,
			node.ID, string(node.Label), node.Name, node.Summary,
			nullString(node.Synonyms), node.Domain, node.Subdomain,
			nullString(node.Description), nullString(node.Properties),
			nullString(node.Tags), nullString(node.RelatedEntities),
			node.Confidence, string(node.Provenance),
			nullString(node.SourceRefs), nullString(node.FileSlug), node.CreatedAt, node.UpdatedAt,
		)
		if err != nil {
			return fmt.Errorf("NodeRepo.InsertBatch %s: %w", node.ID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("NodeRepo.InsertBatch commit: %w", err)
	}
	return nil
}

// RowID 返回节点的 SQLite rowid（FTS5 索引以 rowid 关联）。
// 增量流水线真删 node 前捕获 rowid，删除后用 FTSIndex.DeleteByRowID 清理索引残留
// （node 删除后 RebuildForSubdomain 的子查询已找不到该 rowid，必须显式清）。
// 节点不存在时返回 (0, nil)。
//
// RowID returns the node's SQLite rowid (FTS5 links rows by rowid). The
// incremental pipeline captures it before deleting a node so the stale FTS entry
// can be removed explicitly. (0, nil) when the node does not exist.
func (r *NodeRepo) RowID(ctx context.Context, id string) (int64, error) {
	r.db.mu.RLock()
	defer r.db.mu.RUnlock()

	var rowid int64
	err := r.db.Conn().QueryRowContext(ctx,
		`SELECT rowid FROM nodes WHERE id = ?`, id).Scan(&rowid)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("NodeRepo.RowID %s: %w", id, err)
	}
	return rowid, nil
}

// Delete 删除指定 ID 的单个节点，返回被删除的行数（0 表示节点本就不存在）。
// 调用方负责同步清理该节点的出入边（EdgeRepo.DeleteBySource / DeleteByTarget）——
// 本方法只删 nodes 行，绝不留下悬空边是调用方的职责（R5）。
// 供增量流水线「引用计数归零真删 node」使用；全量重建走删库，不用本方法。
//
// Delete removes a single node by ID, returning rows affected (0 = absent).
// The caller must clean up the node's incoming/outgoing edges (R5).
func (r *NodeRepo) Delete(ctx context.Context, id string) (int64, error) {
	r.db.mu.Lock()
	defer r.db.mu.Unlock()

	result, err := r.db.Conn().ExecContext(ctx,
		`DELETE FROM nodes WHERE id = ?`, id)
	if err != nil {
		return 0, fmt.Errorf("NodeRepo.Delete %s: %w", id, err)
	}
	return result.RowsAffected()
}

// DeleteBySubdomain 删除指定域和子域下的所有节点，返回被删除的行数。
//
// DeleteBySubdomain deletes all nodes under the specified domain and subdomain,
// returning the number of rows affected.
func (r *NodeRepo) DeleteBySubdomain(ctx context.Context, domain, subdomain string) (int64, error) {
	r.db.mu.Lock()
	defer r.db.mu.Unlock()

	result, err := r.db.Conn().ExecContext(ctx,
		`DELETE FROM nodes WHERE domain = ? AND subdomain = ?`, domain, subdomain)
	if err != nil {
		return 0, fmt.Errorf("NodeRepo.DeleteBySubdomain: %w", err)
	}
	return result.RowsAffected()
}

// Update 更新一条已有节点记录。匹配依据为节点的 ID 字段。
//
// Update updates an existing node record, matched by the node's ID field.
func (r *NodeRepo) Update(ctx context.Context, node *dktypes.Node) error {
	r.db.mu.Lock()
	defer r.db.mu.Unlock()

	result, err := r.db.Conn().ExecContext(ctx,
		`UPDATE nodes SET label = ?, name = ?, summary = ?, synonyms = ?,
		                  domain = ?, subdomain = ?, description = ?,
		                  properties = ?, tags = ?, related_entities = ?,
		                  confidence = ?, provenance = ?,
		                  source_refs = ?, updated_at = ?
		 WHERE id = ?`,
		string(node.Label), node.Name, node.Summary,
		nullString(node.Synonyms), node.Domain, node.Subdomain,
		nullString(node.Description), nullString(node.Properties),
		nullString(node.Tags), nullString(node.RelatedEntities),
		node.Confidence, string(node.Provenance),
		nullString(node.SourceRefs), node.UpdatedAt,
		node.ID,
	)
	if err != nil {
		return fmt.Errorf("NodeRepo.Update %s: %w", node.ID, err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("NodeRepo.Update: node %s not found", node.ID)
	}
	return nil
}

// SearchByNameSimilarity 通过名称相似度搜索节点。
// 使用 Levenshtein 距离（编辑距离）计算名称相似度，返回所有距离不超过 threshold 的节点。
// SQLite 没有内建的编辑距离函数，因此此方法先将所有节点加载到内存中再在 Go 层过滤。
//
// SearchByNameSimilarity searches for nodes by name similarity.
// Uses Levenshtein distance (edit distance) to compute name similarity,
// returning all nodes whose name distance is within the given threshold.
// Since SQLite lacks a built-in edit distance function, this method loads all
// nodes into memory and filters in Go.
func (r *NodeRepo) SearchByNameSimilarity(ctx context.Context, name string, threshold int) ([]*dktypes.Node, error) {
	if threshold < 0 {
		threshold = 0
	}

	r.db.mu.RLock()
	defer r.db.mu.RUnlock()

	rows, err := r.db.Conn().QueryContext(ctx,
		`SELECT `+nodeColumns+` FROM nodes`)
	if err != nil {
		return nil, fmt.Errorf("NodeRepo.SearchByNameSimilarity: %w", err)
	}
	defer rows.Close()

	allNodes, err := scanNodes(rows)
	if err != nil {
		return nil, err
	}

	// 在 Go 层按 Levenshtein 距离过滤 / Filter by Levenshtein distance in Go
	var results []*dktypes.Node
	for _, node := range allNodes {
		dist := levenshteinDistance(strings.ToLower(name), strings.ToLower(node.Name))
		if dist <= threshold {
			results = append(results, node)
		}
	}
	return results, nil
}

// ——————————————————————————————————————————————————————————————————————————————
// 内部辅助函数 / Internal helper functions
// ——————————————————————————————————————————————————————————————————————————————

// scanTargets 返回按 nodeColumns 顺序排列的扫描目标（无 visibility）。
// scanTargets returns scan destinations ordered to match nodeColumns (no visibility).
func scanTargets(n *dbNode) []interface{} {
	return []interface{}{
		&n.ID, &n.Label, &n.Name, &n.Summary, &n.Synonyms,
		&n.Domain, &n.Subdomain, &n.Description, &n.Properties,
		&n.Tags, &n.RelatedEntities, &n.Confidence,
		&n.Provenance, &n.SourceRefs, &n.FileSlug, &n.CreatedAt, &n.UpdatedAt,
	}
}

// scanNodeRow 将单行扫描进 dbNode。
// scanNodeRow scans a single row into a dbNode.
func scanNodeRow(row *sql.Row, n *dbNode) error {
	return row.Scan(scanTargets(n)...)
}

// scanNodes 从 sql.Rows 中扫描全部行为 []*dktypes.Node。
// scanNodes scans all rows from sql.Rows into a []*dktypes.Node slice.
func scanNodes(rows *sql.Rows) ([]*dktypes.Node, error) {
	var nodes []*dktypes.Node
	for rows.Next() {
		n := &dbNode{}
		if err := rows.Scan(scanTargets(n)...); err != nil {
			return nil, fmt.Errorf("scanNodes: %w", err)
		}
		nodes = append(nodes, n.ToPublic())
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scanNodes rows: %w", err)
	}
	return nodes, nil
}

// nullString 将空字符串映射为 nil（数据库 NULL），非空字符串映射为指针。
// nullString maps an empty string to nil (database NULL) and a non-empty string to a pointer.
func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// levenshteinDistance 计算两个字符串之间的 Levenshtein 编辑距离。
// 使用经典的动态规划实现，时间复杂度 O(m*n)，空间复杂度 O(min(m,n))。
//
// levenshteinDistance computes the Levenshtein edit distance between two strings.
// Uses the classic dynamic programming implementation with O(m*n) time
// and O(min(m,n)) space complexity.
func levenshteinDistance(a, b string) int {
	// 确保 a 是较短的字符串，以减少空间占用
	// Ensure a is the shorter string to minimize space usage
	if len(a) > len(b) {
		a, b = b, a
	}

	m, n := len(a), len(b)

	// 前一行和当前行 / Previous and current row
	prev := make([]int, m+1)
	cur := make([]int, m+1)

	for i := 0; i <= m; i++ {
		prev[i] = i
	}

	for j := 1; j <= n; j++ {
		cur[0] = j
		for i := 1; i <= m; i++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			// 取删除、插入、替换中的最小值 / min of delete, insert, substitute
			cur[i] = prev[i] + 1     // 删除 / delete
			if cur[i-1]+1 < cur[i] { // 插入 / insert
				cur[i] = cur[i-1] + 1
			}
			if prev[i-1]+cost < cur[i] { // 替换 / substitute
				cur[i] = prev[i-1] + cost
			}
		}
		prev, cur = cur, prev
	}
	return prev[m]
}
