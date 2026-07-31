// Package storage 提供领域知识层的持久化存储层实现。
//
// 本文件包含 ManifestRepo 仓储，提供 kg_manifest 表（键值对，value 为 JSON 字符串）
// 的基础读写。kg_manifest 是跨会话的清单存储：本批由增量流水线写入「本轮/累计改动量」，
// 供第三块（全量重整 dk-rebalance）的保底触发读取。
//
// Package storage implements the persistence layer for the Domain Knowledge Layer.
// This file contains the ManifestRepo repository, providing basic read/write over
// the kg_manifest key-value table (values are JSON strings). The incremental
// pipeline writes per-run/cumulative change statistics here for the rebalance
// trigger to consume later.
package storage

import (
	"context"
	"database/sql"
	"fmt"
)

// ManifestRepo 封装对 kg_manifest 表（KV，value 为 JSON）的读写操作。
//
// ManifestRepo encapsulates read/write operations on the kg_manifest KV table.
type ManifestRepo struct {
	db *DB
}

// NewManifestRepo 创建新的 ManifestRepo 实例。
//
// NewManifestRepo creates a new ManifestRepo instance.
func NewManifestRepo(db *DB) *ManifestRepo {
	return &ManifestRepo{db: db}
}

// Set 以 INSERT OR REPLACE 语义写入一个键值对（value 为 JSON 字符串，调用方负责序列化）。
//
// Set upserts a key-value pair; value is a JSON string serialized by the caller.
func (r *ManifestRepo) Set(ctx context.Context, key, valueJSON string) error {
	r.db.mu.Lock()
	defer r.db.mu.Unlock()

	_, err := r.db.Conn().ExecContext(ctx,
		`INSERT OR REPLACE INTO kg_manifest (key, value) VALUES (?, ?)`, key, valueJSON)
	if err != nil {
		return fmt.Errorf("ManifestRepo.Set %s: %w", key, err)
	}
	return nil
}

// Get 读取指定 key 的 value（JSON 字符串）。key 不存在时返回 "", nil。
//
// Get returns the JSON value for a key; ("", nil) when the key is absent.
func (r *ManifestRepo) Get(ctx context.Context, key string) (string, error) {
	r.db.mu.RLock()
	defer r.db.mu.RUnlock()

	var value string
	err := r.db.Conn().QueryRowContext(ctx,
		`SELECT value FROM kg_manifest WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("ManifestRepo.Get %s: %w", key, err)
	}
	return value, nil
}
