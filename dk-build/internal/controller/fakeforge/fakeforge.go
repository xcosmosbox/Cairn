// Package fakeforge 提供 Forge 接口的本地 fake 实现，用于无真实 GitHub 凭证的测试与 e2e。
//
// 它用本地 bare git 仓库模拟 Source/Catalog repo 的分支/SHA/合并，用内存 map 模拟
// PR 与 Release。同一个 Forge 接口让 reconciler 在 fake 与真实 GitHub 间无缝切换。
package fakeforge

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/xcosmosbox/domain-knowledge-layer/dk-build/internal/controller/githubapp"
)

// FakeForge 是 githubapp.Forge 的本地 fake 实现。
type FakeForge struct {
	mu       sync.Mutex
	repos    map[string]*fakeRepo // key: owner/repo
	prs      map[string]*githubapp.PullRequest // key: owner/repo#number
	prByHead map[string]int                       // key: owner/repo:head → number
	releases map[string]*githubapp.Release       // key: owner/repo:tag
	token    string
}

type fakeRepo struct {
	barePath string // 本地 bare git 仓库路径
}

// New 创建空 FakeForge。
func New() *FakeForge {
	return &FakeForge{
		repos:    map[string]*fakeRepo{},
		prs:      map[string]*githubapp.PullRequest{},
		prByHead: map[string]int{},
		releases: map[string]*githubapp.Release{},
		token:    "fake-install-token",
	}
}

// key 返回 owner/repo。
func key(owner, repo string) string { return owner + "/" + repo }

// CreateRepo 在 baseDir 下创建一个 bare git 仓库，并写入一个初始 commit 到 baseBranch。
// 返回 bare 仓库路径。若已存在则复用。
func (f *FakeForge) CreateRepo(baseDir, owner, repo, baseBranch string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := key(owner, repo)
	if fr, ok := f.repos[k]; ok {
		return fr.barePath, nil
	}
	barePath := filepath.Join(baseDir, owner+"-"+repo+".git")
	if err := os.MkdirAll(filepath.Dir(barePath), 0o755); err != nil {
		return "", err
	}
	// git init --bare（在已存在的 baseDir 下执行，避免 chdir 到不存在的路径）。
	if err := runGit(baseDir, "init", "--bare", barePath); err != nil {
		return "", err
	}
	// 用临时工作树创建初始 commit。
	tmp, err := os.MkdirTemp("", "fakeforge-init-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	if err := runGit(tmp, "init", "-b", baseBranch, tmp); err != nil {
		return "", err
	}
	// 写一个 README 占位。
	if err := os.WriteFile(filepath.Join(tmp, "README.md"), []byte("# "+repo+"\n"), 0o644); err != nil {
		return "", err
	}
	if err := runGit(tmp, "add", "."); err != nil {
		return "", err
	}
	if err := runGit(tmp, "-c", "user.email=fake@dkd.local", "-c", "user.name=dkd-fake",
		"commit", "-m", "initial"); err != nil {
		return "", err
	}
	// push 到 bare。
	if err := runGit(tmp, "remote", "add", "origin", barePath); err != nil {
		return "", err
	}
	if err := runGit(tmp, "push", "origin", baseBranch); err != nil {
		return "", err
	}
	// 设 HEAD。
	runGit(barePath, "symbolic-ref", "HEAD", "refs/heads/"+baseBranch)
	f.repos[k] = &fakeRepo{barePath: barePath}
	return barePath, nil
}

// BarePath 返回某 repo 的 bare 仓库路径（测试用）。
func (f *FakeForge) BarePath(owner, repo string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fr, ok := f.repos[key(owner, repo)]
	if !ok {
		return "", fmt.Errorf("fakeforge: repo %s 未注册", key(owner, repo))
	}
	return fr.barePath, nil
}

// ─── Forge 实现 ─────────────────────────────────────────────────

func (f *FakeForge) GetBranchSHA(ctx context.Context, owner, repo, branch string) (string, error) {
	f.mu.Lock()
	barePath, err := f.barePathLocked(owner, repo)
	f.mu.Unlock()
	if err != nil {
		return "", err
	}
	out, err := gitOutput(barePath, "rev-parse", "refs/heads/"+branch)
	if err != nil {
		return "", fmt.Errorf("fakeforge: 获取分支 %s SHA: %w", branch, err)
	}
	return strings.TrimSpace(out), nil
}

func (f *FakeForge) CreateBranch(ctx context.Context, owner, repo, branch, baseSHA string) error {
	f.mu.Lock()
	barePath, err := f.barePathLocked(owner, repo)
	f.mu.Unlock()
	if err != nil {
		return err
	}
	// 若分支已存在则跳过（幂等）。
	if _, err := gitOutput(barePath, "rev-parse", "--verify", "refs/heads/"+branch); err == nil {
		return nil
	}
	return runGit(barePath, "update-ref", "refs/heads/"+branch, baseSHA)
}

