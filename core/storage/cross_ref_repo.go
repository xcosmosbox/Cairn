// Package storage 提供领域知识层的持久化存储层实现，包括数据库连接管理、
// 模式定义、数据迁移和 CRUD 仓储。
//
// 本文件包含 CrossRefRepo 仓储，封装对 cross_references 表的 CRUD 操作，
// 以及跨知识图谱（cross-KG）链接的检测和查询。
// Package storage implements the persistence layer for the Cairn,
// including database connection management, schema definition, data migration,
// and CRUD repositories.
//
// This file contains the CrossRefRepo repository for CRUD operations on the
// cross_references table, plus cross-KG link detection and queries.
package storage

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/xcosmosbox/cairn/core/dktypes"
)

// CrossRefRepo 封装对 cross_references 表的 CRUD 操作。
// 交叉引用用于记录从当前 Skill 指向其他 Skill 中实体的跨域关系。
//
// CrossRefRepo encapsulates CRUD operations on the cross_references table.
// Cross-references record cross-skill relationships from the current skill
// to entities in other skills.
type CrossRefRepo struct {
	db *DB
}

// NewCrossRefRepo 创建新的 CrossRefRepo 实例。
//
// NewCrossRefRepo creates a new CrossRefRepo instance.
func NewCrossRefRepo(db *DB) *CrossRefRepo {
	return &CrossRefRepo{db: db}
}

// Insert 插入一条交叉引用记录。
// 在插入前尝试解析 target_node_id：通过查询 nodes 表以匹配 target_entity 对应的节点。
// 若找到匹配节点则自动填充 target_node_id；否则标记为 external_kg（外部知识图谱链接）。
//
// Insert inserts a cross-reference record.
// Before inserting, attempts to resolve the target_node_id by querying the nodes
// table for a node matching the target_entity. If a matching node is found,
// target_node_id is populated automatically; otherwise it is marked as external_kg
// (external knowledge graph link).
func (r *CrossRefRepo) Insert(ctx context.Context, ref *dktypes.CrossReference) error {
	r.db.mu.Lock()
	defer r.db.mu.Unlock()

	// 尝试解析 target_node_id 并设置解析状态
	// Attempt to resolve target_node_id and set resolution status
	if ref.TargetNodeID == "" {
		resolvedID, err := r.lookupNodeID(ctx, ref.TargetEntity)
		if err == nil && resolvedID != "" {
			ref.TargetNodeID = resolvedID
		}
	}
	// 根据 target_node_id 是否已解析设置状态
	// 未解析的目标标记为 external_kg 表示需要从外部知识图谱查找
	// Set status based on whether target_node_id has been resolved
	// Unresolved targets are marked as external_kg to indicate lookup needed in external KGs
	if ref.TargetNodeID != "" {
		ref.ResolutionStatus = dktypes.ResolutionResolved
	} else {
		ref.ResolutionStatus = dktypes.ResolutionExternalKG
	}

	_, err := r.db.Conn().ExecContext(ctx,
		`INSERT INTO cross_references (source_entity, target_entity, target_node_id,
		                               kind, reason, resolution_status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ref.SourceEntity, ref.TargetEntity, nullString(ref.TargetNodeID),
		string(ref.Kind), nullString(ref.Reason),
		string(ref.ResolutionStatus), ref.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("CrossRefRepo.Insert %s->%s: %w", ref.SourceEntity, ref.TargetEntity, err)
	}
	return nil
}

// lookupNodeID 在持有写锁的情况下查找与给定实体名称匹配的节点 ID。
// 查找策略：精确匹配 name 或 id，或匹配 synonyms 中包含该实体名。
//
// lookupNodeID looks up a node ID matching the given entity name while holding the write lock.
// Strategy: exact match on name or id, or match on synonyms containing the entity name.
func (r *CrossRefRepo) lookupNodeID(ctx context.Context, entityName string) (string, error) {
	// 优先尝试按 name 精确匹配 / First try exact match by name
	var nodeID string
	err := r.db.Conn().QueryRowContext(ctx,
		`SELECT id FROM nodes WHERE name = ? LIMIT 1`, entityName).Scan(&nodeID)
	if err == nil {
		return nodeID, nil
	}
	if err != sql.ErrNoRows {
		return "", err
	}

	// 尝试按 id 精确匹配 / Try exact match by id
	err = r.db.Conn().QueryRowContext(ctx,
		`SELECT id FROM nodes WHERE id = ? LIMIT 1`, entityName).Scan(&nodeID)
	if err == nil {
		return nodeID, nil
	}
	if err != sql.ErrNoRows {
		return "", err
	}

	// 尝试按 synonyms 包含匹配 / Try match by synonyms containing the entity name
	err = r.db.Conn().QueryRowContext(ctx,
		`SELECT id FROM nodes WHERE synonyms LIKE ? LIMIT 1`,
		"%"+entityName+"%").Scan(&nodeID)
	if err == sql.ErrNoRows {
		return "", nil // 未找到 / not found
	}
	if err != nil {
		return "", err
	}
	return nodeID, nil
}

// ListUnresolved 列出所有尚未解析的交叉引用（target_node_id 为空的记录）。
//
// ListUnresolved lists all cross-references that have not yet been resolved
// (records with empty target_node_id).
func (r *CrossRefRepo) ListUnresolved(ctx context.Context) ([]*dktypes.CrossReference, error) {
	r.db.mu.RLock()
	defer r.db.mu.RUnlock()

	rows, err := r.db.Conn().QueryContext(ctx,
		`SELECT id, source_entity, target_entity, target_node_id, kind, reason,
		        resolution_status, created_at
		   FROM cross_references
		  WHERE target_node_id IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("CrossRefRepo.ListUnresolved: %w", err)
	}
	defer rows.Close()

	return scanCrossRefs(rows)
}

