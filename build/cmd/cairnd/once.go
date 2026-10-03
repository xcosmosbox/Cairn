package main

import (
	"context"
	"fmt"
	"log"

	"github.com/xcosmosbox/cairn/build/internal/controller/githubapp"
	"github.com/xcosmosbox/cairn/build/internal/controller/model"
	"github.com/xcosmosbox/cairn/build/internal/controller/reconcile"
	"github.com/xcosmosbox/cairn/build/internal/controller/store"
	"github.com/xcosmosbox/cairn/core/dkconfig"
)

// runOnce 保留错误并检查最终状态；原调度器只记录错误，导致命令失败仍退出 0。
// One-shot CLI errors propagate to its exit status, while legitimate PR waits remain successful.
func runOnce(ctx context.Context, cfg *dkconfig.Config, st *store.Store, forge githubapp.Forge, recon *reconcile.Reconciler, simulateMerges bool) error {
	runs, err := st.ActiveRuns(ctx)
	if err != nil {
		return err
	}
	active := map[string]bool{}
	for _, run := range runs {
		active[run.RepoID] = true
	}
	for _, rc := range cfg.Repos {
		if active[rc.ID] {
			continue
		}
		repo, err := st.GetRepo(ctx, rc.ID)
		if err != nil {
			return err
		}
		if !repo.Enabled {
			continue
		}
		sha, err := githubapp.ReadBranchSHA(ctx, forge, rc.Owner, rc.Name, rc.Branch)
		if err != nil {
			return fmt.Errorf("run once: source %s: %w", rc.ID, err)
		}
		if !recon.NeedsReconcile(repo, sha) {
			continue
		}
		// one-shot 与 daemon 使用相同构建身份；观察过 SHA 不代表已经成功构建。
		// Suppress exhausted identities while allowing source or configuration drift.
		recent, err := st.ListRuns(ctx, rc.ID, 1)
		if err != nil {
			return err
		}
		if len(recent) > 0 && recent[0].State.IsTerminal() && recent[0].State != model.StateStable && recent[0].DesiredSourceSHA == sha && recent[0].BuilderFingerprint == recon.CurrentFingerprintHex() {
			log.Printf("[once] repo %s: 保留终态 %s（需要新来源/配置或显式恢复）", rc.ID, recent[0].State)
			continue
		}
		id, err := recon.CreateRun(ctx, rc.ID)
		if err != nil {
			return err
		}
		run, err := st.GetRun(ctx, id)
		if err != nil {
			return err
		}
		runs = append(runs, *run)
	}
	for _, run := range runs {
		if err := advanceRun(ctx, st, forge, recon, run.RunID, simulateMerges); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func advanceRun(ctx context.Context, st *store.Store, forge githubapp.Forge, recon *reconcile.Reconciler, runID string, simulateMerges bool) error {
	for i := 0; i < 500; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		res, err := recon.Step(ctx, runID)
		if err != nil {
			return fmt.Errorf("run %s: %w", runID, err)
		}
		run, err := st.GetRun(ctx, runID)
		if err != nil {
			return err
		}
		log.Printf("[once] run %s: %s → %s", runID, res.From, run.State)
		switch run.State {
		case model.StateFailedRetryable, model.StateFailedPermanent, model.StateBlocked, model.StateStale:
			return fmt.Errorf("run %s ended in %s: %s %s", runID, run.State, run.ErrorCode, run.ErrorMessage)
		case model.StateStable:
			fmt.Printf("run %s 最终状态: Stable\n", runID)
			return nil
		}
		if !res.Advanced {
			if simulateMerges && (run.State == model.StateAwaitSourcePR || run.State == model.StateAwaitCatalogPR) {
				kind := model.PRKindSourceWriteback
				if run.State == model.StateAwaitCatalogPR {
					kind = model.PRKindCatalogPublish
				}
				pr, err := st.GetPR(ctx, runID, kind)
				if err != nil {
					return err
				}
				if pr == nil {
					return fmt.Errorf("fake merge: run %s missing %s PR", runID, kind)
				}
				if err := forge.MergePullRequest(ctx, pr.Owner, pr.Repo, pr.Number, "merge"); err != nil {
					return err
				}
				log.Printf("[fake] 模拟合并本地 %s PR #%d", kind, pr.Number)
				continue
			}
			fmt.Printf("run %s 最终状态: %s（等待下一轮或外部操作）\n", runID, run.State)
			return nil
		}
	}
	return fmt.Errorf("run %s exceeded one-shot step limit", runID)
}
