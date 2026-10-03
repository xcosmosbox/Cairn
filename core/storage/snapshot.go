package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// SnapshotDatabase 将包括未 checkpoint WAL 的一致读快照输出到新的数据库。
// VACUUM INTO 会写目标库，故暂时关闭该私有连接的 query_only；源库的 mode=ro
// 仍由 SQLite 强制保护，不能改用 NewDB（会迁移源 KG）或拷贝不完整的主文件。
// SnapshotDatabase writes a consistent snapshot while keeping the source read-only.
func SnapshotDatabase(ctx context.Context, source, destination string) error {
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		if err == nil {
			return fmt.Errorf("SnapshotDatabase: destination already exists: %s", destination)
		}
		return fmt.Errorf("SnapshotDatabase: destination: %w", err)
	}
	abs, err := filepath.Abs(destination)
	if err != nil {
		return fmt.Errorf("SnapshotDatabase: destination path: %w", err)
	}
	// Only ever clean up a name owned by this call. Another writer may publish
	// destination after the initial Stat; exclusive Link must preserve its file.
	tmp, err := os.CreateTemp(filepath.Dir(abs), ".cairn-snapshot-*.db")
	if err != nil {
		return fmt.Errorf("SnapshotDatabase: temporary destination: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("SnapshotDatabase: close temporary destination: %w", err)
	}
	conn, err := OpenSQLite(source, SQLiteOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("SnapshotDatabase: source: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "PRAGMA query_only=0"); err != nil {
		return fmt.Errorf("SnapshotDatabase: enable destination writes: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "VACUUM INTO ?", tmpPath); err != nil {
		return fmt.Errorf("SnapshotDatabase: vacuum: %w", err)
	}
	// VACUUM INTO 不负责同步目标文件；先落盘，再让调用方提交引用它的状态。
	// SQLite does not fsync VACUUM INTO output; persist it before publishing receipts.
	f, err := os.Open(tmpPath)
	if err != nil {
		return fmt.Errorf("SnapshotDatabase: open completed snapshot: %w", err)
	}
	syncErr := f.Sync()
	closeErr := f.Close()
	if syncErr != nil {
		return fmt.Errorf("SnapshotDatabase: sync snapshot: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("SnapshotDatabase: close snapshot: %w", closeErr)
	}
	if err := os.Link(tmpPath, abs); err != nil {
		return fmt.Errorf("SnapshotDatabase: publish destination without overwrite: %w", err)
	}
	dir, err := os.Open(filepath.Dir(abs))
	if err != nil {
		os.Remove(abs)
		return fmt.Errorf("SnapshotDatabase: open output directory: %w", err)
	}
	syncErr = dir.Sync()
	closeErr = dir.Close()
	if syncErr != nil || closeErr != nil {
		os.Remove(abs)
		return fmt.Errorf("SnapshotDatabase: sync output directory: sync=%v close=%v", syncErr, closeErr)
	}
	return nil
}
