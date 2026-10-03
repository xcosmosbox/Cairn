// Package workspace 管理 cairnd 的 Git 镜像、worktree 与受管文件 tree digest（§8）。
//
// 设计：
//   - bare mirror + 每轮独立 worktree，避免 daemon 等待 PR 时长期持有脏目录。
//   - 所有 git 命令经 exec.CommandContext，支持 context cancel 与超时。
//   - 禁止把 token 放进日志或持久化 remote URL；token 经临时环境配置注入。
//   - 禁止执行源仓库脚本/Makefile/workflow/hook；默认不拉 submodule。
//   - 受管 worktree digest 用 Git tree 或 canonical 文件清单计算，只含受管 Markdown/sidecar/_shared。
package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/xcosmosbox/cairn/build/internal/controller/store"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Manager 管理 workspaces 目录下的镜像与 worktree。
type Manager struct {
	baseDir string // workspaces 根目录
}

// New 创建 Manager。baseDir 为 workspaces 根（如 /var/lib/cairnd/workspaces）。
func New(baseDir string) *Manager {
	return &Manager{baseDir: baseDir}
}

// MirrorPath 返回 repo 的 bare mirror 路径。
func (m *Manager) MirrorPath(repoID string) string {
	return filepath.Join(m.baseDir, "mirrors", repoID+".git")
}

// RunDir 返回某 run 的工作目录根。
func (m *Manager) RunDir(runID string) string {
	return filepath.Join(m.baseDir, "runs", runID)
}

// SourceWorktree 返回某 run 的 source worktree 路径。
func (m *Manager) SourceWorktree(runID string) string {
	return filepath.Join(m.RunDir(runID), "source")
}

// CandidateWorktree 返回某 run 的 candidate worktree 路径。
func (m *Manager) CandidateWorktree(runID string) string {
	return filepath.Join(m.RunDir(runID), "candidate")
}

// ReportsDir 返回某 run 的报告目录。
func (m *Manager) ReportsDir(runID string) string {
	return filepath.Join(m.RunDir(runID), "reports")
}

// EnsureMirror 克隆或更新 bare mirror。
// remoteURL 是 https 远端；token 经临时环境配置注入（不持久化）。
func (m *Manager) EnsureMirror(ctx context.Context, repoID, remoteURL, token string) error {
	if err := store.CheckLease(ctx); err != nil {
		return err
	}
	if err := validateRemoteCredentials(remoteURL); err != nil {
		return err
	}
	mp := m.MirrorPath(repoID)
	if _, err := os.Stat(filepath.Join(mp, "HEAD")); err == nil {
		// 已存在：fetch 更新 + prune 已删除的远端分支。
		return m.authenticatedGit(ctx, mp, token, "fetch", "--no-tags", "--prune", "origin")
	}
	if err := os.MkdirAll(filepath.Dir(mp), 0o755); err != nil {
		return err
	}
	// clone --bare。token 通过临时环境配置注入，不写入 remote URL。
	return m.authenticatedGit(ctx, filepath.Dir(mp), token, "clone", "--bare", remoteURL, mp)
}

// CheckoutWorktree 在 runDir 下创建指向 sha 的 worktree。
func (m *Manager) CheckoutWorktree(ctx context.Context, repoID, runID, sha, branch string) (string, error) {
	if err := store.CheckLease(ctx); err != nil {
		return "", err
	}
	mp := m.MirrorPath(repoID)
	wt := m.SourceWorktree(runID)
	// 重试场景：清理可能残留的旧 worktree（目录 + mirror 中的注册）。
	if _, err := os.Stat(wt); err == nil {
		if err := os.RemoveAll(wt); err != nil {
			return "", err
		}
	}
	if err := m.git(ctx, mp, "worktree", "prune"); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		return "", err
	}
	// git worktree add <wt> <sha>。
	if err := m.git(ctx, mp, "worktree", "add", "--detach", wt, sha); err != nil {
		return "", err
	}
	// 在 worktree 中创建/切换到 bot 分支（便于 push）。
	if branch != "" {
		if err := m.git(ctx, wt, "checkout", "-b", branch); err != nil {
			return "", err
		}
	}
	return wt, nil
}

// CommitAll 在 worktree 中提交所有变更。
func (m *Manager) CommitAll(ctx context.Context, worktree, message, authorName, authorEmail string) error {
	if err := m.git(ctx, worktree, "add", "-A"); err != nil {
		return err
	}
	// 若无变更，git commit 会失败；先检查。
	if changed, err := m.hasChanges(ctx, worktree); err != nil {
		return err
	} else if !changed {
		return ErrNoChanges
	}
	return m.git(ctx, worktree,
		"-c", "user.name="+authorName, "-c", "user.email="+authorEmail,
		"commit", "-m", message)
}

