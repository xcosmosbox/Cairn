package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/xcosmosbox/cairn/build/internal/controller/githubapp"
	"github.com/xcosmosbox/cairn/build/internal/controller/model"
	"github.com/xcosmosbox/cairn/core/dkconfig"
	"github.com/xcosmosbox/cairn/core/kbbundle"
)

// PR merged 只是工作流事实，不能证明 stable.json 未被人工改成其他内容。
// 核对实际合并的 catalog 文件后才能提升本地 stable 指针。
// mergedCatalogMatches checks the authoritative merged catalog against the immutable candidate.
func (r *Reconciler) mergedCatalogMatches(ctx context.Context, run *model.Run, repoCfg dkconfig.RepoConfig, cand *model.Candidate) (bool, error) {
	if cand == nil || cand.BundlePath == "" || cand.BundleDigest == "" {
		return false, fmt.Errorf("reconcile: stable promotion has no complete candidate")
	}
	expected, err := kbbundle.Verify(cand.BundlePath)
	if err != nil {
		return false, err
	}
	if expected.BundleDigest != cand.BundleDigest || expected.SourceCommit != run.DesiredSourceSHA {
		return false, fmt.Errorf("reconcile: candidate source/digest does not match run")
	}
	ref := githubapp.RepoRef{Owner: parseOwner(r.cfg.Catalog.Repo), Name: parseName(r.cfg.Catalog.Repo)}
	sha, err := githubapp.ReadBranchSHA(ctx, r.forge, ref.Owner, ref.Name, r.cfg.Catalog.Branch)
	if err != nil {
		return false, err
	}
	token, _, err := r.forge.GetInstallToken(ctx)
	if err != nil {
		return false, err
	}
	remote := r.forge.RemoteURL(ref.Owner, ref.Name)
	if err := r.ws.EnsureMirror(ctx, "catalog", remote, token); err != nil {
		return false, err
	}
	wt, err := r.ws.CheckoutWorktree(ctx, "catalog", run.RunID+"-catalog-verify", sha, "")
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(filepath.Join(wt, r.cfg.Catalog.ManifestDir, repoCfg.KGGroup, "stable.json"))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var actual kbbundle.CatalogManifest
	if err := json.Unmarshal(data, &actual); err != nil {
		return false, nil
	}
	return actual.Channel == "stable" && actual.ReleaseTag == cand.ReleaseTag && actual.AssetName == "bundle.tar.gz" && actual.Manifest == *expected, nil
}