func (f *FakeForge) EnsurePullRequest(ctx context.Context, spec githubapp.PullRequestSpec) (githubapp.PullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := key(spec.Owner, spec.Repo)
	// 幂等：按 head branch 查找已有 PR。
	if num, ok := f.prByHead[k+":"+spec.HeadBranch]; ok {
		pr := f.prs[prKey(k, num)]
		if pr != nil {
			return *pr, nil
		}
	}
	// 也按 RunMarker 在 body 中查找。
	for _, pr := range f.prs {
		if pr.Owner == spec.Owner && pr.Repo == spec.Repo && strings.Contains(pr.HTMLURL, spec.RunMarker) {
			return *pr, nil
		}
	}
	num := len(f.prs) + 1
	for {
		if _, exists := f.prs[prKey(k, num)]; !exists {
			break
		}
		num++
	}
	headSHA, _ := gitOutput(f.repos[k].barePath, "rev-parse", "refs/heads/"+spec.HeadBranch)
	pr := &githubapp.PullRequest{
		Owner:      spec.Owner,
		Repo:       spec.Repo,
		Number:     num,
		HeadBranch: spec.HeadBranch,
		HeadSHA:    strings.TrimSpace(headSHA),
		BaseBranch: spec.BaseBranch,
		State:      "open",
		Mergeable:  true,
		HTMLURL:    fmt.Sprintf("https://fake.github.local/%s/pull/%d#%s", k, num, spec.RunMarker),
	}
	f.prs[prKey(k, num)] = pr
	f.prByHead[k+":"+spec.HeadBranch] = num
	return *pr, nil
}

func (f *FakeForge) GetPullRequest(ctx context.Context, owner, repo string, number int) (githubapp.PullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pr := f.prs[prKey(key(owner, repo), number)]
	if pr == nil {
		return githubapp.PullRequest{}, fmt.Errorf("fakeforge: PR #%d 不存在 (%s)", number, key(owner, repo))
	}
	return *pr, nil
}

func (f *FakeForge) MergePullRequest(ctx context.Context, owner, repo string, number int, method string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := key(owner, repo)
	pr := f.prs[prKey(k, number)]
	if pr == nil {
		return fmt.Errorf("fakeforge: PR #%d 不存在", number)
	}
	if pr.Merged {
		return nil // 幂等。
	}
	barePath := f.repos[k].barePath
	// 合并 head 到 base（bare 仓库用 update-ref 模拟快进；若非快进则创建 merge commit 需工作树）。
	// 简化：把 base 指向 head SHA（快进语义，测试足够）。
	if err := runGit(barePath, "update-ref", "refs/heads/"+pr.BaseBranch, pr.HeadSHA); err != nil {
		return err
	}
	pr.Merged = true
	pr.State = "merged"
	return nil
}

func (f *FakeForge) EnsureRelease(ctx context.Context, spec githubapp.ReleaseSpec) (githubapp.Release, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := key(spec.Owner, spec.Repo)
	rk := k + ":" + spec.Tag
	// 不可变约束（§11.3）：tag 已存在则复用，但 asset digest 必须一致。
	if r, ok := f.releases[rk]; ok {
		if spec.AssetName != "" && r.AssetURL != "" && !strings.Contains(r.AssetURL, spec.AssetDigest) {
			return githubapp.Release{}, fmt.Errorf("fakeforge: tag %s 已存在且 asset digest 不一致（不可覆盖）", spec.Tag)
		}
		return *r, nil
	}
	id := int64(len(f.releases) + 1)
	assetURL := ""
	if spec.AssetName != "" {
		assetURL = fmt.Sprintf("fake-asset://%s/%s/releases/%d/%s#%s",
			k, spec.Tag, id, spec.AssetName, spec.AssetDigest)
	}
	r := &githubapp.Release{
		Owner:    spec.Owner,
		Repo:     spec.Repo,
		ID:       id,
		Tag:      spec.Tag,
		Prelease: spec.Prelease,
		HTMLURL:  fmt.Sprintf("https://fake.github.local/%s/releases/tag/%s", k, spec.Tag),
		AssetURL: assetURL,
	}
	f.releases[rk] = r
	return *r, nil
}

func (f *FakeForge) GetInstallToken(ctx context.Context) (string, time.Time, error) {
	return f.token, time.Now().Add(1 * time.Hour), nil
}

// RemoteURL 返回本地 bare 仓库路径（fake 环境的 git 远端）。
func (f *FakeForge) RemoteURL(owner, repo string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	fr, ok := f.repos[key(owner, repo)]
	if !ok {
		return ""
	}
	return fr.barePath
}

// ─── 内部工具 ────────────────────────────────────────────────────

func (f *FakeForge) barePathLocked(owner, repo string) (string, error) {
	fr, ok := f.repos[key(owner, repo)]
	if !ok {
		return "", fmt.Errorf("fakeforge: repo %s 未注册", key(owner, repo))
	}
	return fr.barePath, nil
}

func prKey(k string, num int) string { return fmt.Sprintf("%s#%d", k, num) }

func runGit(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if dir == "" && len(args) > 0 && args[0] == "init" {
		// init 时 dir 即仓库路径。
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %s: %w", strings.Join(args, " "), string(out), err)
	}
	return nil
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}
