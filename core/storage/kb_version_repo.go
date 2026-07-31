// Package storage 提供领域知识层的持久化存储层实现，包括数据库连接管理、
// 模式定义、数据迁移和 CRUD 仓储。
//
// 本文件包含 KBVersionRepo 仓储，用于管理知识库的版本号。
// Package storage implements the persistence layer for the Domain Knowledge Layer,
// including database connection management, schema definition, data migration,
// and CRUD repositories.
//
// This file contains the KBVersionRepo repository for managing knowledge base
// version numbers.
package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// KBVersionRepo 封装对 kb_version 表的追加与查询操作。
// 知识库版本号使用 ISO 8601 时间戳格式，每次 Bump 生成新的版本标识。
//
// 契约：kb_version 是只追加的版本审计表——只 INSERT（Bump），不 DELETE。
// Current 依赖 rowid（插入顺序）排序取最新版本，禁止删除行（否则 rowid 可能复用，
// 破坏「最新 Bump」语义）。如未来确需清理老版本，须改用 AUTOINCREMENT 列或另行评估。
//
// KBVersionRepo appends to and queries the kb_version table.
// Knowledge base version numbers use ISO 8601 timestamp format;
// each Bump call generates a new version identifier.
// Contract: kb_version is append-only — INSERT (Bump) only, never DELETE.
// Current relies on rowid (insert order) for "latest"; deletion would break it.
type KBVersionRepo struct {
	db *DB
}

// NewKBVersionRepo 创建新的 KBVersionRepo 实例。
//
// NewKBVersionRepo creates a new KBVersionRepo instance.
func NewKBVersionRepo(db *DB) *KBVersionRepo {
	return &KBVersionRepo{db: db}
}

// Current 返回知识库的当前版本号。
// 取 kb_version 表中按 rowid 降序的第一条记录（rowid = 插入顺序 = Bump 顺序）。
// 若表中无记录，返回空字符串。
//
// 排序键用 rowid 而非 updated_at：version/updated_at 用 time.RFC3339Nano 格式化，
// 该格式截断 trailing zeros，使字典序与时间序倒挂（精度短的时间戳字典序反而更大，
// 因 'Z' > 数字字符）→ ORDER BY updated_at DESC 在两次 Bump 精度不同时会取到较早版本。
// rowid 是 SQLite 普通表自带的插入顺序整数，kb_version 只追加不删除，rowid 严格单调，
// 既绕开精度问题，也对时钟回拨健壮（语义是「最新 Bump」而非「最大时间戳」）。
//
// Current returns the current knowledge base version.
// Retrieves the latest-bumped version by rowid (insert order = Bump order).
// Returns an empty string if the table is empty.
func (r *KBVersionRepo) Current(ctx context.Context) (string, error) {
	r.db.mu.RLock()
	defer r.db.mu.RUnlock()

	var version string
	err := r.db.Conn().QueryRowContext(ctx,
		`SELECT version FROM kb_version ORDER BY rowid DESC LIMIT 1`).Scan(&version)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("KBVersionRepo.Current: %w", err)
	}
	return version, nil
}

// Bump 生成新的版本号并将其持久化到 kb_version 表。
// 版本号格式为 ISO 8601 时间戳（如 "2026-06-15T14:30:00Z"）。
// 返回新生成的版本号字符串。
//
// Bump generates a new version number and persists it to the kb_version table.
// The version number is an ISO 8601 timestamp (e.g., "2026-06-15T14:30:00Z").
// Returns the newly generated version string.
func (r *KBVersionRepo) Bump(ctx context.Context) (string, error) {
	r.db.mu.Lock()
	defer r.db.mu.Unlock()

	version := time.Now().UTC().Format(time.RFC3339Nano)

	_, err := r.db.Conn().ExecContext(ctx,
		`INSERT INTO kb_version (version, updated_at)
		 VALUES (?, ?)`, version, version)
	if err != nil {
		return "", fmt.Errorf("KBVersionRepo.Bump: %w", err)
	}

	return version, nil
}
