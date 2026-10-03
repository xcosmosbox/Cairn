package workspace

import (
	"context"
	"github.com/xcosmosbox/cairn/build/internal/controller/store"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 原实现只在首次 clone 注入 token，fetch 无鉴权；失败还把认证 header 放进错误。
func TestRemoteCommandsUseCurrentCredentialWithoutLeaking(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "git.log")
	shim := `#!/bin/sh
printf '%s\n' "$*" "$GIT_CONFIG_COUNT" "$GIT_CONFIG_KEY_0" "$GIT_CONFIG_VALUE_0" >> "$CAIRN_GIT_SHIM_LOG"
exit "${CAIRN_GIT_SHIM_EXIT:-0}"
`
	if err := os.WriteFile(filepath.Join(root, "git"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CAIRN_GIT_SHIM_LOG", logPath)
	m := New(filepath.Join(root, "ws"))
	ctx := context.Background()
	if err := m.EnsureMirror(ctx, "repo", "https://example.invalid/source.git", "dummy-first"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(m.MirrorPath("repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.MirrorPath("repo"), "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.EnsureMirror(ctx, "repo", "https://example.invalid/source.git", "dummy-rotated"); err != nil {
		t.Fatal(err)
	}
	if err := m.PushBranch(ctx, "repo", "https://example.invalid/source.git", "demo", "dummy-push"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 12 {
		t.Fatalf("unexpected command log: %q", data)
	}
	for i, token := range []string{"dummy-first", "dummy-rotated", "dummy-push"} {
		if lines[i*4+1] != "1" || lines[i*4+2] != "http.extraheader" || lines[i*4+3] != "Authorization: basic "+basicAuth("x-access-token", token) {
			t.Fatalf("command %d lost current credential", i)
		}
		if strings.Contains(lines[i*4], token) || strings.Contains(lines[i*4], basicAuth("x-access-token", token)) {
			t.Fatalf("credential exposed in argv")
		}
		if !strings.Contains(lines[i*4], "core.hooksPath="+os.DevNull) {
			t.Fatalf("command %d inherited Git hooks", i)
		}
	}
	if !strings.Contains(lines[4], "fetch --no-tags --prune origin") {
		t.Fatalf("expected authenticated fetch: %q", lines[4])
	}
	t.Setenv("CAIRN_GIT_SHIM_EXIT", "1")
	err = m.EnsureMirror(ctx, "repo", "https://example.invalid/source.git", "dummy-failing")
	if err == nil {
		t.Fatal("failed fetch accepted")
	}
	if strings.Contains(err.Error(), "dummy-failing") || strings.Contains(err.Error(), basicAuth("x-access-token", "dummy-failing")) {
		t.Fatalf("credential leaked through failure")
	}
}

func TestRemoteCredentialNotPersisted(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	if out, err := exec.Command("git", "init", "--bare", remote).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-b", "main"}, {"-c", "user.name=test", "-c", "user.email=test@local", "commit", "--allow-empty", "-m", "fixture"}, {"push", remote, "main"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = source
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if out, err := exec.Command("git", "-C", remote, "symbolic-ref", "HEAD", "refs/heads/main").CombinedOutput(); err != nil {
		t.Fatalf("git HEAD: %v: %s", err, out)
	}
	m := New(filepath.Join(root, "ws"))
	if err := m.EnsureMirror(context.Background(), "repo", remote, "dummy-current"); err != nil {
		t.Fatal(err)
	}
	if err := m.EnsureMirror(context.Background(), "repo", remote, "dummy-next"); err != nil {
		t.Fatal(err)
	}
	config, err := os.ReadFile(filepath.Join(m.MirrorPath("repo"), "config"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"extraheader", "hookspath", "dummy-current", "dummy-next", basicAuth("x-access-token", "dummy-current")} {
		if strings.Contains(strings.ToLower(string(config)), strings.ToLower(forbidden)) {
			t.Fatalf("credential config persisted: %s", forbidden)
		}
	}
}

func TestCredentialBearingRemoteRejectedBeforePersistence(t *testing.T) {
	t.Parallel()
	m := New(t.TempDir())
	err := m.EnsureMirror(context.Background(), "repo", "https://user:dummy-url-secret@example.invalid/repo.git", "dummy-token")
	if err == nil || strings.Contains(err.Error(), "dummy-url-secret") {
		t.Fatal("credential URL accepted or leaked")
	}
	if _, err := os.Stat(m.MirrorPath("repo")); !os.IsNotExist(err) {
		t.Fatal("invalid remote created mirror")
	}
}

func TestWorkspaceRejectsLostLeaseBeforeFilesystemAndGitMutation(t *testing.T) {
	root := t.TempDir()
	log := filepath.Join(root, "called")
	shim := "#!/bin/sh\ntouch " + log + "\n"
	if err := os.WriteFile(filepath.Join(root, "git"), []byte(shim), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	journal, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	if won, err := journal.AcquireLease(context.Background(), "repo", "owner", time.Minute); err != nil || !won {
		t.Fatal(err)
	}
	ctx := journal.WithRepoLease(context.Background(), "repo", "owner")
	if err := journal.ReleaseLease(context.Background(), "repo", "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.AcquireLease(context.Background(), "repo", "successor", time.Minute); err != nil {
		t.Fatal(err)
	}
	wsRoot := filepath.Join(root, "workspaces")
	m := New(wsRoot)
	if err := m.EnsureMirror(ctx, "repo", "https://example.invalid/source.git", "dummy-token"); err == nil {
		t.Fatal("clone after takeover")
	}
	if err := m.PushBranchTo(ctx, "repo", "https://example.invalid/source.git", "bot", "main", "dummy-token"); err == nil {
		t.Fatal("push after takeover")
	}
	if _, err := m.CheckoutWorktree(ctx, "repo", "run", "head", ""); err == nil {
		t.Fatal("checkout after takeover")
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("git executed after takeover")
	}
	if _, err := os.Stat(wsRoot); !os.IsNotExist(err) {
		t.Fatal("workspace changed after takeover")
	}
}
