// Package storage 提供领域知识层的持久化存储层实现。
//
// 本文件包含 NodeSourceRepo 仓储，提供 node_sources 表的写入与查询操作。
// node_sources 持久化 node ↔ 来源文档映射（每个 node 的每个 member 对应一行），
// 是回写锚定文档、shared 引用计数（distinct file_path 归零才软删 node）、
// 以及审计的命脉。
//
// 表结构（schema.go）：
//
//	CREATE TABLE node_sources (
//	    node_uuid   TEXT NOT NULL,
//	    member_id   TEXT NOT NULL,
//	    skill       TEXT NOT NULL,
//	    file_path   TEXT NOT NULL,
//	    start_line  INTEGER,   -- 可空（span 缺失时仅退化排序，不阻塞）
//	    end_line    INTEGER,   -- 可空
//	    PRIMARY KEY (node_uuid, member_id, file_path)
//	);
//
// 本批只实现写入（InsertBatch）与回写/审计所需的基础查询；
// 增量批次的软删/引用计数查询（ListByNode / ListByFile）一并提供以便后续复用。
//
// Package storage implements the persistence layer for the Domain Knowledge Layer.
// This file contains the NodeSourceRepo repository, providing write and query
// operations on the node_sources table — the lifeline of write-back anchoring,
// shared reference counting, and audit.
package storage

import (
	"context"
	"database/sql"
	"fmt"
)

// NodeSource 对应 node_sources 表的一行：node 的某个 member 在来源文档中的位置。
// start_line/end_line 可为 0（表示 span 缺失，仅退化排序，不阻塞回写）。
//
// NodeSource maps one row of node_sources: where a member of a node lives in
// its source document. start_line/end_line may be 0 (span absent; write-back
// degrades ordering only, never blocks).
type NodeSource struct {
	NodeUUID  string // 对应 nodes.id（entity/concept 的 UUID 主键）/ node UUID PK
	MemberID  string // 04 标注单元 id（extract.AssignIDs 生成）/ 04 annotated-unit id
	Skill     string // 来源 skill 名 / source skill name
	FilePath  string // 来源文档相对仓库根的路径 / source doc path relative to repo root
	StartLine int    // 1-based 闭区间起；0 表示无 span / 1-based inclusive start; 0 = no span
	EndLine   int    // 1-based 闭区间止；0 表示无 span / 1-based inclusive end; 0 = no span
}

// NodeSourceRepo 封装对 node_sources 表的所有 CRUD 操作。
// 所有方法均通过嵌入的 *DB 访问数据库，并使用互斥锁保证线程安全。
//
// NodeSourceRepo encapsulates all CRUD operations on the node_sources table.
type NodeSourceRepo struct {
	db *DB
}

// NewNodeSourceRepo 创建新的 NodeSourceRepo 实例。
//
// NewNodeSourceRepo creates a new NodeSourceRepo instance.
func NewNodeSourceRepo(db *DB) *NodeSourceRepo {
	return &NodeSourceRepo{db: db}
}

