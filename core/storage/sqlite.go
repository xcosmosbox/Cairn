package storage

import (
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

// SQLiteOptions 控制连接权限；本入口不建表、不迁移。
// SQLiteOptions configures a connection without creating or migrating schema.
type SQLiteOptions struct {
	ReadOnly              bool
	ExistingOnly          bool
	JournalMode           string
	BusyTimeoutMs         int
	MaxOpenConns          int
	ImmediateTransactions bool
	PreserveJournalMode   bool
}

// OpenSQLite 对 modernc 使用实际支持的 URI/_pragma；普通路径上的 mode=ro
// 会被驱动忽略，因此必须先转为绝对 file: URI，并转义 ?/# 等文件名字符。
// Readonly connections never set journal_mode, create a missing file or migrate it.
func OpenSQLite(path string, opts SQLiteOptions) (*sql.DB, error) {
	if path == "" {
		return nil, fmt.Errorf("OpenSQLite: empty database path")
	}
	if opts.BusyTimeoutMs < 0 || opts.MaxOpenConns < 0 {
		return nil, fmt.Errorf("OpenSQLite: negative busy timeout or connection limit")
	}
	if opts.BusyTimeoutMs == 0 {
		opts.BusyTimeoutMs = 5000
	}
	if opts.MaxOpenConns == 0 {
		opts.MaxOpenConns = 1
	}
	q := url.Values{}
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", opts.BusyTimeoutMs))
	if opts.ImmediateTransactions {
		q.Set("_txlock", "immediate")
	}
	var dsn string
	if path == ":memory:" {
		if opts.ReadOnly {
			return nil, fmt.Errorf("OpenSQLite: readonly requires an existing file")
		}
		dsn = path
	} else {
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("OpenSQLite: absolute path: %w", err)
		}
		u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
		dsn = u.String()
		if opts.ReadOnly {
			q.Set("mode", "ro")
		} else if opts.ExistingOnly {
			q.Set("mode", "rw")
		} else {
			q.Set("mode", "rwc")
		}
	}
	if opts.ReadOnly {
		q.Add("_pragma", "query_only(1)")
	} else if !opts.PreserveJournalMode {
		mode := strings.ToUpper(opts.JournalMode)
		if mode == "" {
			mode = "WAL"
		}
		switch mode {
		case "WAL", "DELETE", "TRUNCATE", "PERSIST", "MEMORY", "OFF":
		default:
			return nil, fmt.Errorf("OpenSQLite: invalid journal mode %q", opts.JournalMode)
		}
		q.Add("_pragma", "journal_mode("+mode+")")
	}
	conn, err := sql.Open("sqlite", dsn+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("OpenSQLite %s: %w", path, err)
	}
	conn.SetMaxOpenConns(opts.MaxOpenConns)
	if err := conn.Ping(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("OpenSQLite %s: %w", path, err)
	}
	if opts.ReadOnly {
		var queryOnly int
		if err := conn.QueryRow("PRAGMA query_only").Scan(&queryOnly); err != nil || queryOnly != 1 {
			conn.Close()
			return nil, fmt.Errorf("OpenSQLite %s: readonly protection unavailable (query_only=%d): %v", path, queryOnly, err)
		}
	}
	return conn, nil
}
