// Package storage 提供领域知识层的持久化存储层实现，包括数据库连接管理、
// 模式定义、数据迁移和 CRUD 仓储。
//
// 本文件包含数据库迁移机制：Migration 结构体、Migrations 注册表以及
// DB 上的 Migrate 和 SchemaVersion 方法。迁移利用 SQLite 的 PRAGMA user_version
// 追踪当前版本，只执行未应用过的迁移。
// Package storage implements the persistence layer for the Domain Knowledge Layer,
// including database connection management, schema definition, data migration,
// and CRUD repositories.
//
// This file contains the database migration mechanism: the Migration struct,
// Migrations registry, and the Migrate and SchemaVersion methods on DB.
// Migrations use SQLite's PRAGMA user_version to track the current version,
// executing only unapplied migrations.
package storage

import "fmt"

// Migration 表示一次数据库迁移操作。
// 每次迁移包含一个版本号、描述信息以及向上/向下 SQL 语句。
// 向上迁移（Up）用于应用变更，向下迁移（Down）用于回滚。
//
// Migration represents a single database migration operation.
// Each migration contains a version number, description, and up/down SQL statements.
// The Up migration applies changes; the Down migration rolls them back.
type Migration struct {
	// Version 是迁移的递增版本号，从 1 开始
	// Version is the incremental migration version number, starting from 1
	Version int
	// Description 描述本次迁移的目的和内容
	// Description describes the purpose and content of this migration
	Description string
	// Up 是应用迁移的 SQL 语句（可为空，表示仅由初始 DDL 创建的模式）
	// Up is the SQL statement to apply the migration (may be empty for initial DDL-created schema)
	Up string
	// Down 是回滚迁移的 SQL 语句
	// Down is the SQL statement to rollback the migration
	Down string
}