// PushBranch 推送分支到远端（经 token credential helper）。
func (m *Manager) PushBranch(ctx context.Context, repoID, remoteURL, branch, token string) error {
	if err := validateRemoteCredentials(remoteURL); err != nil {
		return err
	}
	mp := m.MirrorPath(repoID)
	// 从 mirror（bare repo）push：worktree 的 commit 和 branch ref 都在 mirror 的共享 git dir 中。
	return m.authenticatedGit(ctx, mp, token, "push", remoteURL, "refs/heads/"+branch+":refs/heads/"+branch)
}

// HeadSHA 返回 worktree 当前 HEAD 的 SHA。
func (m *Manager) HeadSHA(ctx context.Context, worktree string) (string, error) {
	out, err := m.output(ctx, worktree, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// MergeBase 返回两个 ref 的 merge-base。
func (m *Manager) MergeBase(ctx context.Context, worktree, a, b string) (string, error) {
	out, err := m.output(ctx, worktree, "merge-base", a, b)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// BranchSHA 从 mirror 查询分支 SHA。
func (m *Manager) BranchSHA(ctx context.Context, repoID, branch string) (string, error) {
	out, err := m.output(ctx, m.MirrorPath(repoID), "rev-parse", "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// ErrNoChanges 表示 worktree 无可提交变更。
var ErrNoChanges = fmt.Errorf("workspace: 无可提交变更")

// DiffStat 返回 worktree 相对于 baseSHA 的文件变更摘要（git diff --stat 格式）。
func (m *Manager) DiffStat(ctx context.Context, worktree, baseSHA string) (string, error) {
	out, err := m.output(ctx, worktree, "diff", "--stat", baseSHA, "HEAD")
	if err != nil {
		// baseSHA 可能不在 worktree 中（detached HEAD），用 diff --cached --stat 代替。
		out, err = m.output(ctx, worktree, "diff", "--stat", "HEAD")
		if err != nil {
			return "", err
		}
	}
	return strings.TrimSpace(out), nil
}

// CreateBranchInWorktree 在 worktree 中创建并切换到新分支。
func (m *Manager) CreateBranchInWorktree(ctx context.Context, worktree, branch string) error {
	return m.git(ctx, worktree, "checkout", "-b", branch)
}

func (m *Manager) hasChanges(ctx context.Context, worktree string) (bool, error) {
	out, err := m.output(ctx, worktree, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// ─── 受管文件 tree digest ───────────────────────────────────────

// ManagedDigest 计算受管文件的 canonical digest：只包含受管 Markdown、sidecar
// (.kg.yaml) 与 _shared 目录，按相对路径升序，内容为 (path \n sha256(content))。
// 不含 mtime、临时文件、DB、日志或绝对路径（§8）。
func ManagedDigest(root string) (string, error) {
	var entries []fileEntry
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !isManagedFile(rel) {
			return nil
		}
		d, err := digestFile(path)
		if err != nil {
			return err
		}
		entries = append(entries, fileEntry{rel, d})
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	h := sha256.New()
	for _, e := range entries {
		fmt.Fprintf(h, "%s\n%s\n", e.path, e.digest)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

type fileEntry struct {
	path   string
	digest string
}

// isManagedFile 判断相对路径是否为受管文件：.md / .kg.yaml / _shared 下的文件。
func isManagedFile(rel string) bool {
	// _shared 目录下全部受管。
	if strings.HasPrefix(rel, "_shared/") {
		return true
	}
	// Markdown 文档。
	if strings.HasSuffix(rel, ".md") {
		return true
	}
	// sidecar。
	if strings.HasSuffix(rel, ".kg.yaml") || strings.HasSuffix(rel, ".md.kg.yaml") {
		return true
	}
	return false
}

func digestFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:]), nil
}

// ─── git 执行 ────────────────────────────────────────────────────

func (m *Manager) git(ctx context.Context, dir string, args ...string) error {
	_, err := m.run(ctx, dir, args, true)
	return err
}

func (m *Manager) output(ctx context.Context, dir string, args ...string) (string, error) {
	return m.run(ctx, dir, args, false)
}

// URL 中的 HTTPS 用户信息会落进 remote config 和错误，因此必须由独立 token 参数提供。
// Reject credential-bearing HTTP URLs before spawning Git or persisting remote config.
func validateRemoteCredentials(remoteURL string) error {
	if strings.HasPrefix(remoteURL, "http://") || strings.HasPrefix(remoteURL, "https://") {
		u, err := url.Parse(remoteURL)
		if err != nil {
			return fmt.Errorf("workspace: invalid HTTP remote URL")
		}
		if u.User != nil {
			return fmt.Errorf("workspace: pass HTTP credentials as token, not in remote URL")
		}
	}
	return nil
}

// authenticatedGit 为每次 clone/fetch/push 注入本次 token，避免后续 fetch 丢失鉴权。
// Authentication is command scoped: no token enters argv, errors or repository config.
func (m *Manager) authenticatedGit(ctx context.Context, dir, token string, args ...string) error {
	_, err := m.runWithToken(ctx, dir, args, true, token)
	return err
}

func (m *Manager) run(ctx context.Context, dir string, args []string, suppress bool) (string, error) {
	return m.runWithToken(ctx, dir, args, suppress, "")
}

func (m *Manager) runWithToken(ctx context.Context, dir string, args []string, suppress bool, token string) (string, error) {
	if err := store.CheckLease(ctx); err != nil {
		return "", err
	}
	// 仓库或全局配置中的 commit/push hook 会执行任意脚本；用最高优先级临时
	// 配置关闭每个 Manager 命令的 hooks，不能仅靠非交互和 LFS 环境变量。
	// Disable inherited hooks for every command without persisting configuration.
	commandArgs := append([]string{"-c", "core.hooksPath=" + os.DevNull}, args...)
	cmd := exec.CommandContext(ctx, "git", commandArgs...)
	if dir != "" {
		cmd.Dir = dir
	}
	// 安全：禁止 hooks / 不执行源仓库脚本。
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_LFS_SKIP_SMUDGE=1",
	)
	if token != "" {
		// Git 的命令级环境配置不会写入 clone 后的 config，也不会进入错误中的命令参数。
		// Environment configuration is temporary; rotating credentials apply to every fetch.
		cmd.Env = append(cmd.Env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraheader",
			"GIT_CONFIG_VALUE_0=Authorization: basic "+basicAuth("x-access-token", token))
	}
	if err := store.CheckLease(ctx); err != nil {
		return "", err
	}
	var out []byte
	var err error
	if suppress {
		cmd.Stdout = nil
		cmd.Stderr = nil
		err = cmd.Run()
	} else {
		out, err = cmd.Output()
	}
	if err != nil {
		command := strings.Join(args, " ")
		if token != "" {
			command = strings.ReplaceAll(command, token, "[redacted]")
			command = strings.ReplaceAll(command, basicAuth("x-access-token", token), "[redacted]")
		}
		return "", fmt.Errorf("git %s: %w", command, err)
	}
	return string(out), nil
}

// basicAuth 返回 base64(user:pass)。
func basicAuth(user, token string) string {
	return b64(user + ":" + token)
}

// CleanupRun 清理某 run 的 worktree 与临时文件（已终结且过保留期）。
func (m *Manager) CleanupRun(runID string) error {
	rd := m.RunDir(runID)
	// 先 prune worktree。
	mp := filepath.Dir(filepath.Dir(rd)) // mirrors 在 runs 同级
	_ = mp
	return os.RemoveAll(rd)
}

// b64 是 base64 标准编码（避免引入额外 import 行号混乱）。
func b64(s string) string {
	const tbl = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	b := []byte(s)
	var out strings.Builder
	for i := 0; i < len(b); i += 3 {
		var n uint32
		var cnt int
		for j := 0; j < 3 && i+j < len(b); j++ {
			n = n<<8 | uint32(b[i+j])
			cnt++
		}
		n <<= uint((3 - cnt) * 8)
		out.WriteByte(tbl[(n>>18)&0x3f])
		out.WriteByte(tbl[(n>>12)&0x3f])
		if cnt > 1 {
			out.WriteByte(tbl[(n>>6)&0x3f])
		} else {
			out.WriteByte('=')
		}
		if cnt > 2 {
			out.WriteByte(tbl[n&0x3f])
		} else {
			out.WriteByte('=')
		}
	}
	return out.String()
}

// touch 确保目录存在。
var _ = time.Second

// EnsureBranchInWorktree 重入已经提交/推送的同一 worktree 时保留已生成提交，
// 不重建分支或改变提交时间。已有其他提交的分支则拒绝覆盖。
// EnsureBranchInWorktree preserves an existing matching branch on replay.
func (m *Manager) EnsureBranchInWorktree(ctx context.Context, worktree, branch string) error {
	current, err := m.output(ctx, worktree, "symbolic-ref", "--short", "-q", "HEAD")
	if err == nil && strings.TrimSpace(current) == branch {
		return nil
	}
	head, err := m.HeadSHA(ctx, worktree)
	if err != nil {
		return err
	}
	existing, err := m.output(ctx, worktree, "rev-parse", "--verify", "refs/heads/"+branch)
	if err == nil {
		if strings.TrimSpace(existing) != head {
			return fmt.Errorf("workspace: branch %s already has a different commit", branch)
		}
		return m.git(ctx, worktree, "checkout", branch)
	}
	return m.git(ctx, worktree, "checkout", "-b", branch)
}

// PushBranchTo pushes an immutable local branch to the configured target ref.
func (m *Manager) PushBranchTo(ctx context.Context, repoID, remoteURL, branch, targetBranch, token string) error {
	if err := validateRemoteCredentials(remoteURL); err != nil {
		return err
	}
	return m.authenticatedGit(ctx, m.MirrorPath(repoID), token, "push", remoteURL, "refs/heads/"+branch+":refs/heads/"+targetBranch)
}
