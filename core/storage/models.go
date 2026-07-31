// Package storage 提供领域知识层的持久化存储层实现，包括数据库连接管理、
// 模式定义、数据迁移和 CRUD 仓储。
//
// 本文件包含数据库行映射结构体（dbNode、dbEdge）及其到公有类型
// （dktypes.Node、dktypes.Edge）的转换方法。这些结构体使用 *string 表示
// 可空的数据库列，在转换时处理 nil 与空字符串之间的差异。
// Package storage implements the persistence layer for the Domain Knowledge Layer,
// including database connection management, schema definition, data migration,
// and CRUD repositories.
//
// This file contains database row mapping structs (dbNode, dbEdge) and their
// conversion methods to public types (dktypes.Node, dktypes.Edge). These structs
// use *string for nullable database columns, handling the difference between nil
// and empty strings during conversion.
package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/xcosmosbox/domain-knowledge-layer/core/dktypes"
)

// dbNode 映射 nodes 表中的一行记录。
// 可空列使用 *string 指针类型，与 SQL NULL 语义保持一致。
//
// dbNode maps a row in the nodes table.
// Nullable columns use *string pointer types to align with SQL NULL semantics.
type dbNode struct {
	// ID 节点唯一标识符 / node unique identifier
	ID string `db:"id"`
	// Label 节点标签：Entity 或 Concept / node label: Entity or Concept
	Label string `db:"label"`
	// Name 节点可读名称 / human-readable node name
	Name string `db:"name"`
	// Summary 节点简短摘要 / short summary of the node
	Summary string `db:"summary"`
	// Synonyms 同义词列表（逗号分隔，可为 NULL）/ comma-separated synonyms (nullable)
	Synonyms *string `db:"synonyms"`
	// Domain 业务域 / business domain
	Domain string `db:"domain"`
	// Subdomain 业务子域 / business subdomain
	Subdomain string `db:"subdomain"`
	// Description 详细描述（可为 NULL）/ detailed description (nullable)
	Description *string `db:"description"`
	// Properties JSON 格式的附加属性（可为 NULL）/ additional properties in JSON (nullable)
	Properties *string `db:"properties"`
	// Tags 标签列表（逗号分隔，可为 NULL）/ comma-separated tags (nullable)
	Tags *string `db:"tags"`
	// RelatedEntities 关联实体 ID 列表（逗号分隔，可为 NULL）/ comma-separated related entity IDs (nullable)
	RelatedEntities *string `db:"related_entities"`
	// Confidence 置信度分数（0.0 ~ 1.0）/ confidence score (0.0 to 1.0)
	Confidence float64 `db:"confidence"`
	// Provenance 数据来源方式 / how the data was sourced
	Provenance string `db:"provenance"`
	// SourceRefs 引用来源列表（逗号分隔，可为 NULL）/ comma-separated source references (nullable)
	SourceRefs *string `db:"source_refs"`
	// CreatedAt 创建时间（SQLite TEXT，读取时解析）/ creation timestamp (SQLite TEXT, parsed on read)
	CreatedAt string `db:"created_at"`
	// UpdatedAt 最近更新时间（SQLite TEXT，读取时解析）/ last-updated timestamp (SQLite TEXT, parsed on read)
	UpdatedAt string `db:"updated_at"`
	// FileSlug shared 节点 primary 文件的可读 slug（可为 NULL）/ human-readable slug for shared primary file (nullable)
	FileSlug *string `db:"file_slug"`
}

// ToPublic 将 dbNode（数据库行）转换为公有类型 dktypes.Node。
// 处理 *string 到 string 的转换：nil 指针映射为空字符串。
//
// ToPublic converts a dbNode (database row) to the public type dktypes.Node.
// Handles *string to string conversion: nil pointers map to empty strings.
func (n *dbNode) ToPublic() *dktypes.Node {
	node := &dktypes.Node{
		ID:         n.ID,
		Label:      dktypes.Label(n.Label),
		Name:       n.Name,
		Summary:    n.Summary,
		Domain:     n.Domain,
		Subdomain:  n.Subdomain,
		Confidence: n.Confidence,
		Provenance: dktypes.Provenance(n.Provenance),
	}
	// 解析 SQLite TEXT 时间字段（尝试多种格式）
	// Parse SQLite TEXT time fields (try multiple formats)
	node.CreatedAt, _ = parseTime(n.CreatedAt)
	node.UpdatedAt, _ = parseTime(n.UpdatedAt)
	// 处理可空字段：nil → 空字符串 / Handle nullable fields: nil → empty string
	if n.Synonyms != nil {
		node.Synonyms = *n.Synonyms
	}
	if n.Description != nil {
		node.Description = *n.Description
	}
	if n.Properties != nil {
		node.Properties = *n.Properties
	}
	if n.Tags != nil {
		node.Tags = *n.Tags
	}
	if n.RelatedEntities != nil {
		node.RelatedEntities = *n.RelatedEntities
	}
	if n.SourceRefs != nil {
		node.SourceRefs = *n.SourceRefs
	}
	if n.FileSlug != nil {
		node.FileSlug = *n.FileSlug
	}
	return node
}