// InsertBatch 在一个事务内批量插入 node_sources 行。
// start_line/end_line 为 0 时写入 NULL（与 schema 的可空语义一致）。
// 主键冲突（同一 node_uuid+member_id+file_path）时用 INSERT OR REPLACE 覆盖，
// 保证全量重建场景下重复写入幂等（防御 ingest 对同一 member 多来源同文档的去重）。
//
// InsertBatch inserts multiple node_sources rows within a single transaction.
// start_line/end_line = 0 are written as NULL. Uses INSERT OR REPLACE so that
// duplicate (node_uuid, member_id, file_path) rows are idempotent.
func (r *NodeSourceRepo) InsertBatch(ctx context.Context, rows []NodeSource) error {
	if len(rows) == 0 {
		return nil
	}

	r.db.mu.Lock()
	defer r.db.mu.Unlock()

	tx, err := r.db.Conn().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("NodeSourceRepo.InsertBatch begin tx: %w", err)
	}
	defer tx.Rollback() // 无操作（若已提交）/ no-op if already committed

	stmt, err := tx.PrepareContext(ctx,
		`INSERT OR REPLACE INTO node_sources (node_uuid, member_id, skill, file_path, start_line, end_line)
		 VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("NodeSourceRepo.InsertBatch prepare: %w", err)
	}
	defer stmt.Close()

	for _, row := range rows {
		var startLine, endLine any
		if row.StartLine > 0 {
			startLine = row.StartLine
		} // 否则 nil → NULL / else nil → NULL
		if row.EndLine > 0 {
			endLine = row.EndLine
		}
		if _, err := stmt.ExecContext(ctx,
			row.NodeUUID, row.MemberID, row.Skill, row.FilePath, startLine, endLine,
		); err != nil {
			return fmt.Errorf("NodeSourceRepo.InsertBatch (%s/%s/%s): %w",
				row.NodeUUID, row.MemberID, row.FilePath, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("NodeSourceRepo.InsertBatch commit: %w", err)
	}
	return nil
}

// ListByNode 返回某 node 的全部来源行（用于 shared 判定 / 审计）。
//
// ListByNode returns all source rows for a given node uuid.
func (r *NodeSourceRepo) ListByNode(ctx context.Context, nodeUUID string) ([]NodeSource, error) {
	r.db.mu.RLock()
	defer r.db.mu.RUnlock()

	rows, err := r.db.Conn().QueryContext(ctx,
		`SELECT node_uuid, member_id, skill, file_path, start_line, end_line
		   FROM node_sources
		  WHERE node_uuid = ?
		  ORDER BY member_id, file_path`, nodeUUID)
	if err != nil {
		return nil, fmt.Errorf("NodeSourceRepo.ListByNode %s: %w", nodeUUID, err)
	}
	defer rows.Close()
	return scanNodeSources(rows)
}

// ListByFile 返回引用了某文档的全部 node 来源行（用于回写按文档分组 / 软删时反查）。
//
// ListByFile returns all source rows referencing a given file path.
func (r *NodeSourceRepo) ListByFile(ctx context.Context, filePath string) ([]NodeSource, error) {
	r.db.mu.RLock()
	defer r.db.mu.RUnlock()

	rows, err := r.db.Conn().QueryContext(ctx,
		`SELECT node_uuid, member_id, skill, file_path, start_line, end_line
		   FROM node_sources
		  WHERE file_path = ?
		  ORDER BY node_uuid, member_id`, filePath)
	if err != nil {
		return nil, fmt.Errorf("NodeSourceRepo.ListByFile %s: %w", filePath, err)
	}
	defer rows.Close()
	return scanNodeSources(rows)
}

// ListAll 返回 node_sources 表全部行（测试与审计用）。
//
// ListAll returns all rows in node_sources (tests / audit).
func (r *NodeSourceRepo) ListAll(ctx context.Context) ([]NodeSource, error) {
	r.db.mu.RLock()
	defer r.db.mu.RUnlock()

	rows, err := r.db.Conn().QueryContext(ctx,
		`SELECT node_uuid, member_id, skill, file_path, start_line, end_line
		   FROM node_sources
		  ORDER BY node_uuid, member_id, file_path`)
	if err != nil {
		return nil, fmt.Errorf("NodeSourceRepo.ListAll: %w", err)
	}
	defer rows.Close()
	return scanNodeSources(rows)
}

// DeleteByNodeAndFile 删除某 node 在指定文档的全部来源行，返回被删除的行数。
// 增量流水线「引用计数软删」的第一步：先删 (node, 文档) 来源贡献，
// 再用 CountDistinctFilesByNode 判该 node 是否还有其他文档贡献（>0 存活，==0 真删）。
//
// DeleteByNodeAndFile removes all source rows of a node contributed by one
// document — step one of the incremental reference-counted soft delete.
func (r *NodeSourceRepo) DeleteByNodeAndFile(ctx context.Context, nodeUUID, filePath string) (int64, error) {
	r.db.mu.Lock()
	defer r.db.mu.Unlock()

	result, err := r.db.Conn().ExecContext(ctx,
		`DELETE FROM node_sources WHERE node_uuid = ? AND file_path = ?`, nodeUUID, filePath)
	if err != nil {
		return 0, fmt.Errorf("NodeSourceRepo.DeleteByNodeAndFile (%s/%s): %w", nodeUUID, filePath, err)
	}
	return result.RowsAffected()
}

// DeleteByNode 删除某 node 的全部来源行，返回被删除的行数。
// 仅在 node 真删（引用计数归零）时调用，与 NodeRepo.Delete 配对。
//
// DeleteByNode removes all source rows of a node; used when the node itself
// is deleted (reference count reached zero).
func (r *NodeSourceRepo) DeleteByNode(ctx context.Context, nodeUUID string) (int64, error) {
	r.db.mu.Lock()
	defer r.db.mu.Unlock()

	result, err := r.db.Conn().ExecContext(ctx,
		`DELETE FROM node_sources WHERE node_uuid = ?`, nodeUUID)
	if err != nil {
		return 0, fmt.Errorf("NodeSourceRepo.DeleteByNode %s: %w", nodeUUID, err)
	}
	return result.RowsAffected()
}

// CountDistinctFilesByNode 返回某 node 当前剩余的不同来源文档数（引用计数）。
// 软删判定：>0 表示仍有其他文档贡献该 node（存活）；==0 表示无任何来源（真删）。
//
// CountDistinctFilesByNode returns the number of distinct source documents
// still contributing to a node (the reference count for soft delete).
func (r *NodeSourceRepo) CountDistinctFilesByNode(ctx context.Context, nodeUUID string) (int, error) {
	r.db.mu.RLock()
	defer r.db.mu.RUnlock()

	var n int
	err := r.db.Conn().QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT file_path) FROM node_sources WHERE node_uuid = ?`, nodeUUID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("NodeSourceRepo.CountDistinctFilesByNode %s: %w", nodeUUID, err)
	}
	return n, nil
}