// Migrations 返回按版本号排序的所有迁移列表。
// 迁移必须按版本号递增顺序添加，每个版本仅执行一次。
//
// Migrations returns all migrations in version order.
// Migrations must be added in increasing version order;
// each version is executed only once.
func Migrations() []Migration {
	return []Migration{
		{
			Version:     1,
			Description: "initial schema — 初始模式，包含 7 张表及索引 / initial schema with 7 tables and indexes",
			Up:          "",
			Down:        "",
		},
		{
			Version:     2,
			Description: "file_states 表添加 repo_url 列，改为 (repo_url, file_path) 复合主键 / add repo_url column, change to (repo_url, file_path) composite PK",
			Up: "BEGIN IMMEDIATE;\n" +
			`
		-- 创建新的 file_states 表（含 repo_url 复合主键）
		-- Create new file_states table with repo_url composite primary key
		CREATE TABLE IF NOT EXISTS file_states_new (
		    repo_url     TEXT NOT NULL DEFAULT '',
		    file_path    TEXT NOT NULL,
		    content_hash TEXT NOT NULL,
		    indexed_at   TEXT DEFAULT (datetime('now')),
		    PRIMARY KEY (repo_url, file_path)
		);

		-- 将旧数据迁移到新表（repo_url 设为空字符串）
		-- Migrate old data to new table (repo_url set to empty string)
		INSERT INTO file_states_new (repo_url, file_path, content_hash, indexed_at)
		    SELECT '', file_path, content_hash, indexed_at FROM file_states
		    WHERE true
		    ON CONFLICT(repo_url, file_path) DO NOTHING;

		-- 删除旧表
		-- Drop old table
		DROP TABLE file_states;

		-- 重命名新表
		-- Rename new table
		ALTER TABLE file_states_new RENAME TO file_states;
		` +
			"COMMIT;",
			Down: "BEGIN IMMEDIATE;\n" +
				`
		-- 回滚到单主键模式（保留 file_path 作为唯一主键）
		-- Revert to single primary key (keep file_path as the only PK)
		CREATE TABLE IF NOT EXISTS file_states_old (
		    file_path    TEXT PRIMARY KEY,
		    content_hash TEXT NOT NULL,
		    indexed_at   TEXT DEFAULT (datetime('now'))
		);

		INSERT INTO file_states_old (file_path, content_hash, indexed_at)
		    SELECT file_path, content_hash, indexed_at FROM file_states;

		DROP TABLE file_states;
		ALTER TABLE file_states_old RENAME TO file_states;
		` +
				"COMMIT;",
		},
		{
			Version:     3,
			Description: "cross_references 表 resolution_status 新增 'external_kg' 状态 / add 'external_kg' to cross_references.resolution_status CHECK",
			Up: "BEGIN IMMEDIATE;\n" +
				`
		-- 重新创建 cross_references 表，更新 CHECK 约束以包含 'external_kg'
		-- Recreate cross_references table with updated CHECK constraint including 'external_kg'
		CREATE TABLE IF NOT EXISTS cross_references_new (
		    id                INTEGER PRIMARY KEY AUTOINCREMENT,
		    source_entity     TEXT NOT NULL,
		    target_entity     TEXT NOT NULL,
		    target_node_id    TEXT,
		    kind              TEXT NOT NULL,
		    reason            TEXT,
		    resolution_status TEXT NOT NULL DEFAULT 'resolved' CHECK(resolution_status IN ('resolved', 'unresolved', 'external_kg')),
		    created_at        TEXT DEFAULT (datetime('now'))
		);

		-- 将现有数据迁移到新表（保留所有现有行）
		-- Migrate existing data to new table (preserve all existing rows)
		INSERT INTO cross_references_new
		    SELECT id, source_entity, target_entity, target_node_id, kind, reason, resolution_status, created_at
		    FROM cross_references;

		-- 删除旧表并重命名新表
		-- Drop old table and rename new table
		DROP TABLE cross_references;
		ALTER TABLE cross_references_new RENAME TO cross_references;
		` +
				"COMMIT;",
			Down: "BEGIN IMMEDIATE;\n" +
				`
		-- 回滚：重新创建不含 'external_kg' 的 cross_references 表
		-- Rollback: recreate cross_references table without 'external_kg'
		CREATE TABLE IF NOT EXISTS cross_references_old (
		    id                INTEGER PRIMARY KEY AUTOINCREMENT,
		    source_entity     TEXT NOT NULL,
		    target_entity     TEXT NOT NULL,
		    target_node_id    TEXT,
		    kind              TEXT NOT NULL,
		    reason            TEXT,
		    resolution_status TEXT NOT NULL DEFAULT 'resolved' CHECK(resolution_status IN ('resolved', 'unresolved')),
		    created_at        TEXT DEFAULT (datetime('now'))
		);

		INSERT INTO cross_references_old
		    SELECT id, source_entity, target_entity, target_node_id, kind, reason,
		           CASE WHEN resolution_status = 'external_kg' THEN 'unresolved' ELSE resolution_status END,
		           created_at
		    FROM cross_references;

		DROP TABLE cross_references;
		ALTER TABLE cross_references_old RENAME TO cross_references;
		` +
				"COMMIT;",
		},
		{
			Version:     4,
			Description: "方案 B 一等节点：nodes.label 加 Skill/Domain/Subdomain，edges.kind 加 provides / Plan B first-class nodes: add Skill/Domain/Subdomain to nodes.label, add provides to edges.kind",
			// SQLite 无法直接 ALTER 一个 CHECK 约束，必须重建表（同 migration 3 的做法）。
			// 关键点：
			//  1) nodes 与 edges 均在 CreateIndexesSQL() 中定义了索引，且 NewDB 先建索引后跑迁移，
			//     因此重建表会丢失这些索引 —— 本迁移末尾必须重新创建它们，保持自洽。
			//  2) nodes 被 nodes_fts 通过 rowid 映射，重建时显式拷贝 rowid 以保持 FTS 索引有效，
			//     无需重建 FTS。
			//  3) edges.id 是 INTEGER PRIMARY KEY（即 rowid），显式拷贝以保持自增主键连续。
			//
			// SQLite cannot ALTER a CHECK constraint in place; the table must be rebuilt
			// (same approach as migration 3). Key points:
			//  1) Both nodes and edges have indexes in CreateIndexesSQL(), and NewDB creates
			//     indexes before running migrations, so rebuilding drops those indexes — this
			//     migration recreates them at the end to stay self-contained.
			//  2) nodes is mapped by nodes_fts via rowid; we copy rowid explicitly to keep the
			//     FTS index valid, avoiding an FTS rebuild.
			//  3) edges.id is INTEGER PRIMARY KEY (the rowid); copied explicitly to preserve the
			//     autoincrement primary key values.
			Up: "BEGIN IMMEDIATE;\n" +
				`
		-- 重建 nodes 表，label CHECK 扩展为 5 类一等节点
		-- Rebuild nodes with label CHECK extended to 5 first-class node types
		CREATE TABLE IF NOT EXISTS nodes_new (
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
		    created_at       TEXT DEFAULT (datetime('now')),
		    updated_at       TEXT DEFAULT (datetime('now'))
		);

		-- 显式拷贝 rowid，保持 nodes_fts 的 rowid 映射有效
		-- Copy rowid explicitly to keep the nodes_fts rowid mapping valid
		INSERT INTO nodes_new (rowid, id, label, name, summary, synonyms, domain, subdomain,
		    description, properties, tags, related_entities, confidence,
		    provenance, source_refs, created_at, updated_at)
		    SELECT rowid, id, label, name, summary, synonyms, domain, subdomain,
		           description, properties, tags, related_entities, confidence,
		           provenance, source_refs, created_at, updated_at
		    FROM nodes;

		DROP TABLE nodes;
		ALTER TABLE nodes_new RENAME TO nodes;

		-- 重建 edges 表，kind CHECK 加入 'provides'
		-- Rebuild edges with 'provides' added to the kind CHECK
		CREATE TABLE IF NOT EXISTS edges_new (
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
		);

		-- 显式拷贝 id（自增主键/rowid），保持边 ID 稳定
		-- Copy id (autoincrement PK / rowid) explicitly to keep edge IDs stable
		INSERT INTO edges_new (id, source_id, target_id, kind, description, properties,
		    provenance, confidence, source_refs, bidirectional, cardinality, created_at)
		    SELECT id, source_id, target_id, kind, description, properties,
		           provenance, confidence, source_refs, bidirectional, cardinality, created_at
		    FROM edges;

		DROP TABLE edges;
		ALTER TABLE edges_new RENAME TO edges;

		-- 重建被丢弃的索引（NewDB 先建索引后迁移，重建表会连带删除索引）
		-- Recreate the dropped indexes (NewDB creates indexes before migrating; rebuilding drops them)
		CREATE INDEX IF NOT EXISTS idx_nodes_domain ON nodes(domain);
		CREATE INDEX IF NOT EXISTS idx_nodes_subdomain ON nodes(subdomain);
		CREATE INDEX IF NOT EXISTS idx_nodes_label ON nodes(label);
		CREATE INDEX IF NOT EXISTS idx_edges_source ON edges(source_id);
		CREATE INDEX IF NOT EXISTS idx_edges_target ON edges(target_id);
		CREATE INDEX IF NOT EXISTS idx_edges_kind ON edges(kind);
		CREATE INDEX IF NOT EXISTS idx_edges_provenance ON edges(provenance);
		` +
				"COMMIT;",
			// 回滚：恢复旧 CHECK 约束。新引入的节点/边类型在旧约束下非法，
			// 故 down 迁移显式过滤掉 label IN (Skill/Domain/Subdomain) 的节点及
			// kind='provides' 的边（否则重建会触发 CHECK 失败）。这是方案 B 回退的固有数据损失，
			// 已在此注明；Migrate() 目前不自动调用 Down，此逻辑仅为对称性与手动回滚保留。
			// Rollback: restore old CHECK constraints. The newly introduced node/edge types are
			// invalid under the old constraints, so the down migration explicitly filters out
			// nodes with label IN (Skill/Domain/Subdomain) and edges with kind='provides'
			// (otherwise the rebuild would trigger a CHECK failure). This data loss is inherent
			// to reverting Plan B and is noted here. Migrate() does not call Down automatically;
			// this exists for symmetry and manual rollback.
			Down: "BEGIN IMMEDIATE;\n" +
				`
		CREATE TABLE IF NOT EXISTS nodes_old (
		    id               TEXT PRIMARY KEY,
		    label            TEXT NOT NULL CHECK(label IN ('Entity', 'Concept')),
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
		    created_at       TEXT DEFAULT (datetime('now')),
		    updated_at       TEXT DEFAULT (datetime('now'))
		);

		-- 仅拷贝旧约束允许的 Entity/Concept 节点，丢弃一等节点（Skill/Domain/Subdomain）
		-- Copy only Entity/Concept nodes allowed by the old constraint; drop first-class nodes
		INSERT INTO nodes_old (rowid, id, label, name, summary, synonyms, domain, subdomain,
		    description, properties, tags, related_entities, confidence,
		    provenance, source_refs, created_at, updated_at)
		    SELECT rowid, id, label, name, summary, synonyms, domain, subdomain,
		           description, properties, tags, related_entities, confidence,
		           provenance, source_refs, created_at, updated_at
		    FROM nodes WHERE label IN ('Entity', 'Concept');

		DROP TABLE nodes;
		ALTER TABLE nodes_old RENAME TO nodes;

		CREATE TABLE IF NOT EXISTS edges_old (
		    id            INTEGER PRIMARY KEY AUTOINCREMENT,
		    source_id     TEXT NOT NULL,
		    target_id     TEXT NOT NULL,
		    kind          TEXT NOT NULL CHECK(kind IN ('triggers', 'depends_on', 'references', 'generalizes', 'composes', 'contradicts')),
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
		);

		-- 丢弃 kind='provides' 的边（旧约束不允许）
		-- Drop edges with kind='provides' (not allowed by the old constraint)
		INSERT INTO edges_old (id, source_id, target_id, kind, description, properties,
		    provenance, confidence, source_refs, bidirectional, cardinality, created_at)
		    SELECT id, source_id, target_id, kind, description, properties,
		           provenance, confidence, source_refs, bidirectional, cardinality, created_at
		    FROM edges WHERE kind <> 'provides';

		DROP TABLE edges;
		ALTER TABLE edges_old RENAME TO edges;

		CREATE INDEX IF NOT EXISTS idx_nodes_domain ON nodes(domain);
		CREATE INDEX IF NOT EXISTS idx_nodes_subdomain ON nodes(subdomain);
		CREATE INDEX IF NOT EXISTS idx_nodes_label ON nodes(label);
		CREATE INDEX IF NOT EXISTS idx_edges_source ON edges(source_id);
		CREATE INDEX IF NOT EXISTS idx_edges_target ON edges(target_id);
		CREATE INDEX IF NOT EXISTS idx_edges_kind ON edges(kind);
		CREATE INDEX IF NOT EXISTS idx_edges_provenance ON edges(provenance);
		` +
				"COMMIT;",
		},
		{
			Version:     5,
			Description: "nodes 表添加 file_slug 列（shared 节点 primary 文件的可读 slug）/ add file_slug column for human-readable shared file names",
			Up:          "ALTER TABLE nodes ADD COLUMN file_slug TEXT;",
			Down:        "", // SQLite 不支持 DROP COLUMN（需重建表），回滚不支持
		},
	}
}

