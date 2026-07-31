// Package githubapp 实现 GitHub App 认证与 Forge 抽象（产品化 Prompt §4）。
//
// Forge 是窄而有业务意义的接口，覆盖 Source/Catalog 仓库的 Git 引用、PR 与 Release
// 操作。只实现 GitHub Forge，但保留接口供 fake 测试与未来扩展（GitLab/Gitea）。
// Git CLI push 由 workspace 包执行（§8）；本包的 PushBranch 通过 API 创建/更新分支引用，
// 实际的 commit 推送由 workspace 用 git push 完成（token 经 credential helper 注入）。
package githubapp

import (
	"context"
	"time"
)

// RepoRef 标识一个仓库。
type RepoRef struct {
	Owner string
	Name  string
}

// String 返回 owner/name。
func (r RepoRef) String() string { return r.Owner + "/" + r.Name }

// CommitRef 标识一个 commit。
type CommitRef struct {
	Owner  string
	Repo   string
	Branch string
	SHA    string
}

// PullRequestSpec 描述要创建/查找的 PR。
type PullRequestSpec struct {
	Owner      string
	Repo       string
	HeadBranch string // bot 分支
	BaseBranch string
	Title      string
	Body       string
	// RunMarker 是 commit message/PR body 中的机器可解析标记（dk-run-id:...），
	// 用于幂等查找已有 PR（§8）。
	RunMarker string
}

// PullRequest 是 PR 的观测视图。
type PullRequest struct {
	Owner     string
	Repo      string
	Number    int
	HeadBranch string
	HeadSHA   string
	BaseBranch string
	State     string // open | closed | merged
	Merged    bool
	Mergeable bool
	HTMLURL   string
	// MergeCommitSHA 是 GitHub 的 merge_commit_sha。
	// 对已合并 PR：实际 merge commit；对 open PR：GitHub 计算的 test-merge commit。
	// 用于应对 GitHub 最终一致性延迟——当分支 HEAD 已等于此值时，PR 实际已合并
	// （即使 merged 标志因主从复制延迟仍为 false）。
	MergeCommitSHA string
}

// ReleaseSpec 描述要创建的 Release。
type ReleaseSpec struct {
	Owner    string
	Repo     string
	Tag      string
	Target   string // commitish（分支或 SHA）
	Title    string
	Body     string
	Prelease bool
	// AssetDigest 用于不可变约束：若 tag 已存在且 asset 名相同，只有 digest 一致才能复用（§11.3）。
	AssetName     string
	AssetDigest   string
	AssetFilePath string
}

// Release 是 Release 的观测视图。
// JSON tag 对齐 GitHub API 返回字段名（tag_name, prerelease, html_url, assets_url）。
type Release struct {
	Owner    string `json:"-"`
	Repo     string `json:"-"`
	ID       int64  `json:"id"`
	Tag      string `json:"tag_name"`   // GitHub API: tag_name
	Prelease bool   `json:"prerelease"` // GitHub API: prerelease
	HTMLURL  string `json:"html_url"`   // GitHub API: html_url
	AssetURL string `json:"assets_url"` // GitHub API: assets_url
}

// Forge 是 Source/Catalog 仓库操作的窄接口。
type Forge interface {
	// GetBranchSHA 返回目标 branch 的当前 HEAD SHA。
	GetBranchSHA(ctx context.Context, owner, repo, branch string) (string, error)
	// CreateBranch 从 baseSHA 创建新分支（若不存在）。
	CreateBranch(ctx context.Context, owner, repo, branch, baseSHA string) error
	// EnsurePullRequest 创建或复用已有 PR（按 RunMarker 幂等查找）。
	EnsurePullRequest(ctx context.Context, spec PullRequestSpec) (PullRequest, error)
	// GetPullRequest 查询 PR 当前状态。
	GetPullRequest(ctx context.Context, owner, repo string, number int) (PullRequest, error)
	// MergePullRequest 合并 PR（method: merge|squash|rebase）。幂等：已合并返回 nil。
	MergePullRequest(ctx context.Context, owner, repo string, number int, method string) error
	// EnsureRelease 创建或复用 Release（不可变约束：tag+asset digest 一致才复用）。
	EnsureRelease(ctx context.Context, spec ReleaseSpec) (Release, error)
	// GetInstallToken 返回 git push 用的 installation token（供 workspace credential helper）。
	GetInstallToken(ctx context.Context) (string, time.Time, error)
	// RemoteURL 返回 owner/repo 的 git 远端 URL（fake 返回本地 bare 路径，真实返回 https URL）。
	RemoteURL(owner, repo string) string
}
