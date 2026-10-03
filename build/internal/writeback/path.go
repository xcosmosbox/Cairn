package writeback

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// 文件操作必须留在受管根目录；rename 是原子的，但经过 symlink 父目录仍可能
// 改写仓库外文件。读取和写入前都检查实际目录链，删除也使用同一规则。
// safeRepoPath rejects traversal and symlink components before a managed operation.
func safeRepoPath(root, rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) || strings.Contains(rel, "\\") || filepath.ToSlash(filepath.Clean(rel)) != rel || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("writeback: unsafe repository path %q", rel)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	current := abs
	for _, part := range append([]string{""}, strings.Split(filepath.FromSlash(rel), string(filepath.Separator))...) {
		if part != "" {
			current = filepath.Join(current, part)
		}
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("writeback: symlink repository component: %s", current)
		}
	}
	return filepath.Join(abs, filepath.FromSlash(rel)), nil
}

// ValidateRepositoryPath resolves a confined managed path without creating or changing files.
func ValidateRepositoryPath(root, rel string) (string, error) { return safeRepoPath(root, rel) }
