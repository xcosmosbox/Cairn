package fakeforge

import (
	"context"
	"strings"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/controller/githubapp"
)

func TestExistingRepositoryReregistersWithoutReinitializing(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	first := New()
	path, err := first.CreateRepo(base, "synthetic", "source", "main")
	if err != nil {
		t.Fatal(err)
	}
	sha, err := first.GetBranchSHA(context.Background(), "synthetic", "source", "main")
	if err != nil {
		t.Fatal(err)
	}
	// Distinct parent guarantees new history even when both calls happen within one second.
	tree, err := gitOutput(path, "rev-parse", sha+"^{tree}")
	if err != nil {
		t.Fatal(err)
	}
	nextCommit, err := gitOutput(path, "-c", "user.name=test", "-c", "user.email=test@local", "commit-tree", strings.TrimSpace(tree), "-p", sha, "-m", "later fixture")
	if err != nil {
		t.Fatal(err)
	}
	sha = strings.TrimSpace(nextCommit)
	if err := runGit(path, "update-ref", "refs/heads/main", sha); err != nil {
		t.Fatal(err)
	}
	second := New()
	other, err := second.CreateRepo(base, "synthetic", "source", "main")
	if err != nil {
		t.Fatal(err)
	}
	next, err := second.GetBranchSHA(context.Background(), "synthetic", "source", "main")
	if err != nil {
		t.Fatal(err)
	}
	if path != other || sha != next {
		t.Fatal("re-registration modified repository history")
	}
}

func TestUnregisteredPullRequestReturnsError(t *testing.T) {
	t.Parallel()
	_, err := New().EnsurePullRequest(context.Background(), githubapp.PullRequestSpec{Owner: "unknown", Repo: "missing", HeadBranch: "head"})
	if err == nil {
		t.Fatal("unknown repo accepted")
	}
}
