// Package storage 提供领域知识层的持久化存储层实现，包括数据库连接管理、
// 模式定义、数据迁移和 CRUD 仓储。
//
// 本文件包含所有 DDL（数据定义语言）函数：建表语句、索引创建语句和表名列表。
// Package storage implements the persistence layer for the Cairn,
// including database connection management, schema definition, data migration,
// and CRUD repositories.
//
// This file contains all DDL (Data Definition Language) functions: table creation
// statements, index creation statements, and table name listing.
package storage

// CreateTablesSQL 返回按依赖顺序排列的所有 CREATE TABLE 语句。
// 创建顺序至关重要：先创建被引用的表（file_states, kb_version, nodes），
// 再创建引用它们的表（edges, cross_references, conflict_reports），
// 接着创建元数据存储表（kg_manifest），最后创建虚拟 FTS 表（nodes_fts）。
//
// CreateTablesSQL returns all CREATE TABLE statements in dependency order.
// The creation order is critical: referenced tables (file_states, kb_version, nodes)
// are created first, then tables that reference them (edges, cross_references,
// conflict_reports), then metadata storage (kg_manifest), and finally the
// virtual FTS table (nodes_fts).
func CreateTablesSQL() []string {
	return []string{
		// file_states — 跟踪已索引文件的哈希值，用于增量更新
		// 使用 (repo_url, file_path) 复合主键，防止同一文件在不同仓库中冲突
		// file_states — tracks hashes of indexed files for incremental updates
		// Uses (repo_url, file_path) composite primary key to avoid conflicts
		// when the same file path exists in different repositories
		`CREATE TABLE IF NOT EXISTS file_states (
    repo_url     TEXT NOT NULL DEFAULT '',
    file_path    TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    indexed_at   TEXT DEFAULT (datetime('now')),
    PRIMARY KEY (repo_url, file_path)
);`,

		// kb_version — 存储知识库的当前版本号
		// kb_version — stores the current version of the knowledge base
		`CREATE TABLE IF NOT EXISTS kb_version (
    version    TEXT PRIMARY KEY,
    updated_at TEXT DEFAULT (datetime('now'))
);`,

		// nodes — 知识图谱节点表（一等节点：skill/domain/subdomain/entity/concept）
		// nodes — knowledge graph node table (first-class nodes: skill/domain/subdomain/entity/concept)
		`CREATE TABLE IF NOT EXISTS nodes (
    id               TEXT PRIMARY KEY,
    label            TEXT NOT NULL CHECK(label IN ('Entity', 'Concept', 'Skill', 'Domain', 'Subdomain')),
    name             TEXT NOT NULL,
    summary          TEXT NOT NULL,
    synonyms         TEXT,
    domain           TEXT NOT NULL,
    subdomain        TEXT NOT NULL,
    description      TEXT,
    properties       TEXT,
    tags             TEXT,
    related_entities TEXT,
    confidence       REAL NOT NULL DEFAULT 1.0 CHECK(confidence >= 0.0 AND confidence <= 1.0),
    provenance       TEXT NOT NULL DEFAULT 'llm_inferred' CHECK(provenance IN ('llm_inferred', 'human_curated', 'extraction')),
    source_refs      TEXT,
    file_slug        TEXT,
    created_at       TEXT DEFAULT (datetime('now')),
    updated_at       TEXT DEFAULT (datetime('now'))
);`,

		// edges — 知识图谱边表（节点间关系）
		// edges — knowledge graph edge table (relationships between nodes)
		`CREATE TABLE IF NOT EXISTS edges (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    source_id     TEXT NOT NULL,
    target_id     TEXT NOT NULL,
    kind          TEXT NOT NULL CHECK(kind IN ('triggers', 'depends_on', 'references', 'generalizes', 'composes', 'contradicts', 'provides')),
    description   TEXT,
    properties    TEXT,
    provenance    TEXT NOT NULL DEFAULT 'llm_inferred' CHECK(provenance IN ('llm_inferred', 'human_curated', 'extraction')),
    confidence    REAL NOT NULL DEFAULT 1.0 CHECK(confidence >= 0.0 AND confidence <= 1.0),
    source_refs   TEXT,
    bidirectional INTEGER DEFAULT 0 CHECK(bidirectional IN (0, 1)),
    cardinality   TEXT,
    created_at    TEXT DEFAULT (datetime('now')),
    FOREIGN KEY (source_id) REFERENCES nodes(id),
    FOREIGN KEY (target_id) REFERENCES nodes(id)
);`,

		// cross_references — 跨 Skill 引用表
		// resolution_status 新增 'external_kg' 状态：表示目标实体在当前 KG 中无法解析，
		// 需要从外部知识图谱中查找
		// cross_references — cross-skill reference table
		// resolution_status includes 'external_kg' state: indicates the target entity
		// cannot be resolved in the current KG and should be looked up in an external KG
		`CREATE TABLE IF NOT EXISTS cross_references (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    source_entity     TEXT NOT NULL,
    target_entity     TEXT NOT NULL,
    target_node_id    TEXT,
    kind              TEXT NOT NULL,
    reason            TEXT,
    resolution_status TEXT NOT NULL DEFAULT 'resolved' CHECK(resolution_status IN ('resolved', 'unresolved', 'external_kg')),
    created_at        TEXT DEFAULT (datetime('now'))
);`,

		// conflict_reports — 同名概念冲突报告表
		// conflict_reports — name conflict report table
		`CREATE TABLE IF NOT EXISTS conflict_reports (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    entity_name         TEXT NOT NULL,
    conflicting_sources TEXT NOT NULL,
    resolution_status   TEXT NOT NULL DEFAULT 'unresolved' CHECK(resolution_status IN ('resolved', 'unresolved')),
    resolution_note     TEXT,
    resolved_by         TEXT,
    created_at          TEXT DEFAULT (datetime('now'))
);`,

		// kg_manifest — 知识图谱清单存储表（键值对，value 为 JSON）
		// 用于持久化存储 BuildForGroup 生成的清单数据，支持跨会话查询
		// kg_manifest — knowledge graph manifest storage table (key-value, value is JSON)
		// Persists manifest data generated by BuildForGroup for cross-session queries
		`CREATE TABLE IF NOT EXISTS kg_manifest (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL  -- JSON string
);`,

		// nodes_fts — 节点全文搜索虚拟表（FTS5）
		// nodes_fts — node full-text search virtual table (FTS5)
		`CREATE VIRTUAL TABLE IF NOT EXISTS nodes_fts USING fts5(
    name, summary, synonyms, tags, description, domain, subdomain,
    prefix='2 3'
);`,

		// node_sources — 持久化 node ↔ 来源文档映射（回写与 shared 判定的命脉）。
		// 每个 node 的每个 member 对应一行，记录该 member 所属 skill、文件路径与原文行区间，
		// 供回写锚定文档、shared 引用计数（distinct file_path 归零才软删 node）、以及审计。
		// 主键 (node_uuid, member_id, file_path) 保证同一 node 同一 member 在同一文件唯一。
		// start_line/end_line 可空（span 缺失时仅退化排序，不阻塞）。
		//
		// node_sources — persists the node ↔ source-document mapping (the lifeline of
		// write-back and shared ownership). Each row ties one member of a node to its
		// skill/file/span, anchoring write-back, shared reference counting (a node is
		// soft-deleted only when distinct file_path hits zero), and audit. PK guarantees
		// uniqueness per (node_uuid, member_id, file_path). start_line/end_line are nullable.
		`CREATE TABLE IF NOT EXISTS node_sources (
    node_uuid   TEXT NOT NULL,
    member_id   TEXT NOT NULL,
    skill       TEXT NOT NULL,
    file_path   TEXT NOT NULL,
    start_line  INTEGER,
    end_line    INTEGER,
    PRIMARY KEY (node_uuid, member_id, file_path)
);`,

		// uuid_lineage — node 身份血缘（merge/split 才写，普通 member 增删不写）。
		// 仅在重整 merge / split 身份重组时插入一行，记录 new_uuid ← old_uuid 的演变与原因。
		// 决策总账 R2 / V3-a：uuid 首次派生后钉死永不变；只有身份重组才产生新 uuid 并写 lineage。
		// reason CHECK 限定为 ('merged','split')，防止脏数据写入其他值。
		//
		// uuid_lineage — node identity lineage (written only on merge/split, never on
		// plain member add/remove). A row records new_uuid ← old_uuid with its reason.
		// Decision ledger R2 / V3-a: a uuid is pinned once first derived and never changes;
		// only identity restructure (merge/split) produces a new uuid and writes lineage.
		// reason is CHECK-constrained to ('merged','split') to block dirty values.
		`CREATE TABLE IF NOT EXISTS uuid_lineage (
    new_uuid   TEXT NOT NULL,
    old_uuid   TEXT NOT NULL,
    reason     TEXT NOT NULL CHECK(reason IN ('merged','split')),
    created_at TEXT DEFAULT (datetime('now')),
    PRIMARY KEY (new_uuid, old_uuid)
);`,
	}
}

