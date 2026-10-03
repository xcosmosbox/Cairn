package repoidentity

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCanonicalGitTransports(t *testing.T) {
	t.Parallel()
	for _, remote := range []string{
		"https://github.com/Owner/Repo.git",
		"https://dummy:password@github.com/Owner/Repo.git/",
		"git@github.com:Owner/Repo.git",
		"ssh://git@github.com:22/Owner/Repo.git",
	} {
		got, err := CanonicalGitURL(remote)
		if err != nil || got != "git:github.com/owner/repo" {
			t.Fatalf("remote=%s identity=%s err=%v", remote, got, err)
		}
	}
}

// Ephemeral checkout paths previously gave every new worktree a new partition.
func TestGitOriginSurvivesRelocation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	repo := filepath.Join(dir, "original")
	if err := os.Mkdir(repo, 0755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", repo}, {"-C", repo, "remote", "add", "origin", "git@github.com:Owner/Repo.git"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	first, err := Resolve(context.Background(), repo, "")
	if err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(dir, "moved")
	if err := os.Rename(repo, moved); err != nil {
		t.Fatal(err)
	}
	second, err := Resolve(context.Background(), moved, "")
	if err != nil || first != second {
		t.Fatalf("identity changed: %q %q err=%v", first, second, err)
	}
}

func TestLocalIdentityRequiresMarkerAndSurvivesRelocation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	repo := filepath.Join(dir, "original")
	if err := os.Mkdir(repo, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(context.Background(), repo, ""); !errors.Is(err, ErrNoIdentity) {
		t.Fatalf("unproven ownership accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, MarkerName)); !os.IsNotExist(err) {
		t.Fatal("readonly resolution created marker")
	}
	first, err := Ensure(context.Background(), repo, "")
	if err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(dir, "moved")
	if err := os.Rename(repo, moved); err != nil {
		t.Fatal(err)
	}
	second, err := Ensure(context.Background(), moved, "")
	if err != nil || first != second {
		t.Fatalf("identity changed: %q %q err=%v", first, second, err)
	}
	if err := os.WriteFile(filepath.Join(moved, MarkerName), []byte("bad marker"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Ensure(context.Background(), moved, ""); err == nil {
		t.Fatal("corrupt marker silently replaced")
	}
}

func TestInvalidOriginIsNotReplacedByLocalIdentity(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	for _, args := range [][]string{{"init", repo}, {"-C", repo, "remote", "add", "origin", "ssh://example.com/"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	if _, err := Ensure(context.Background(), repo, ""); err == nil {
		t.Fatal("invalid origin accepted")
	}
	if _, err := os.Stat(filepath.Join(repo, MarkerName)); !os.IsNotExist(err) {
		t.Fatal("invalid origin masked by marker")
	}
}

func TestGitIdentityIncludesNestedScanRoot(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	for _, args := range [][]string{{"init", repo}, {"-C", repo, "remote", "add", "origin", "git@github.com:Owner/Repo.git"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	for _, rel := range []string{"skills/a", "skills/b"} {
		if err := os.MkdirAll(filepath.Join(repo, rel), 0755); err != nil {
			t.Fatal(err)
		}
	}
	root, err := Resolve(context.Background(), repo, "")
	if err != nil {
		t.Fatal(err)
	}
	a, err := Resolve(context.Background(), filepath.Join(repo, "skills/a"), "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Resolve(context.Background(), filepath.Join(repo, "skills/b"), "")
	if err != nil {
		t.Fatal(err)
	}
	if root == a || a == b {
		t.Fatalf("scan roots share ownership: root=%s a=%s b=%s", root, a, b)
	}
}
