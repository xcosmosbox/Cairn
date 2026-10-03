package buildmeta

import "testing"

func TestSourceIdentityIncludesActualBuilderCodeWithAndWithoutVCS(t *testing.T) {
	first, second := "sha256:first", "sha256:second"
	if sourceIdentity("same-vcs-revision", first) == sourceIdentity("same-vcs-revision", second) {
		t.Fatal("dirty code changes reused VCS-only identity")
	}
	if sourceIdentity("", first) != first || sourceIdentity("same-vcs-revision", first) != sourceIdentity("same-vcs-revision", first) {
		t.Fatal("identity is not deterministic")
	}
	_, identity := BuilderVersion()
	if identity == "" {
		t.Fatal("builder identity missing without build VCS")
	}
}