// ListCrossKGLinks 列出所有标记为 external_kg 的交叉引用，返回 CrossKGLink 列表。
// 这些链接表示目标实体在当前 KG 中无法解析，构成跨知识图谱的外部链接关系。
// 每条 CrossKGLink 包含源实体、目标实体、关系类型及原因说明。
//
// ListCrossKGLinks lists all cross-references marked as external_kg, returning
// them as a CrossKGLink slice. These links represent entities that cannot be
// resolved in the current KG, forming cross-knowledge-graph external links.
// Each CrossKGLink contains the source entity, target entity, relationship kind,
// and reason.
func (r *CrossRefRepo) ListCrossKGLinks(ctx context.Context) ([]dktypes.CrossKGLink, error) {
	r.db.mu.RLock()
	defer r.db.mu.RUnlock()

	rows, err := r.db.Conn().QueryContext(ctx,
		`SELECT source_entity, target_entity, kind, reason, created_at
		   FROM cross_references
		  WHERE resolution_status = 'external_kg'
		  ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("CrossRefRepo.ListCrossKGLinks: %w", err)
	}
	defer rows.Close()

	var links []dktypes.CrossKGLink
	for rows.Next() {
		var (
			sourceEntity string
			targetEntity string
			kind         string
			reason       *string
			createdAt    string
		)
		if err := rows.Scan(&sourceEntity, &targetEntity, &kind, &reason, &createdAt); err != nil {
			return nil, fmt.Errorf("CrossRefRepo.ListCrossKGLinks scan: %w", err)
		}

		link := dktypes.CrossKGLink{
			FromEntity: sourceEntity,
			ToEntity:   targetEntity,
			Kind:       dktypes.RelationKind(kind),
		}
		if reason != nil {
			link.Reason = *reason
		}
		// 解析创建时间 / Parse creation time
		if t, err := parseTime(createdAt); err == nil {
			link.CreatedAt = t
		}

		links = append(links, link)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("CrossRefRepo.ListCrossKGLinks rows: %w", err)
	}
	return links, nil
}

// ListBySource 列出指定源实体的所有交叉引用。
//
// ListBySource lists all cross-references originating from the specified source entity.
func (r *CrossRefRepo) ListBySource(ctx context.Context, sourceEntity string) ([]*dktypes.CrossReference, error) {
	r.db.mu.RLock()
	defer r.db.mu.RUnlock()

	rows, err := r.db.Conn().QueryContext(ctx,
		`SELECT id, source_entity, target_entity, target_node_id, kind, reason,
		        resolution_status, created_at
		   FROM cross_references
		  WHERE source_entity = ?`, sourceEntity)
	if err != nil {
		return nil, fmt.Errorf("CrossRefRepo.ListBySource %s: %w", sourceEntity, err)
	}
	defer rows.Close()

	return scanCrossRefs(rows)
}

// DeleteByTargetEntity 删除目标实体匹配的所有交叉引用，返回被删除的行数。
//
// DeleteByTargetEntity deletes all cross-references matching the target entity,
// returning the number of rows affected.
func (r *CrossRefRepo) DeleteByTargetEntity(ctx context.Context, targetEntity string) (int64, error) {
	r.db.mu.Lock()
	defer r.db.mu.Unlock()

	result, err := r.db.Conn().ExecContext(ctx,
		`DELETE FROM cross_references WHERE target_entity = ?`, targetEntity)
	if err != nil {
		return 0, fmt.Errorf("CrossRefRepo.DeleteByTargetEntity %s: %w", targetEntity, err)
	}
	return result.RowsAffected()
}

// ——————————————————————————————————————————————————————————————————————————————
// 内部辅助函数 / Internal helper functions
// ——————————————————————————————————————————————————————————————————————————————

// scanCrossRefs 从 sql.Rows 中扫描全部行为 []*dktypes.CrossReference。
// scanCrossRefs scans all rows from sql.Rows into a []*dktypes.CrossReference slice.
func scanCrossRefs(rows *sql.Rows) ([]*dktypes.CrossReference, error) {
	var refs []*dktypes.CrossReference
	for rows.Next() {
		var (
			id               int64
			sourceEntity     string
			targetEntity     string
			targetNodeID     *string
			kind             string
			reason           *string
			resolutionStatus string
			createdAt        string
		)
		if err := rows.Scan(
			&id, &sourceEntity, &targetEntity, &targetNodeID,
			&kind, &reason, &resolutionStatus, &createdAt,
		); err != nil {
			return nil, fmt.Errorf("scanCrossRefs: %w", err)
		}

		ref := &dktypes.CrossReference{
			ID:               id,
			SourceEntity:     sourceEntity,
			TargetEntity:     targetEntity,
			Kind:             dktypes.RelationKind(kind),
			ResolutionStatus: dktypes.ResolutionStatus(resolutionStatus),
		}
		ref.CreatedAt, _ = parseTime(createdAt)
		if targetNodeID != nil {
			ref.TargetNodeID = *targetNodeID
		}
		if reason != nil {
			ref.Reason = *reason
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scanCrossRefs rows: %w", err)
	}
	return refs, nil
}
