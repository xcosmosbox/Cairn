package githubapp

import (
	"context"
	"fmt"
	"strings"
)

// ValidateBranchSHA 将缺失的远端事实视为读取失败，不能把空值当作分支变化或 checkout 默认值。
// A missing branch fact is a read failure, never evidence of a changed source or a checkout default.
func ValidateBranchSHA(sha string) error {
	if strings.TrimSpace(sha) == "" {
		return fmt.Errorf("githubapp: branch response has no commit SHA")
	}
	return nil
}

// ReadBranchSHA 在 Forge 抽象边界统一验证 SHA，包含 fake/custom Forge 返回的空成功结果。
// Validate every business branch read, even when a custom Forge reports success with an empty value.
func ReadBranchSHA(ctx context.Context, forge Forge, owner, repo, branch string) (string, error) {
	sha, err := forge.GetBranchSHA(ctx, owner, repo, branch)
	if err != nil {
		return "", err
	}
	if err := ValidateBranchSHA(sha); err != nil {
		return "", fmt.Errorf("githubapp: branch %s/%s@%s: %w", owner, repo, branch, err)
	}
	return sha, nil
}