// Migrate 执行所有尚未应用的数据库迁移。
// 通过 SQLite PRAGMA user_version 读取当前模式版本，仅执行版本号大于当前版本的迁移。
// 每执行完一个迁移即更新 PRAGMA user_version，保证即使中途失败也能准确定位。
//
// Migrate applies all unapplied database migrations.
// It reads the current schema version via SQLite PRAGMA user_version and only executes
// migrations with a version number greater than the current version.
// After each migration, PRAGMA user_version is updated, ensuring accurate tracking
// even if a migration fails midway.
func (db *DB) Migrate() error {
	// 读取当前模式版本 / Read current schema version
	var currentVersion int
	row := db.conn.QueryRow("PRAGMA user_version")
	if err := row.Scan(&currentVersion); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}

	migrations := Migrations()
	for _, m := range migrations {
		if m.Version > currentVersion {
			// 执行向上迁移 SQL / Execute up migration SQL
			if m.Up != "" {
				if _, err := db.conn.Exec(m.Up); err != nil {
					return fmt.Errorf("migration %d (%s): %w", m.Version, m.Description, err)
				}
			}
			// 更新模式版本号 / Update schema version
			if _, err := db.conn.Exec(fmt.Sprintf("PRAGMA user_version = %d", m.Version)); err != nil {
				return fmt.Errorf("set user_version to %d: %w", m.Version, err)
			}
		}
	}
	return nil
}

// SchemaVersion 返回当前数据库的模式版本号。
// 版本号通过 SQLite PRAGMA user_version 读取。
//
// SchemaVersion returns the current database schema version.
// The version is read via SQLite PRAGMA user_version.
func (db *DB) SchemaVersion() (int, error) {
	var v int
	err := db.conn.QueryRow("PRAGMA user_version").Scan(&v)
	return v, err
}