// CreateIndexesSQL 返回所有 CREATE INDEX 语句。
// 索引覆盖了高频查询字段：domain、subdomain、label（nodes 表），
// 以及 source_id、target_id、kind、provenance（edges 表），以加速常见查询。
//
// CreateIndexesSQL returns all CREATE INDEX statements.
// Indexes cover high-frequency query columns: domain, subdomain, label
// (nodes table), and source_id, target_id, kind, provenance (edges table)
// to accelerate common queries.
func CreateIndexesSQL() []string {
	return []string{
		`CREATE INDEX IF NOT EXISTS idx_nodes_domain ON nodes(domain);`,
		`CREATE INDEX IF NOT EXISTS idx_nodes_subdomain ON nodes(subdomain);`,
		`CREATE INDEX IF NOT EXISTS idx_nodes_label ON nodes(label);`,
		`CREATE INDEX IF NOT EXISTS idx_edges_source ON edges(source_id);`,
		`CREATE INDEX IF NOT EXISTS idx_edges_target ON edges(target_id);`,
		`CREATE INDEX IF NOT EXISTS idx_edges_kind ON edges(kind);`,
		`CREATE INDEX IF NOT EXISTS idx_edges_provenance ON edges(provenance);`,
		// node_sources 索引：按 file_path 反查「引用了某文档的所有 node」（回写/软删时用），
		// 以及按 node_uuid 正查「某 node 的所有来源」（shared 判定 / 审计时用）。
		// Indexes on node_sources: lookup by file_path (which nodes cite a doc — used by
		// write-back / soft-delete) and by node_uuid (all sources of a node — shared
		// ownership / audit).
		`CREATE INDEX IF NOT EXISTS idx_node_sources_file ON node_sources(file_path);`,
		`CREATE INDEX IF NOT EXISTS idx_node_sources_uuid ON node_sources(node_uuid);`,
	}
}

// AllTableNames 返回所有 10 张数据表的名称列表。
// 包含：file_states、kb_version、nodes、edges、cross_references、
// conflict_reports、kg_manifest、nodes_fts（虚拟 FTS 表）、
// node_sources（node↔来源文档映射）、uuid_lineage（node 身份血缘）。
//
// AllTableNames returns the list of all 10 data table names.
// Includes: file_states, kb_version, nodes, edges, cross_references,
// conflict_reports, kg_manifest, nodes_fts (virtual FTS table),
// node_sources (node↔source-doc mapping), uuid_lineage (node identity lineage).
func AllTableNames() []string {
	return []string{
		"file_states",
		"kb_version",
		"nodes",
		"edges",
		"cross_references",
		"conflict_reports",
		"kg_manifest",
		"nodes_fts",
		"node_sources",
		"uuid_lineage",
	}
}
