package kbbundle

import (
	"context"
	"os"
	"path/filepath"

	"github.com/xcosmosbox/cairn/core/storage"
)

// SnapshotSQLite 复用 storage 的一致只读快照，原子替换当前调用拥有的输出文件。
// SnapshotSQLite publishes a standalone snapshot including committed WAL changes.
func SnapshotSQLite(ctx context.Context, src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(dst), ".bundle-snapshot-*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	defer os.Remove(name)
	if err := storage.SnapshotDatabase(ctx, src, name); err != nil {
		return err
	}
	file, err := os.Open(name)
	if err != nil {
		return err
	}
	err = file.Sync()
	file.Close()
	if err != nil {
		return err
	}
	if err := os.Rename(name, dst); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(dst))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func actualSchema(path string) (int, error) {
	db, err := storage.OpenReadOnly(path)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	if err := storage.ValidateQueryContract(context.Background(), db); err != nil {
		return 0, err
	}
	return db.SchemaVersion()
}
