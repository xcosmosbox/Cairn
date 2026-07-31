package storage

// OpenPreflightReadOnly 专供“先验证、后迁移”的构建入口。它不设置 journal_mode，
// 不执行 DDL/索引/迁移，并通过 mode=ro + query_only 保证无写副作用。

import (
	"database/sql"
	"fmt"
)

func OpenPreflightReadOnly(path string) (*DB, error) {
	// 直接以 path 拼接 DSN，不走 url.URL 的 file: URI 构造：
	// modernc.org/sqlite 对 `file:<相对路径>` 形式的 URI 解析存在缺陷——
	// 相对路径（如 ../tmp/kg.db）会被误解析，Ping 报 "out of memory (1)"。
	// 直接 path 拼 DSN 时驱动按普通文件路径处理，相对/绝对路径均正确。
	// （与 OpenReadOnly 的 DSN 构造方式保持一致。）
	dsn := path + "?mode=ro&_query_only=true&_busy_timeout=5000"
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open preflight readonly sqlite %s: %w", path, err)
	}
	conn.SetMaxOpenConns(1)
	if err := conn.Ping(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("validate sqlite %s: %w", path, err)
	}
	return &DB{conn: conn, path: path}, nil
}
