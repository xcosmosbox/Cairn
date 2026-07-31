// Package storage 提供领域知识层的持久化存储层实现，包括数据库连接管理、
// 模式定义、数据迁移和 CRUD 仓储。
//
// 本文件包含 ConflictRepo 仓储，封装对 conflict_reports 表的 CRUD 操作。
// Package storage implements the persistence layer for the Cairn,
// including database connection management, schema definition, data migration,
// and CRUD repositories.
//
// This file contains the ConflictRepo repository for CRUD operations on the
// conflict_reports table.
package storage

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/xcosmosbox/cairn/core/dktypes"
)

// ConflictRepo 封装对 conflict_reports 表的 CRUD 操作。
// 冲突报告用于记录因不同来源对同名概念给出不一致定义而产生的冲突。
//
// ConflictRepo encapsulates CRUD operations on the conflict_reports table.
// Conflict reports record conflicts arising from inconsistent definitions
// of the same concept name by different sources.
type ConflictRepo struct {
	db *DB
}

// NewConflictRepo 创建新的 ConflictRepo 实例。
//
// NewConflictRepo creates a new ConflictRepo instance.
func NewConflictRepo(db *DB) *ConflictRepo {
	return &ConflictRepo{db: db}
}

// Insert 插入一条冲突报告记录。
//
// Insert inserts a conflict report record.
func (r *ConflictRepo) Insert(ctx context.Context, report *dktypes.ConflictReport) error {
	r.db.mu.Lock()
	defer r.db.mu.Unlock()

	_, err := r.db.Conn().ExecContext(ctx,
		`INSERT INTO conflict_reports (entity_name, conflicting_sources,
		                               resolution_status, resolution_note,
		                               resolved_by, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		report.EntityName, report.ConflictingSources,
		string(report.ResolutionStatus),
		nullString(report.ResolutionNote),
		nullString(report.ResolvedBy), report.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("ConflictRepo.Insert %s: %w", report.EntityName, err)
	}
	return nil
}

// ListUnresolved 列出所有未解决的冲突报告。
// 仅返回 resolution_status='unresolved' 的记录。
//
// ListUnresolved lists all unresolved conflict reports.
// Returns only records with resolution_status='unresolved'.
func (r *ConflictRepo) ListUnresolved(ctx context.Context) ([]*dktypes.ConflictReport, error) {
	r.db.mu.RLock()
	defer r.db.mu.RUnlock()

	rows, err := r.db.Conn().QueryContext(ctx,
		`SELECT id, entity_name, conflicting_sources, resolution_status,
		        resolution_note, resolved_by, created_at
		   FROM conflict_reports
		  WHERE resolution_status = 'unresolved'`)
	if err != nil {
		return nil, fmt.Errorf("ConflictRepo.ListUnresolved: %w", err)
	}
	defer rows.Close()

	return scanConflictReports(rows)
}

// Resolve 将指定冲突报告标记为已解决。
// 设置 resolution_status='resolved'，同时记录解决说明和解决者。
//
// Resolve marks the specified conflict report as resolved.
// Sets resolution_status='resolved' and records the resolution note and resolver.
func (r *ConflictRepo) Resolve(ctx context.Context, id int64, note, resolvedBy string) error {
	r.db.mu.Lock()
	defer r.db.mu.Unlock()

	result, err := r.db.Conn().ExecContext(ctx,
		`UPDATE conflict_reports
		    SET resolution_status = 'resolved',
		        resolution_note = ?,
		        resolved_by = ?
		  WHERE id = ?`,
		nullString(note), nullString(resolvedBy), id)
	if err != nil {
		return fmt.Errorf("ConflictRepo.Resolve %d: %w", id, err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("ConflictRepo.Resolve: conflict report %d not found", id)
	}
	return nil
}

// DeleteByEntity 删除与指定实体名称相关的所有冲突报告，返回被删除的行数。
//
// DeleteByEntity deletes all conflict reports related to the specified entity name,
// returning the number of rows affected.
func (r *ConflictRepo) DeleteByEntity(ctx context.Context, entityName string) (int64, error) {
	r.db.mu.Lock()
	defer r.db.mu.Unlock()

	result, err := r.db.Conn().ExecContext(ctx,
		`DELETE FROM conflict_reports WHERE entity_name = ?`, entityName)
	if err != nil {
		return 0, fmt.Errorf("ConflictRepo.DeleteByEntity %s: %w", entityName, err)
	}
	return result.RowsAffected()
}

// ——————————————————————————————————————————————————————————————————————————————
// 内部辅助函数 / Internal helper functions
// ——————————————————————————————————————————————————————————————————————————————

// scanConflictReports 从 sql.Rows 中扫描全部行为 []*dktypes.ConflictReport。
// scanConflictReports scans all rows from sql.Rows into a []*dktypes.ConflictReport slice.
func scanConflictReports(rows *sql.Rows) ([]*dktypes.ConflictReport, error) {
	var reports []*dktypes.ConflictReport
	for rows.Next() {
		var (
			id                 int64
			entityName         string
			conflictingSources string
			resolutionStatus   string
			resolutionNote     *string
			resolvedBy         *string
			createdAt          string
		)
		if err := rows.Scan(
			&id, &entityName, &conflictingSources,
			&resolutionStatus, &resolutionNote,
			&resolvedBy, &createdAt,
		); err != nil {
			return nil, fmt.Errorf("scanConflictReports: %w", err)
		}

		r := &dktypes.ConflictReport{
			ID:                 id,
			EntityName:         entityName,
			ConflictingSources: conflictingSources,
			ResolutionStatus:   dktypes.ResolutionStatus(resolutionStatus),
		}
		r.CreatedAt, _ = parseTime(createdAt)
		if resolutionNote != nil {
			r.ResolutionNote = *resolutionNote
		}
		if resolvedBy != nil {
			r.ResolvedBy = *resolvedBy
		}
		reports = append(reports, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scanConflictReports rows: %w", err)
	}
	return reports, nil
}
