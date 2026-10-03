package reconcile

import (
	"context"
	"testing"

	"github.com/xcosmosbox/cairn/build/internal/controller/fakeforge"
	"github.com/xcosmosbox/cairn/build/internal/controller/model"
)

func TestMissingSourceBranchFactRetriesBeforeMutation(t *testing.T) {
	t.Parallel()
	for _, state := range []model.RunState{model.StateFetchSource, model.StateAwaitSourcePR, model.StateReconcileMergedSource, model.StateUploadCandidate} {
		for _, sha := range []string{"", " \t"} {
			t.Run(string(state)+"/"+sha, func(t *testing.T) {
				f := &recoveryForge{FakeForge: fakeforge.New(), branch: func(context.Context) (string, error) { return sha, nil }}
				r, s := recoverySetup(t, state, f)
				if state == model.StateAwaitSourcePR {
					recoveryPR(t, s, model.PRKindSourceWriteback)
				}
				// 原 UploadCandidate 把空值判为 Stale；Merged 会继续 token/checkout 默认引用。
				// No source fact must retry its checkpoint before credentials, checkout or publication.
				result, err := r.Step(context.Background(), "run")
				if err != nil {
					t.Fatal(err)
				}
				saved, err := s.GetRun(context.Background(), "run")
				if err != nil {
					t.Fatal(err)
				}
				if result.To != model.StateFailedRetryable || saved.State != result.To || saved.RetryState != state || saved.DesiredSourceSHA == "" || f.branchCalls.Load() != 1 {
					t.Fatalf("missing source advanced/lost checkpoint: result=%+v run=%+v reads=%d", result, saved, f.branchCalls.Load())
				}
				repo, err := s.GetRepo(context.Background(), "repo")
				if err != nil {
					t.Fatal(err)
				}
				if repo.LastSeenSourceSHA != "" || repo.LastStableSourceSHA != "" || repo.LastStableBundleDigest != "" {
					t.Fatalf("unknown fact changed source metadata: %+v", repo)
				}
			})
		}
	}
}