// DistinctSkillsByDomain 返回「当前仍贡献某 domain」的 distinct skill 列表
// （以 node_sources 为来源权威：JOIN nodes 限定 domain，已删 node 自然排除）。
// 增量流水线按「该 skill 是否仍通过任一存活 node 贡献该 domain」精确同步
// skill→domain provides 与 node/domain source_refs（问题 9）。
//
// DistinctSkillsByDomain returns the distinct skills currently contributing to
// a domain, with node_sources as the source of truth (deleted nodes excluded).
func (r *NodeSourceRepo) DistinctSkillsByDomain(ctx context.Context, domain string) ([]string, error) {
	r.db.mu.RLock()
	defer r.db.mu.RUnlock()

	rows, err := r.db.Conn().QueryContext(ctx,
		`SELECT DISTINCT s.skill FROM node_sources s
		   JOIN nodes n ON n.id = s.node_uuid
		  WHERE n.domain = ?
		  ORDER BY s.skill`, domain)
	if err != nil {
		return nil, fmt.Errorf("NodeSourceRepo.DistinctSkillsByDomain %s: %w", domain, err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var skill string
		if err := rows.Scan(&skill); err != nil {
			return nil, fmt.Errorf("NodeSourceRepo.DistinctSkillsByDomain scan: %w", err)
		}
		out = append(out, skill)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("NodeSourceRepo.DistinctSkillsByDomain rows: %w", err)
	}
	return out, nil
}

// DistinctSkillsByNode 返回某 node 当前全部来源行的 distinct skill 列表
// （更新 node.source_refs 用，问题 9）。
//
// DistinctSkillsByNode returns the distinct skills across one node's source rows.
func (r *NodeSourceRepo) DistinctSkillsByNode(ctx context.Context, nodeUUID string) ([]string, error) {
	r.db.mu.RLock()
	defer r.db.mu.RUnlock()

	rows, err := r.db.Conn().QueryContext(ctx,
		`SELECT DISTINCT skill FROM node_sources WHERE node_uuid = ? ORDER BY skill`, nodeUUID)
	if err != nil {
		return nil, fmt.Errorf("NodeSourceRepo.DistinctSkillsByNode %s: %w", nodeUUID, err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var skill string
		if err := rows.Scan(&skill); err != nil {
			return nil, fmt.Errorf("NodeSourceRepo.DistinctSkillsByNode scan: %w", err)
		}
		out = append(out, skill)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("NodeSourceRepo.DistinctSkillsByNode rows: %w", err)
	}
	return out, nil
}

// scanNodeSources 从 sql.Rows 扫描全部行为 []NodeSource。
// scanNodeSources scans all rows into a []NodeSource slice.
func scanNodeSources(rows *sql.Rows) ([]NodeSource, error) {
	var out []NodeSource
	for rows.Next() {
		var ns NodeSource
		var startLine, endLine sql.NullInt64
		if err := rows.Scan(
			&ns.NodeUUID, &ns.MemberID, &ns.Skill, &ns.FilePath,
			&startLine, &endLine,
		); err != nil {
			return nil, fmt.Errorf("scanNodeSources: %w", err)
		}
		if startLine.Valid {
			ns.StartLine = int(startLine.Int64)
		}
		if endLine.Valid {
			ns.EndLine = int(endLine.Int64)
		}
		out = append(out, ns)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scanNodeSources rows: %w", err)
	}
	return out, nil
}
