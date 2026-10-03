// Package repoidentity resolves stable repository ownership without using a
// transient worktree path. 查询/预检只读取身份，只有全量构建允许建立本地身份。
package repoidentity

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

const MarkerName = ".cairn-repository-id"

var ErrNoIdentity = errors.New("no stable repository identity")

// CanonicalGitURL 把 HTTPS、SSH 和 SCP origin 统一为仓库身份；凭据不进入身份。
// CanonicalGitURL normalizes transport differences without retaining credentials.
func CanonicalGitURL(remote string) (string, error) {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return "", fmt.Errorf("repoidentity: empty Git origin")
	}
	if !strings.Contains(remote, "://") && strings.Contains(remote, ":") && !filepath.IsAbs(remote) {
		parts := strings.SplitN(remote, ":", 2)
		remote = "ssh://" + parts[0] + "/" + parts[1]
	}
	u, err := url.Parse(remote)
	if err != nil {
		return "", fmt.Errorf("repoidentity: parse Git origin: %w", err)
	}
	if u.Host != "" {
		host := strings.ToLower(u.Hostname())
		port := u.Port()
		if port != "" && !(u.Scheme == "ssh" && port == "22") && !(u.Scheme == "https" && port == "443") && !(u.Scheme == "http" && port == "80") {
			host += ":" + port
		}
		path := strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
		if path == "" {
			return "", fmt.Errorf("repoidentity: Git origin has no repository path")
		}
		if host == "github.com" {
			path = strings.ToLower(path)
		}
		return "git:" + host + "/" + path, nil
	}
	local := remote
	if u.Scheme == "file" {
		local = u.Path
	} else if u.Scheme != "" {
		return "", fmt.Errorf("repoidentity: unsupported Git origin")
	}
	abs, err := filepath.Abs(local)
	if err != nil {
		return "", err
	}
	return "git-local:" + filepath.ToSlash(filepath.Clean(abs)), nil
}

// Resolve 不生成任何文件。没有 origin、配置身份或持久化本地标记时停止，
// 不能因“没有 sidecar 可比对”而放行错误仓库。
// Resolve is read-only and fails closed when ownership cannot be established.
func Resolve(ctx context.Context, repoPath, explicit string) (string, error) {
	if explicit != "" {
		if strings.TrimSpace(explicit) != explicit || strings.ContainsAny(explicit, "\r\n\x00") {
			return "", fmt.Errorf("repoidentity: invalid configured identity")
		}
		return explicit, nil
	}
	gitRoot, gitErr := exec.CommandContext(ctx, "git", "-C", repoPath, "rev-parse", "--show-toplevel").Output()
	if output, err := exec.CommandContext(ctx, "git", "-C", repoPath, "config", "--local", "--get", "remote.origin.url").Output(); gitErr == nil && err == nil && strings.TrimSpace(string(output)) != "" {
		remote := strings.TrimSpace(string(output))
		if !strings.Contains(remote, ":") && !filepath.IsAbs(remote) {
			remote = filepath.Join(strings.TrimSpace(string(gitRoot)), remote)
		}
		identity, err := CanonicalGitURL(remote)
		if err != nil {
			return "", err
		}
		root, err := filepath.EvalSymlinks(strings.TrimSpace(string(gitRoot)))
		if err != nil {
			return "", fmt.Errorf("repoidentity: Git root: %w", err)
		}
		abs, err := filepath.Abs(repoPath)
		if err != nil {
			return "", err
		}
		abs, err = filepath.EvalSymlinks(abs)
		if err != nil {
			return "", err
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("repoidentity: scan root is outside Git repository")
		}
		// The origin alone does not distinguish scanning the whole repo from a
		// nested skill directory; confusing them turns root paths into deletions.
		if rel != "." {
			identity += "#subdir=" + url.PathEscape(filepath.ToSlash(rel))
		}
		return identity, nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	path := filepath.Join(repoPath, MarkerName)
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("repoidentity: %w; configure --repository-identity or perform a full rebuild", ErrNoIdentity)
		}
		return "", fmt.Errorf("repoidentity: read marker: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > 100 {
		return "", fmt.Errorf("repoidentity: invalid local identity marker")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	identity := strings.TrimSpace(string(data))
	if !strings.HasPrefix(identity, "local:") {
		return "", fmt.Errorf("repoidentity: invalid local identity marker")
	}
	if _, err := uuid.Parse(strings.TrimPrefix(identity, "local:")); err != nil {
		return "", fmt.Errorf("repoidentity: invalid local identity UUID: %w", err)
	}
	return identity, nil
}

// Ensure 只供全量构建建立本地身份；拷贝或搬迁工作树时保留标记即可保持归属。
// Ensure may create a durable marker for a local, non-Git full build.
func Ensure(ctx context.Context, repoPath, explicit string) (string, error) {
	identity, err := Resolve(ctx, repoPath, explicit)
	if err == nil {
		return identity, nil
	}
	if !errors.Is(err, ErrNoIdentity) {
		return "", err
	}
	path := filepath.Join(repoPath, MarkerName)
	if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
		return "", err // Never silently replace a corrupt existing identity.
	}
	identity = "local:" + uuid.NewString()
	f, createErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if os.IsExist(createErr) {
		return Resolve(ctx, repoPath, explicit)
	}
	if createErr != nil {
		return "", fmt.Errorf("repoidentity: create local identity: %w", createErr)
	}
	_, writeErr := f.WriteString(identity + "\n")
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil {
		return "", fmt.Errorf("repoidentity: persist local identity: %w", writeErr)
	}
	if closeErr != nil {
		return "", closeErr
	}
	return identity, nil
}
