// Package storage 提供领域知识层的持久化存储层实现，包括数据库连接管理、
// 模式定义、数据迁移和 CRUD 仓储。
//
// 本文件包含 FileStateRepo 仓储，用于跟踪已索引文件的哈希状态以支持增量更新。
// 使用 (repo_url, file_path) 复合主键实现 Loop Guard：
// 防止同一文件在不同仓库中被重复处理。
// Package storage implements the persistence layer for the Domain Knowledge Layer,
// including database connection management, schema definition, data migration,
// and CRUD repositories.
//
// This file contains the FileStateRepo repository for tracking indexed file
// hashes to support incremental updates.
// Uses (repo_url, file_path) composite primary key for Loop Guard:
// prevents the same file from being re-processed across different repositories.
package storage

import (
	"context"
	"database/sql"
	"fmt"
)

// FileState 表示一个已索引文件的状态记录。
// 通过 (repo_url, file_path) 唯一标识一条记录。
//
// FileState represents the state record of an indexed file.
// Uniquely identified by the (repo_url, file_path) composite key.
type FileState struct {
	// RepoURL 是文件所属仓库的 URL。
	// RepoURL is the URL of the repository the file belongs to.
	RepoURL string
	// FilePath 是文件的完整路径。
	// FilePath is the full path of the file.
	FilePath string
	// ContentHash 是文件内容的哈希值。
	// ContentHash is the hash of the file content.
	ContentHash string
	// IndexedAt 是文件的最近索引时间（SQLite TEXT）。
	// IndexedAt is the time the file was last indexed (SQLite TEXT).
	IndexedAt string
}

// FileStateRepo 封装对 file_states 表的 CRUD 操作。
// 用于增量索引：通过比对文件哈希判断文件是否已变更。
// 同时支持 Loop Guard：检查 (repo_url, file_path, hash) 是否已处理。
//
// FileStateRepo encapsulates CRUD operations on the file_states table.
// Used for incremental indexing: compares file hashes to determine whether
// a file has changed.
// Also supports Loop Guard: checks whether a (repo_url, file_path, hash)
// combination has already been processed.
type FileStateRepo struct {
	db *DB
}

// NewFileStateRepo 创建新的 FileStateRepo 实例。
//
// NewFileStateRepo creates a new FileStateRepo instance.
func NewFileStateRepo(db *DB) *FileStateRepo {
	return &FileStateRepo{db: db}
}

// Get 获取指定仓库和文件路径的状态记录。
// 若文件状态不存在，返回 nil, nil。
//
// Get retrieves the state record for a given repo URL and file path.
// Returns nil, nil if no state record exists.
func (r *FileStateRepo) Get(ctx context.Context, repoURL, filePath string) (*FileState, error) {
	r.db.mu.RLock()
	defer r.db.mu.RUnlock()

	row := r.db.Conn().QueryRowContext(ctx,
		`SELECT repo_url, file_path, content_hash, indexed_at
		   FROM file_states WHERE repo_url = ? AND file_path = ?`, repoURL, filePath)

	var fs FileState
	err := row.Scan(&fs.RepoURL, &fs.FilePath, &fs.ContentHash, &fs.IndexedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("FileStateRepo.Get repo=%s path=%s: %w", repoURL, filePath, err)
	}
	return &fs, nil
}

// Upsert 插入或更新文件状态记录（使用 repoURL 作为分区键）。
// 使用 INSERT OR REPLACE 语义：若 (repo_url, file_path) 已存在则更新，
// 否则插入新记录。
//
// Upsert inserts or updates a file state record (using repoURL as partition key).
// Uses INSERT OR REPLACE semantics: updates if (repo_url, file_path) exists,
// inserts a new record otherwise.
func (r *FileStateRepo) Upsert(ctx context.Context, repoURL, filePath, contentHash string) error {
	r.db.mu.Lock()
	defer r.db.mu.Unlock()

	_, err := r.db.Conn().ExecContext(ctx,
		`INSERT OR REPLACE INTO file_states (repo_url, file_path, content_hash, indexed_at)
		 VALUES (?, ?, ?, datetime('now'))`, repoURL, filePath, contentHash)
	if err != nil {
		return fmt.Errorf("FileStateRepo.Upsert repo=%s path=%s: %w", repoURL, filePath, err)
	}
	return nil
}

