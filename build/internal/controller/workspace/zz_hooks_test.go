package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 原执行器只设置非交互/LFS 环境变量，commit 和 push 仍会执行继承的 Git hooks。
// Real hooks must remain disabled across global, mirror and per-worktree configuration.
func TestManagerCommandsDisableInheritedGitHooks(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	sentinel := filepath.Join(root, "hook-executed")
	t.Setenv("CAIRN_HOOK_SENTINEL", sentinel)
	globalHooks := filepath.Join(root, "global-hooks")
	writeHookSentinels(t, globalHooks)
	remote := filepath.Join(root, "remote.git")
	source := filepath.Join(root, "source")
	hooksGit(t, root, "init", "--bare", remote)
	hooksGit(t, root, "init", "-b", "main", source)
	hooksGit(t, source, "-c", "user.name=fixture", "-c", "user.email=fixture@local", "commit", "--allow-empty", "-m", "fixture")
	hooksGit(t, source, "push", remote, "main")
	hooksGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")
	// 先让真实 Git 执行相同脚本，避免只证明测试 hook 本身未安装或不可执行。
	// Calibrate the sentinels with real commits and a push before the managed calls.
	hooksGit(t, source, "-c", "core.hooksPath="+globalHooks, "-c", "user.name=fixture", "-c", "user.email=fixture@local", "commit", "--allow-empty", "-m", "hook calibration")
	hooksGit(t, source, "-c", "core.hooksPath="+globalHooks, "push", remote, "main")
	data, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"pre-commit", "post-commit", "pre-push"} {
		if !strings.Contains(string(data), name) {
			t.Fatalf("sentinel hook %s was not executable", name)
		}
	}
	if err := os.Remove(sentinel); err != nil {
		t.Fatal(err)
	}
	globalConfig := filepath.Join(root, "gitconfig")
	hooksGit(t, root, "config", "--file", globalConfig, "core.hooksPath", globalHooks)
	t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)

	for _, location := range []string{"global", "mirror", "worktree"} {
		t.Run(location, func(t *testing.T) {
			m := New(filepath.Join(t.TempDir(), "workspaces"))
			if err := m.EnsureMirror(ctx, "repo", remote, "dummy-token"); err != nil {
				t.Fatal(err)
			}
			branch := "bot-" + location
			sha := strings.TrimSpace(hooksGit(t, remote, "rev-parse", "main"))
			wt, err := m.CheckoutWorktree(ctx, "repo", "run", sha, branch)
			if err != nil {
				t.Fatal(err)
			}
			switch location {
			case "mirror":
				mirrorHooks := filepath.Join(m.MirrorPath("repo"), "hooks")
				writeHookSentinels(t, mirrorHooks)
				hooksGit(t, m.MirrorPath("repo"), "config", "core.hooksPath", mirrorHooks)
			case "worktree":
				worktreeHooks := filepath.Join(root, "worktree-hooks")
				writeHookSentinels(t, worktreeHooks)
				hooksGit(t, m.MirrorPath("repo"), "config", "extensions.worktreeConfig", "true")
				hooksGit(t, wt, "config", "--worktree", "core.bare", "false")
				hooksGit(t, wt, "config", "--worktree", "core.hooksPath", worktreeHooks)
			}
			if err := os.WriteFile(filepath.Join(wt, "managed.md"), []byte("managed commit\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := m.CommitAll(ctx, wt, "managed writeback", "cairnd", "cairnd@local"); err != nil {
				t.Fatal(err)
			}
			if err := m.PushBranch(ctx, "repo", remote, branch, "dummy-token"); err != nil {
				t.Fatal(err)
			}
			head, err := m.HeadSHA(ctx, wt)
			if err != nil {
				t.Fatal(err)
			}
			if pushed := strings.TrimSpace(hooksGit(t, remote, "rev-parse", "refs/heads/"+branch)); pushed != head {
				t.Fatalf("hook suppression prevented normal Git publication: %s != %s", pushed, head)
			}
			if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
				t.Fatalf("managed %s command executed a hook: %v", location, err)
			}
		})
	}
}

func writeHookSentinels(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"pre-commit", "post-commit", "pre-push"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nprintf '%s\\n' \"$0\" >> \"$CAIRN_HOOK_SENTINEL\"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func hooksGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}
