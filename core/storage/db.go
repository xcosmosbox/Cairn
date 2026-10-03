// Package storage 提供领域知识层的持久化存储层实现，包括数据库连接管理、
// 模式定义、数据迁移和 CRUD 仓储。
//
// 本文件包含数据库连接包装器 DB 及初始化入口 NewDB，负责创建 SQLite 连接、
// 启用 WAL 模式、执行 DDL 和迁移。
// Package storage implements the persistence layer for the Cairn,
// including database connection management, schema definition, data migration,
// and CRUD repositories.
//
// This file contains the DB connection wrapper and the NewDB entry point, responsible
// for creating the SQLite connection, enabling WAL mode, running DDL, and migrations.
package storage

import (
	"context"
	"database/sql"
	"fmt"
	"sync"

	_ "modernc.org/sqlite" // SQLite driver (pure Go, CGo free)
)

// DB 包装一个 SQLite 数据库连接，提供线程安全访问。
// 使用 RWMutex 保护所有写操作，允许多个读操作并发执行。
//
// DB wraps a SQLite database connection with thread-safe access.
// A RWMutex protects all write operations while allowing concurrent reads.
type DB struct {
	conn *sql.DB
	mu   sync.RWMutex
	path string // 数据库文件路径 / database file path
}

// DBOptions 配置数据库连接的参数。
// 所有字段均有合理默认值，不设置即可使用。
//
// DBOptions configures the database connection parameters.
// All fields have sensible defaults and can be used without explicit configuration.
type DBOptions struct {
	// Path 是 SQLite 数据库文件路径，默认 "./knowledge.db"
	// Path is the SQLite database file path, default "./knowledge.db"
	Path string
	// MaxOpenConns 是最大打开连接数，默认 1（SQLite 序列化写）
	// MaxOpenConns is the maximum number of open connections, default 1 (SQLite serializes writes)
	MaxOpenConns int
	// JournalMode 是 SQLite 日志模式，默认 "WAL"
	// JournalMode is the SQLite journal mode, default "WAL"
	JournalMode string
	// BusyTimeoutMs 是 SQLite 忙等待超时时间（毫秒），默认 5000
	// BusyTimeoutMs is the SQLite busy timeout in milliseconds, default 5000
	BusyTimeoutMs int
}

// NewDB 创建或打开一个 SQLite 数据库，自动执行以下操作：
//  1. 应用默认配置参数
//  2. 打开 SQLite 连接并启用 WAL 模式
//  3. 按序创建所有数据表
//  4. 创建所有索引
//  5. 运行数据库迁移
//
// 返回的 *DB 实例是线程安全的，可通过 Conn() 获取底层 *sql.DB。
//
// NewDB creates or opens a SQLite database, automatically performing:
//  1. Apply default configuration parameters
//  2. Open SQLite connection with WAL mode enabled
//  3. Create all data tables in dependency order
//  4. Create all indexes
//  5. Run database migrations
//
// The returned *DB instance is thread-safe; use Conn() to access the underlying *sql.DB.
func NewDB(opts DBOptions) (*DB, error) {
	// 应用默认配置 / Apply defaults
	if opts.Path == "" {
		opts.Path = "./knowledge.db"
	}
	if opts.MaxOpenConns == 0 {
		opts.MaxOpenConns = 1
	}
	if opts.JournalMode == "" {
		opts.JournalMode = "WAL"
	}
	if opts.BusyTimeoutMs == 0 {
		opts.BusyTimeoutMs = 5000
	}

	// 打开 SQLite 连接（通过 DSN 参数设置日志模式和忙等待超时）
	// Open SQLite connection (journal mode and busy timeout set via DSN parameters)
	conn, err := OpenSQLite(opts.Path, SQLiteOptions{JournalMode: opts.JournalMode, BusyTimeoutMs: opts.BusyTimeoutMs, MaxOpenConns: opts.MaxOpenConns})
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", opts.Path, err)
	}
	conn.SetMaxOpenConns(opts.MaxOpenConns)

	db := &DB{conn: conn, path: opts.Path}
	// 初始化失败也必须释放连接，否则重试会泄漏文件描述符与 SQLite 锁。
	// Close the connection on every failed initialization path.
	initialized := false
	defer func() {
		if !initialized {
			conn.Close()
		}
	}()

	// 创建数据表 / Create tables
	for _, ddl := range CreateTablesSQL() {
		if _, err := conn.Exec(ddl); err != nil {
			return nil, fmt.Errorf("create table: %w", err)
		}
	}

	// 创建索引 / Create indexes
	for _, idx := range CreateIndexesSQL() {
		if _, err := conn.Exec(idx); err != nil {
			return nil, fmt.Errorf("create index: %w", err)
		}
	}

	// 运行迁移 / Run migrations
	if err := db.Migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}

	initialized = true
	return db, nil
}

// Close 关闭底层数据库连接。
// 调用后不应再使用该 DB 实例。
//
// Close closes the underlying database connection.
// The DB instance should not be used after calling Close.
func (db *DB) Close() error {
	return db.conn.Close()
}

// Conn 返回底层的 *sql.DB 连接，供仓储层直接使用。
// 调用方应自行处理并发控制；对于写操作，推荐先获取写锁。
//
// Conn returns the underlying *sql.DB connection for direct use by repository layers.
// Callers should handle concurrency control; for write operations, acquiring
// the write lock is recommended.
func (db *DB) Conn() *sql.DB {
	return db.conn
}

// OpenReadOnly 以只读模式打开一个已存在的知识库，跳过建表与迁移。
// 适用于查询端（如 MCP Server）对已构建 .db 的只读访问，保证不会对
// 数据库产生任何写副作用。底层通过 SQLite mode=ro + query_only(1)
// 双重只读保护，且不执行任何 DDL 或 Migrate。
//
// 与 NewDB 的区别：NewDB 以读写模式打开并执行建表+索引+迁移（面向构建端）；
// OpenReadOnly 仅建立只读连接，面向查询端，不会也不需要修改 schema。
//
// OpenReadOnly opens an existing knowledge base in read-only mode, skipping
// table creation and migration. Suitable for read-only query frontends (e.g.
// MCP Server) over an already-built .db, guaranteeing no write side effects.
// Read-only is enforced doubly via SQLite mode=ro and query_only(1), and
// no DDL or migration is executed.
func OpenReadOnly(path string) (*DB, error) {
	if path == "" {
		path = "./knowledge.db"
	}
	conn, err := OpenSQLite(path, SQLiteOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("open readonly sqlite %s: %w", path, err)
	}
	conn.SetMaxOpenConns(1)
	return &DB{conn: conn, path: path}, nil
}

// OpenExisting 仅用于显式要求写元数据的命令；它不创建文件、不建表、不迁移。
// OpenExisting opens an existing DB read-write without schema initialization.
func OpenExisting(path string) (*DB, error) {
	conn, err := OpenSQLite(path, SQLiteOptions{ExistingOnly: true, PreserveJournalMode: true})
	if err != nil {
		return nil, err
	}
	return &DB{conn: conn, path: path}, nil
}

// 确保 context 包已导入（供后续仓储层使用）
// Ensure context package is imported (for future repository layer use)
var _ context.Context