// HasChanged 检查文件内容是否已变更。
// 将新哈希与存储的哈希比较：若记录不存在或哈希不同，返回 true。
//
// HasChanged checks whether the file content has changed.
// Compares the new hash against the stored hash: returns true if the record
// does not exist or the hashes differ.
func (r *FileStateRepo) HasChanged(ctx context.Context, repoURL, filePath, newHash string) (bool, error) {
	r.db.mu.RLock()
	defer r.db.mu.RUnlock()

	row := r.db.Conn().QueryRowContext(ctx,
		`SELECT content_hash FROM file_states WHERE repo_url = ? AND file_path = ?`, repoURL, filePath)

	var storedHash string
	err := row.Scan(&storedHash)
	if err == sql.ErrNoRows {
		// 文件从未索引过，视为已变更 / File never indexed, treat as changed
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("FileStateRepo.HasChanged repo=%s path=%s: %w", repoURL, filePath, err)
	}

	return storedHash != newHash, nil
}

// ShouldSkip 检查该文件是否已在当前哈希值下处理过。
// 这是 Loop Guard 的核心方法：若 (repo_url, file_path) 的记录存在且
// content_hash 完全匹配，返回 true（可跳过处理）；否则返回 false（需处理）。
//
// ShouldSkip checks whether this file at this hash has already been processed.
// This is the core Loop Guard method: if a record for (repo_url, file_path)
// exists and the content_hash matches exactly, returns true (skip processing);
// otherwise returns false (process).
func (r *FileStateRepo) ShouldSkip(ctx context.Context, repoURL, filePath, newHash string) (bool, error) {
	r.db.mu.RLock()
	defer r.db.mu.RUnlock()

	row := r.db.Conn().QueryRowContext(ctx,
		`SELECT content_hash FROM file_states WHERE repo_url = ? AND file_path = ?`, repoURL, filePath)

	var storedHash string
	err := row.Scan(&storedHash)
	if err == sql.ErrNoRows {
		// 从未处理过，不跳过 / Never processed, don't skip
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("FileStateRepo.ShouldSkip repo=%s path=%s: %w", repoURL, filePath, err)
	}

	// 相同哈希 → 已处理过，跳过 / Same hash → already processed, skip
	return storedHash == newHash, nil
}

// Delete 删除指定仓库中指定文件的状态记录。
// Delete removes the state record for a specified file in a specified repo.
func (r *FileStateRepo) Delete(ctx context.Context, repoURL, filePath string) error {
	r.db.mu.Lock()
	defer r.db.mu.Unlock()

	_, err := r.db.Conn().ExecContext(ctx,
		`DELETE FROM file_states WHERE repo_url = ? AND file_path = ?`, repoURL, filePath)
	if err != nil {
		return fmt.Errorf("FileStateRepo.Delete repo=%s path=%s: %w", repoURL, filePath, err)
	}
	return nil
}

// ListByRepo 列出指定仓库中的所有文件状态记录。
// ListByRepo lists all file state records for a given repo URL.
func (r *FileStateRepo) ListByRepo(ctx context.Context, repoURL string) ([]*FileState, error) {
	r.db.mu.RLock()
	defer r.db.mu.RUnlock()

	rows, err := r.db.Conn().QueryContext(ctx,
		`SELECT repo_url, file_path, content_hash, indexed_at
		   FROM file_states WHERE repo_url = ? ORDER BY file_path`, repoURL)
	if err != nil {
		return nil, fmt.Errorf("FileStateRepo.ListByRepo %s: %w", repoURL, err)
	}
	defer rows.Close()

	var states []*FileState
	for rows.Next() {
		var fs FileState
		if err := rows.Scan(&fs.RepoURL, &fs.FilePath, &fs.ContentHash, &fs.IndexedAt); err != nil {
			return states, fmt.Errorf("FileStateRepo.ListByRepo scan: %w", err)
		}
		states = append(states, &fs)
	}
	return states, rows.Err()
}
