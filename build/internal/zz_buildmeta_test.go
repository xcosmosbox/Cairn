package buildmeta

import (
	"testing"

	buildversion "github.com/xcosmosbox/cairn/core/version"
)

func TestSourceIdentityIncludesActualBuilderCodeWithAndWithoutVCS(t *testing.T) {
	first, second := "sha256:first", "sha256:second"
	if sourceIdentity("same-vcs-revision", first) == sourceIdentity("same-vcs-revision", second) {
		t.Fatal("dirty code changes reused VCS-only identity")
	}
	if sourceIdentity("", first) != first || sourceIdentity("same-vcs-revision", first) != sourceIdentity("same-vcs-revision", first) {
		t.Fatal("identity is not deterministic")
	}
	version, identity := BuilderVersion()
	if identity == "" {
		t.Fatal("builder identity missing without build VCS")
	}
	if version != buildversion.Current() || identity != sourceIdentity(buildversion.Info().Revision, SourceDigest("")) {
		t.Fatalf("shared version masked builder source identity: version=%s identity=%s", version, identity)
	}
}