// dbEdge 映射 edges 表中的一行记录。
// 可空列使用 *string 指针类型，与 SQL NULL 语义保持一致。
//
// dbEdge maps a row in the edges table.
// Nullable columns use *string pointer types to align with SQL NULL semantics.
type dbEdge struct {
	// ID 自增主键 / auto-increment primary key
	ID int64 `db:"id"`
	// SourceID 边起点的节点 ID / node ID at the source of the edge
	SourceID string `db:"source_id"`
	// TargetID 边终点的节点 ID / node ID at the target of the edge
	TargetID string `db:"target_id"`
	// Kind 语义关系类型 / semantic relationship type
	Kind string `db:"kind"`
	// Description 关系描述（可为 NULL）/ description of the relationship (nullable)
	Description *string `db:"description"`
	// Properties JSON 格式的附加属性（可为 NULL）/ additional properties in JSON (nullable)
	Properties *string `db:"properties"`
	// Provenance 数据来源方式 / how the data was sourced
	Provenance string `db:"provenance"`
	// Confidence 置信度分数（0.0 ~ 1.0）/ confidence score (0.0 to 1.0)
	Confidence float64 `db:"confidence"`
	// SourceRefs 引用来源列表（逗号分隔，可为 NULL）/ comma-separated source references (nullable)
	SourceRefs *string `db:"source_refs"`
	// Bidirectional 是否为双向关系（1 = 是，0 = 否）/ whether bidirectional (1 = yes, 0 = no)
	Bidirectional int `db:"bidirectional"`
	// Cardinality 关系基数（可为 NULL，如 "1:1", "1:N", "M:N"）/ cardinality (nullable, e.g., "1:1", "1:N", "M:N")
	Cardinality *string `db:"cardinality"`
	// CreatedAt 创建时间（SQLite TEXT，读取时解析）/ creation timestamp (SQLite TEXT, parsed on read)
	CreatedAt string `db:"created_at"`
}

// parseTime 解析 SQLite TEXT 时间字段，尝试多种格式。
// parseTime parses a SQLite TEXT time field, trying multiple formats.
func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	// 尝试 SQLite datetime() 格式 / Try SQLite datetime() format
	if t, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
		return t, nil
	}
	// 尝试 RFC3339 格式 / Try RFC3339 format
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	// 尝试 RFC3339Nano 格式 / Try RFC3339Nano format
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("parseTime: unrecognized format: %s", s)
}

// ToPublic 将 dbEdge（数据库行）转换为公有类型 dktypes.Edge。
// 处理 *string 到 string 的转换：nil 指针映射为空字符串。
//
// ToPublic converts a dbEdge (database row) to the public type dktypes.Edge.
// Handles *string to string conversion: nil pointers map to empty strings.
func (e *dbEdge) ToPublic() *dktypes.Edge {
	edge := &dktypes.Edge{
		ID:            e.ID,
		SourceID:      e.SourceID,
		TargetID:      e.TargetID,
		Kind:          dktypes.RelationKind(e.Kind),
		Provenance:    dktypes.Provenance(e.Provenance),
		Confidence:    e.Confidence,
		Bidirectional: e.Bidirectional,
	}
	// 解析 SQLite TEXT 时间字段 / Parse SQLite TEXT time field
	edge.CreatedAt, _ = parseTime(e.CreatedAt)
	// 处理可空字段：nil → 空字符串 / Handle nullable fields: nil → empty string
	if e.Description != nil {
		edge.Description = *e.Description
	}
	if e.Properties != nil {
		edge.Properties = *e.Properties
	}
	if e.SourceRefs != nil {
		edge.SourceRefs = *e.SourceRefs
	}
	if e.Cardinality != nil {
		edge.Cardinality = *e.Cardinality
	}
	return edge
}

// 确保 context 包已导入（供后续仓储层使用）
// Ensure context package is imported (for future repository layer use)
var _ context.Context
