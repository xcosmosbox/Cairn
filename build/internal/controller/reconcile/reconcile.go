// Package reconcile 实现 cairnd 控制器的幂等状态机（产品化 Prompt §7）。
//
// Reconciler.Step 是唯一的状态推进入口：读取 run 当前状态，执行下一步动作，
// 在事务中迁移状态。daemon / run --once / reconcile 命令均调用同一 Reconciler，
// 不形成三套不一致的流程（§5.1）。
//
// 每轮执行幂等：进程在任意点崩溃后，重启重新 Step 即可恢复（§14）。
package reconcile

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/xcosmosbox/cairn/core/dkconfig"
	"github.com/xcosmosbox/cairn/core/kbbundle"
	"github.com/xcosmosbox/cairn/build/internal/controller/githubapp"
	"github.com/xcosmosbox/cairn/build/internal/controller/model"
	"github.com/xcosmosbox/cairn/build/internal/controller/publisher"
	"github.com/xcosmosbox/cairn/build/internal/controller/runner"
	"github.com/xcosmosbox/cairn/build/internal/controller/store"
	"github.com/xcosmosbox/cairn/build/internal/controller/workspace"
)

// Reconciler 是控制器的状态机引擎。
type Reconciler struct {
	cfg     *dkconfig.Config
	store   *store.Store
	forge   githubapp.Forge
	ws      *workspace.Manager
	runner  runner.PipelineRunner
	pub     *publisher.Publisher
	prSummarizer runner.PRSummarizer // 可选：LLM PR 摘要器
	holderID string // 进程实例标识（lease holder）
	leaseTTL time.Duration
}

// Options 构造 Reconciler 的参数。
type Options struct {
	Config   *dkconfig.Config
	Store    *store.Store
	Forge    githubapp.Forge
	WS       *workspace.Manager
	Runner   runner.PipelineRunner
	Publisher *publisher.Publisher
	PRSummarizer runner.PRSummarizer // 可选
	HolderID string
	LeaseTTL time.Duration
}

// transition 包装 TransitionRun，记录迁移失败（避免静默丢失 DB 错误）。
func (r *Reconciler) transition(ctx context.Context, runID string, to model.RunState, reason string) {
	if err := r.store.TransitionRun(ctx, runID, to, reason); err != nil {
		fmt.Printf("[reconcile] 状态迁移失败 %s → %s: %v\n", runID, to, err)
	}
}

// New 创建 Reconciler。
func New(opts Options) *Reconciler {
	if opts.LeaseTTL == 0 {
		opts.LeaseTTL = 30 * time.Minute
	}
	if opts.HolderID == "" {
		opts.HolderID = "cairnd-default"
	}
	return &Reconciler{
		cfg: opts.Config, store: opts.Store, forge: opts.Forge, ws: opts.WS,
		runner: opts.Runner, pub: opts.Publisher, prSummarizer: opts.PRSummarizer,
		holderID: opts.HolderID, leaseTTL: opts.LeaseTTL,
	}
}

// StepResult 是一次 Step 的结果。
type StepResult struct {
	Advanced bool      // 是否推进了状态
	From     model.RunState
	To       model.RunState
	Message  string
}

// Step 推进某 run 一步。若 run 已终态或不需要推进，返回 Advanced=false。
func (r *Reconciler) Step(ctx context.Context, runID string) (StepResult, error) {
	run, err := r.store.GetRun(ctx, runID)
	if err != nil {
		return StepResult{}, err
	}
	if run.State.IsTerminal() {
		return StepResult{Advanced: false, From: run.State, To: run.State}, nil
	}
	res := StepResult{From: run.State}
	repo, err := r.store.GetRepo(ctx, run.RepoID)
	if err != nil {
		return res, err
	}
	repoCfg, ok := r.cfg.RepoByID(run.RepoID)
	if !ok {
		// repo 配置已移除；标记 stale。
		r.transition(ctx, runID, model.StateStale, "repo config removed")
		return StepResult{Advanced: true, From: run.State, To: model.StateStale}, nil
	}
	rc := *repoCfg

	switch run.State {
	case model.StateIdle:
		// Idle → FetchSource（严格状态机：先进入 FetchSource 再做实际工作）。
		r.transition(ctx, runID, model.StateFetchSource, "idle → fetch")
		res.To = model.StateFetchSource
		res.Advanced = true
		return res, nil
	case model.StateFetchSource:
		return r.stepFetchSource(ctx, run, repo, rc, res)
	case model.StateRestoreBase:
		return r.stepRestoreBase(ctx, run, repo, rc, res)
	case model.StatePlan:
		return r.stepPlan(ctx, run, repo, rc, res)
	case model.StateFullBuild:
		return r.stepFullBuild(ctx, run, repo, rc, res)
	case model.StateIncrementalBuild:
		return r.stepIncremental(ctx, run, repo, rc, res)
	case model.StateRebalanceCheck:
		return r.stepRebalanceCheck(ctx, run, repo, rc, res)
	case model.StateRebalance:
		return r.stepRebalance(ctx, run, repo, rc, res)
	case model.StateValidateCandidate:
		return r.stepValidate(ctx, run, repo, rc, res)
	case model.StateCreateSourcePR:
		return r.stepCreateSourcePR(ctx, run, repo, rc, res)
	case model.StateAwaitSourcePR:
		return r.stepAwaitSourcePR(ctx, run, repo, rc, res)
	case model.StateReconcileMergedSource:
		return r.stepReconcileMerged(ctx, run, repo, rc, res)
	case model.StateUploadCandidate:
		return r.stepUploadCandidate(ctx, run, repo, rc, res)
	case model.StateCreateCatalogPR:
		return r.stepCreateCatalogPR(ctx, run, repo, rc, res)
	case model.StateAwaitCatalogPR:
		return r.stepAwaitCatalogPR(ctx, run, repo, rc, res)
	case model.StateFailedRetryable:
		return r.stepRetry(ctx, run, repo, rc, res)
	}
	return res, nil
}

// ─── 各状态处理 ──────────────────────────────────────────────────

func (r *Reconciler) stepFetchSource(ctx context.Context, run *model.Run, repo *model.ManagedRepo, repoCfg dkconfig.RepoConfig, res StepResult) (StepResult, error) {
	// 获取 source branch SHA。
	sha, err := r.forge.GetBranchSHA(ctx, repo.GitHubOwner, repo.GitHubName, repo.Branch)
	if err != nil {
		return r.failRetryable(ctx, run, "fetch_source", err, res)
	}
	r.store.UpdateRepoSeen(ctx, repo.ID, sha)
	r.store.UpdateRunDesiredSHA(ctx, run.RunID, sha)
	run.DesiredSourceSHA = sha
	// 计算当前 builder fingerprint 并记录到 run。fingerprint 变化意味着旧 stable 的
	// 构建语义已过时（模型/schema/builder 升级），须全量重建而非基于旧 base 做增量。
	curFP := r.fingerprintHex()
	r.store.UpdateRunFingerprint(ctx, run.RunID, curFP)
	run.BuilderFingerprint = curFP
	// no-op 判定：SHA + fingerprint + stable 都相同（§7 兼容性矩阵）。
	if sha == repo.LastStableSourceSHA && repo.LastStableBundleDigest != "" && curFP == repo.LastStableFingerprint {
		r.transition(ctx, run.RunID, model.StateStable, "no-op: SHA + fingerprint unchanged")
		res.To = model.StateStable
		res.Advanced = true
		return res, nil
	}
	// 选择路径：无 stable base，或 fingerprint 变化 → 全量；否则增量。
	if repo.LastStableBundleDigest == "" || curFP != repo.LastStableFingerprint {
		reason := "no stable base: full build"
		if repo.LastStableBundleDigest != "" {
			reason = "fingerprint changed: full rebuild"
		}
		r.transition(ctx, run.RunID, model.StateFullBuild, reason)
		res.To = model.StateFullBuild
	} else {
		r.transition(ctx, run.RunID, model.StateRestoreBase, "has stable base: restore + incremental")
		res.To = model.StateRestoreBase
	}
	res.Advanced = true
	return res, nil
}

func (r *Reconciler) stepRestoreBase(ctx context.Context, run *model.Run, repo *model.ManagedRepo, repoCfg dkconfig.RepoConfig, res StepResult) (StepResult, error) {
	// 恢复上一个稳定 Bundle 的 KG 到候选 DB。
	candidateDB := r.candidateDBPath(run.RunID)
	// 简化：从 stable bundle dir 复制 knowledge.db。实际应从 Bundle Registry pull。
	// 此处用 store 记录的 stable digest 定位本地 bundle。
	stableDir := filepath.Join(r.cfg.Service.BundleDir, repo.KGGroup, "stable")
	srcDB := filepath.Join(stableDir, "knowledge.db")
	if _, err := statFile(srcDB); err != nil {
		// 无 stable bundle 文件 → 降级全量。
		r.transition(ctx, run.RunID, model.StateFullBuild, "stable bundle missing: full build")
		res.To = model.StateFullBuild
		res.Advanced = true
		return res, nil
	}
	if err := copyFile(srcDB, candidateDB); err != nil {
		return r.failRetryable(ctx, run, "restore_base", err, res)
	}
	r.transition(ctx, run.RunID, model.StatePlan, "base restored")
	res.To = model.StatePlan
	res.Advanced = true
	return res, nil
}

func (r *Reconciler) stepPlan(ctx context.Context, run *model.Run, repo *model.ManagedRepo, repoCfg dkconfig.RepoConfig, res StepResult) (StepResult, error) {
	// 镜像 source repo + checkout worktree。
	token, _, _ := r.forge.GetInstallToken(ctx)
	remoteURL := r.forge.RemoteURL(repo.GitHubOwner, repo.GitHubName)
	if err := r.ws.EnsureMirror(ctx, repo.ID, remoteURL, token); err != nil {
		return r.failRetryable(ctx, run, "mirror", err, res)
	}
	if _, err := r.ws.CheckoutWorktree(ctx, repo.ID, run.RunID, run.DesiredSourceSHA, ""); err != nil {
		return r.failRetryable(ctx, run, "checkout", err, res)
	}
	r.transition(ctx, run.RunID, model.StateIncrementalBuild, "plan: incremental")
	res.To = model.StateIncrementalBuild
	res.Advanced = true
	return res, nil
}

func (r *Reconciler) stepFullBuild(ctx context.Context, run *model.Run, repo *model.ManagedRepo, repoCfg dkconfig.RepoConfig, res StepResult) (StepResult, error) {
	// 镜像 + checkout source SHA。
	token, _, _ := r.forge.GetInstallToken(ctx)
	remoteURL := r.forge.RemoteURL(repo.GitHubOwner, repo.GitHubName)
	if err := r.ws.EnsureMirror(ctx, repo.ID, remoteURL, token); err != nil {
		return r.failRetryable(ctx, run, "mirror", err, res)
	}
	wt, err := r.ws.CheckoutWorktree(ctx, repo.ID, run.RunID, run.DesiredSourceSHA, "")
	if err != nil {
		return r.failRetryable(ctx, run, "checkout", err, res)
	}
	candidateDB := r.candidateDBPath(run.RunID)
	// 重试场景：清理旧 candidate DB + 确保父目录存在（SQLite CANTOPEN=14 伪装成 OOM）。
	os.MkdirAll(filepath.Dir(candidateDB), 0o755)
	os.Remove(candidateDB)
	// 全量构建（RunFullRebuild 内部会 os.Remove(db)）。
	buildRes, err := r.runner.Full(ctx, runner.FullRequest{
		RepoPath: wt, DBPath: candidateDB, MinConfidence: repoCfg.Rewrite.MinConfidence,
	})
	if err != nil {
		return r.failRetryable(ctx, run, "full_build", err, res)
	}
	r.transition(ctx, run.RunID, model.StateValidateCandidate, fmt.Sprintf("full build: %d nodes", buildRes.NodesCreated))
	res.To = model.StateValidateCandidate
	res.Advanced = true
	return res, nil
}

func (r *Reconciler) stepIncremental(ctx context.Context, run *model.Run, repo *model.ManagedRepo, repoCfg dkconfig.RepoConfig, res StepResult) (StepResult, error) {
	wt := r.ws.SourceWorktree(run.RunID)
	candidateDB := r.candidateDBPath(run.RunID)
	buildRes, err := r.runner.Incremental(ctx, runner.IncrementalRequest{
		RepoPath: wt, DBPath: candidateDB, MinConfidence: repoCfg.Rewrite.MinConfidence,
	})
	if err != nil {
		return r.failRetryable(ctx, run, "incremental", err, res)
	}
	// 记录是否有回写差异。
	if buildRes.HasWriteback {
		r.transition(ctx, run.RunID, model.StateRebalanceCheck, fmt.Sprintf("incremental: %d docs rewritten", buildRes.DocsRewritten))
		res.To = model.StateRebalanceCheck
	} else {
		r.transition(ctx, run.RunID, model.StateRebalanceCheck, "incremental: no writeback")
		res.To = model.StateRebalanceCheck
	}
	res.Advanced = true
	return res, nil
}

func (r *Reconciler) stepRebalanceCheck(ctx context.Context, run *model.Run, repo *model.ManagedRepo, repoCfg dkconfig.RepoConfig, res StepResult) (StepResult, error) {
	wt := r.ws.SourceWorktree(run.RunID)
	candidateDB := r.candidateDBPath(run.RunID)
	dec, err := r.runner.RebalanceCheck(ctx, runner.RebalanceRequest{RepoPath: wt, DBPath: candidateDB})
	if err != nil {
		return r.failRetryable(ctx, run, "rebalance_check", err, res)
	}
	if dec.Triggered {
		r.transition(ctx, run.RunID, model.StateRebalance, fmt.Sprintf("rebalance triggered: %v", dec.Reasons))
		res.To = model.StateRebalance
	} else {
		r.transition(ctx, run.RunID, model.StateValidateCandidate, "rebalance not triggered")
		res.To = model.StateValidateCandidate
	}
	res.Advanced = true
	return res, nil
}

func (r *Reconciler) stepRebalance(ctx context.Context, run *model.Run, repo *model.ManagedRepo, repoCfg dkconfig.RepoConfig, res StepResult) (StepResult, error) {
	wt := r.ws.SourceWorktree(run.RunID)
	candidateDB := r.candidateDBPath(run.RunID)
	_, err := r.runner.Rebalance(ctx, runner.RebalanceRequest{RepoPath: wt, DBPath: candidateDB, Force: true})
	if err != nil {
		return r.failRetryable(ctx, run, "rebalance", err, res)
	}
	r.transition(ctx, run.RunID, model.StateValidateCandidate, "rebalance done")
	res.To = model.StateValidateCandidate
	res.Advanced = true
	return res, nil
}

func (r *Reconciler) stepValidate(ctx context.Context, run *model.Run, repo *model.ManagedRepo, repoCfg dkconfig.RepoConfig, res StepResult) (StepResult, error) {
	candidateDB := r.candidateDBPath(run.RunID)
	vr, err := r.runner.Validate(ctx, runner.ValidateRequest{DBPath: candidateDB})
	if err != nil {
		return r.failRetryable(ctx, run, "validate", err, res)
	}
	if !vr.OK {
		r.store.SetRunError(ctx, run.RunID, "validation_failed", fmt.Sprintf("checks failed: %v", vr.Errors), time.Now().Add(10*time.Minute))
		r.transition(ctx, run.RunID, model.StateFailedRetryable, "validation failed")
		res.To = model.StateFailedRetryable
		res.Advanced = true
		return res, nil
	}
	// 检查是否有回写差异（通过 worktree 状态）。
	wt := r.ws.SourceWorktree(run.RunID)
	digest, _ := workspace.ManagedDigest(wt)
	// 若 worktree 有未提交变更 → 创建 Source PR；否则直接上传 candidate。
	if hasUncommitted, _ := r.wsHasChanges(ctx, wt); hasUncommitted {
		run.Reason = "validate ok, has writeback: " + digest
		r.transition(ctx, run.RunID, model.StateCreateSourcePR, run.Reason)
		res.To = model.StateCreateSourcePR
	} else {
		r.transition(ctx, run.RunID, model.StateUploadCandidate, "validate ok, no writeback")
		res.To = model.StateUploadCandidate
	}
	res.Advanced = true
	return res, nil
}

func (r *Reconciler) stepCreateSourcePR(ctx context.Context, run *model.Run, repo *model.ManagedRepo, repoCfg dkconfig.RepoConfig, res StepResult) (StepResult, error) {
	if repoCfg.Rewrite.Mode != "pr" {
		// direct 模式：直接 push 到 source branch（不推荐，但支持）。
		r.transition(ctx, run.RunID, model.StateReconcileMergedSource, "direct push mode")
		res.To = model.StateReconcileMergedSource
		res.Advanced = true
		return res, nil
	}
	runMarker := "cairn-run-id:" + run.RunID
	botBranch := "cairnd/" + repo.ID + "/" + run.RunID
	wt := r.ws.SourceWorktree(run.RunID)
	// 创建 bot 分支（worktree 此前是 detached HEAD）。
	if err := r.ws.CreateBranchInWorktree(ctx, wt, botBranch); err != nil {
		return r.failRetryable(ctx, run, "create_branch", err, res)
	}
	// 提交回写。
	msg := fmt.Sprintf("chore(kg): structured writeback\n\n%s\nsource: %s", runMarker, run.DesiredSourceSHA[:12])
	if err := r.ws.CommitAll(ctx, wt, msg, "cairnd", "cairnd@bot"); err != nil && err != workspace.ErrNoChanges {
		return r.failRetryable(ctx, run, "commit_writeback", err, res)
	}
	// 计算 expected worktree digest（提交后的受管文件摘要）。
	expectedDigest, _ := workspace.ManagedDigest(wt)
	// 推送 bot 分支。
	token, _, _ := r.forge.GetInstallToken(ctx)
	if err := r.ws.PushBranch(ctx, repo.ID, repo.SourceURL, botBranch, token); err != nil {
		return r.failRetryable(ctx, run, "push_branch", err, res)
	}
	headSHA, _ := r.ws.HeadSHA(ctx, wt)
	// 构建丰富的 PR body。
	candidateDB := r.candidateDBPath(run.RunID)
	diffStat, _ := r.ws.DiffStat(ctx, wt, run.DesiredSourceSHA)
	prBody := r.buildSourcePRBody(ctx, run, repo, repoCfg, expectedDigest, candidateDB, diffStat, runMarker)
	prTitle := fmt.Sprintf("chore(kg): %s 知识图谱结构化回写 @ %s", repo.KGGroup, run.DesiredSourceSHA[:12])
	// 创建 PR。
	pr, err := r.forge.EnsurePullRequest(ctx, githubapp.PullRequestSpec{
		Owner: repo.GitHubOwner, Repo: repo.GitHubName,
		HeadBranch: botBranch, BaseBranch: repo.Branch,
		Title: prTitle,
		Body:  prBody,
		RunMarker: runMarker,
	})
	if err != nil {
		return r.failRetryable(ctx, run, "create_pr", err, res)
	}
	r.store.UpsertPR(ctx, model.PullRequest{
		RunID: run.RunID, Kind: model.PRKindSourceWriteback,
		Owner: repo.GitHubOwner, Repo: repo.GitHubName, Number: pr.Number,
		Branch: botBranch, HeadSHA: headSHA, BaseBranch: repo.Branch, Status: model.PRStatusOpen,
	})
	// 记录 expected digest 供合并后校验。
	cand := model.Candidate{
		CandidateID: run.RunID + "-cand", RunID: run.RunID, SourceSHA: run.DesiredSourceSHA,
		ExpectedWorktreeDigest: expectedDigest, Status: model.CandidateBuilding,
	}
	r.store.UpsertCandidate(ctx, cand)
	r.transition(ctx, run.RunID, model.StateAwaitSourcePR, fmt.Sprintf("PR #%d created", pr.Number))
	res.To = model.StateAwaitSourcePR
	res.Advanced = true
	return res, nil
}

func (r *Reconciler) stepAwaitSourcePR(ctx context.Context, run *model.Run, repo *model.ManagedRepo, repoCfg dkconfig.RepoConfig, res StepResult) (StepResult, error) {
	pr, err := r.store.GetPR(ctx, run.RunID, model.PRKindSourceWriteback)
	if err != nil || pr == nil {
		return r.failRetryable(ctx, run, "pr_not_found", fmt.Errorf("source PR not found"), res)
	}
	// 查询 PR 状态。
	observed, err := r.forge.GetPullRequest(ctx, pr.Owner, pr.Repo, pr.Number)
	if err != nil {
		return r.failRetryable(ctx, run, "get_pr", err, res)
	}
	// 提前获取 source branch HEAD：既用于 merged 二次确认，也用于 stale 判定。
	curSHA, _ := r.forge.GetBranchSHA(ctx, repo.GitHubOwner, repo.GitHubName, repo.Branch)

	// Merged 检测：直接 merged 标志，或分支 HEAD 已等于 PR 的 merge_commit_sha。
	// 后者用于应对 GitHub 最终一致性延迟——合并瞬间分支 ref 已更新，但 merged 标志
	// 因主从复制延迟可能仍返回 false（曾导致误判 Stale：分支已是 merge commit，
	// 既非 DesiredSourceSHA 也非 pr.HeadSHA，stale 判定错误触发）。
	mergedByCommit := observed.MergeCommitSHA != "" && curSHA == observed.MergeCommitSHA
	if observed.Merged || mergedByCommit {
		reason := "source PR merged"
		if !observed.Merged {
			reason = "source PR merged (由 merge_commit_sha 确认，merged 标志延迟)"
		}
		r.store.UpsertPR(ctx, model.PullRequest{
			RunID: run.RunID, Kind: model.PRKindSourceWriteback, Owner: pr.Owner, Repo: pr.Repo,
			Number: pr.Number, Branch: pr.Branch, HeadSHA: observed.HeadSHA, BaseBranch: pr.BaseBranch,
			Status: model.PRStatusMerged,
		})
		r.transition(ctx, run.RunID, model.StateReconcileMergedSource, reason)
		res.To = model.StateReconcileMergedSource
		res.Advanced = true
		return res, nil
	}
	if observed.State == "closed" && !observed.Merged {
		// PR 被关闭未合并 → Blocked（§14）。
		r.transition(ctx, run.RunID, model.StateBlocked, "source PR closed without merge")
		res.To = model.StateBlocked
		res.Advanced = true
		return res, nil
	}
	// 仍 open：检查 source branch 是否已变化（stale 判定）。
	if curSHA != run.DesiredSourceSHA && curSHA != pr.HeadSHA {
		r.transition(ctx, run.RunID, model.StateStale, "source branch changed during PR wait")
		res.To = model.StateStale
		res.Advanced = true
		return res, nil
	}
	// 仍等待。
	res.To = model.StateAwaitSourcePR
	res.Advanced = false
	return res, nil
}

func (r *Reconciler) stepReconcileMerged(ctx context.Context, run *model.Run, repo *model.ManagedRepo, repoCfg dkconfig.RepoConfig, res StepResult) (StepResult, error) {
	// 获取合并后的 source branch HEAD。
	mergedSHA, err := r.forge.GetBranchSHA(ctx, repo.GitHubOwner, repo.GitHubName, repo.Branch)
	if err != nil {
		return r.failRetryable(ctx, run, "get_merged_sha", err, res)
	}
	// 更新 mirror + checkout 合并后 worktree。
	token, _, _ := r.forge.GetInstallToken(ctx)
	if err := r.ws.EnsureMirror(ctx, repo.ID, repo.SourceURL, token); err != nil {
		return r.failRetryable(ctx, run, "remirror", err, res)
	}
	wt, err := r.ws.CheckoutWorktree(ctx, repo.ID, run.RunID+"-merged", mergedSHA, "")
	if err != nil {
		return r.failRetryable(ctx, run, "checkout_merged", err, res)
	}
	// 校验合并后 tree digest 与 candidate 期望一致（§7.1 步骤 7）。
	cand, _ := r.store.GetCandidate(ctx, run.RunID+"-cand")
	actualDigest, _ := workspace.ManagedDigest(wt)
	if cand != nil && cand.ExpectedWorktreeDigest != "" && actualDigest != cand.ExpectedWorktreeDigest {
		// tree 不一致（人在 PR 中调整了内容）→ 重新增量收敛（§7.1 步骤 8）。
		candidateDB := r.candidateDBPath(run.RunID)
		_, err := r.runner.Incremental(ctx, runner.IncrementalRequest{RepoPath: wt, DBPath: candidateDB, MinConfidence: repoCfg.Rewrite.MinConfidence})
		if err != nil {
			return r.failRetryable(ctx, run, "reconverge_incremental", err, res)
		}
		r.transition(ctx, run.RunID, model.StateUploadCandidate, "re-converged after merge adjustment")
		res.To = model.StateUploadCandidate
		res.Advanced = true
		return res, nil
	}
	// tree 一致 → 直接上传 candidate。
	run.DesiredSourceSHA = mergedSHA
	r.store.UpdateRunDesiredSHA(ctx, run.RunID, mergedSHA)
	r.transition(ctx, run.RunID, model.StateUploadCandidate, "merged tree matches expected")
	res.To = model.StateUploadCandidate
	res.Advanced = true
	return res, nil
}

func (r *Reconciler) stepUploadCandidate(ctx context.Context, run *model.Run, repo *model.ManagedRepo, repoCfg dkconfig.RepoConfig, res StepResult) (StepResult, error) {
	candidateDB := r.candidateDBPath(run.RunID)
	bundleDir := filepath.Join(r.cfg.Service.BundleDir, repo.KGGroup, "candidates", run.RunID)
	pub, err := r.pub.EnsureCandidate(ctx, publisher.CandidateSpec{
		Repo: repoCfg, Catalog: r.cfg.Catalog, SourceSHA: run.DesiredSourceSHA,
		Fingerprint:  r.currentFingerprint(),
		ConfigDigest: "sha256:" + run.BuilderFingerprint,
		KGDBPath: candidateDB, BundleDir: bundleDir,
	})
	if err != nil {
		return r.failRetryable(ctx, run, "upload_candidate", err, res)
	}
	// 更新 candidate 记录。保留 stepCreateSourcePR 写入的 ExpectedWorktreeDigest，
	// 避免 upsert 用零值覆盖（有回写路径下合并校验依赖它）。
	expectedDigest := ""
	if existing, _ := r.store.GetCandidate(ctx, run.RunID+"-cand"); existing != nil {
		expectedDigest = existing.ExpectedWorktreeDigest
	}
	r.store.UpsertCandidate(ctx, model.Candidate{
		CandidateID: run.RunID + "-cand", RunID: run.RunID, SourceSHA: run.DesiredSourceSHA,
		ExpectedWorktreeDigest: expectedDigest,
		BundlePath: bundleDir, BundleDigest: pub.Manifest.BundleDigest,
		ReleaseID: pub.Release.ID, ReleaseTag: pub.Release.Tag, Status: model.CandidateUploaded,
	})
	r.transition(ctx, run.RunID, model.StateCreateCatalogPR, fmt.Sprintf("candidate uploaded: %s", pub.Manifest.BundleDigest[:19]))
	res.To = model.StateCreateCatalogPR
	res.Advanced = true
	return res, nil
}

func (r *Reconciler) stepCreateCatalogPR(ctx context.Context, run *model.Run, repo *model.ManagedRepo, repoCfg dkconfig.RepoConfig, res StepResult) (StepResult, error) {
	cand, err := r.store.GetCandidate(ctx, run.RunID+"-cand")
	if err != nil || cand == nil {
		return r.failRetryable(ctx, run, "candidate_not_found", fmt.Errorf("candidate not found"), res)
	}
	token, _, _ := r.forge.GetInstallToken(ctx)
	// 确保 catalog mirror 存在。
	catalogRef := githubapp.RepoRef{Owner: parseOwner(r.cfg.Catalog.Repo), Name: parseName(r.cfg.Catalog.Repo)}
	catalogURL := r.forge.RemoteURL(catalogRef.Owner, catalogRef.Name)
	if err := r.ws.EnsureMirror(ctx, "catalog", catalogURL, token); err != nil {
		return r.failRetryable(ctx, run, "catalog_mirror", err, res)
	}
	pub := publisher.PublishedBundle{
		Manifest: kbbundle.Manifest{BundleDigest: cand.BundleDigest, KG: repoCfg.KGGroup, ConfigDigest: "sha256:" + run.BuilderFingerprint},
		Release:  githubapp.Release{Owner: catalogRef.Owner, Repo: catalogRef.Name, Tag: cand.ReleaseTag},
	}
	// 构建丰富的 Catalog PR body（含可选 LLM 摘要）。
	candidateDB := r.candidateDBPath(run.RunID)
	prBody := r.buildCatalogPRBody(ctx, run, repo, repoCfg, cand, candidateDB)
	pr, err := r.pub.EnsureCatalogProposal(ctx, publisher.CatalogProposalSpec{
		Repo: repoCfg, Catalog: r.cfg.Catalog, Bundle: pub, SourceSHA: run.DesiredSourceSHA,
		RunID: run.RunID, Fingerprint: r.currentFingerprint(),
		CatalogRemoteURL: catalogURL, ForgeToken: token, PRBody: prBody,
	})
	if err != nil {
		return r.failRetryable(ctx, run, "catalog_pr", err, res)
	}
	r.store.UpsertPR(ctx, model.PullRequest{
		RunID: run.RunID, Kind: model.PRKindCatalogPublish,
		Owner: catalogRef.Owner, Repo: catalogRef.Name, Number: pr.Number,
		Branch: pr.HeadBranch, HeadSHA: pr.HeadSHA, BaseBranch: r.cfg.Catalog.Branch, Status: model.PRStatusOpen,
	})
	r.transition(ctx, run.RunID, model.StateAwaitCatalogPR, fmt.Sprintf("catalog PR #%d created", pr.Number))
	res.To = model.StateAwaitCatalogPR
	res.Advanced = true
	return res, nil
}

func (r *Reconciler) stepAwaitCatalogPR(ctx context.Context, run *model.Run, repo *model.ManagedRepo, repoCfg dkconfig.RepoConfig, res StepResult) (StepResult, error) {
	pr, err := r.store.GetPR(ctx, run.RunID, model.PRKindCatalogPublish)
	if err != nil || pr == nil {
		return r.failRetryable(ctx, run, "catalog_pr_not_found", fmt.Errorf("catalog PR not found"), res)
	}
	observed, err := r.forge.GetPullRequest(ctx, pr.Owner, pr.Repo, pr.Number)
	if err != nil {
		return r.failRetryable(ctx, run, "get_catalog_pr", err, res)
	}
	if observed.Merged {
		// Catalog PR 合并 → candidate 提升为 stable（§7.2）。
		cand, _ := r.store.GetCandidate(ctx, run.RunID + "-cand")
		digest := ""
		if cand != nil {
			digest = cand.BundleDigest
		}
		// 元数据指针必须与远端 catalog 一致（事实来源）。同时记录本次 fingerprint，
		// 供下次 no-op / 全量判定使用。
		r.store.UpdateRepoStable(ctx, repo.ID, run.DesiredSourceSHA, digest, run.BuilderFingerprint)
		if cand != nil {
			cand.Status = model.CandidateStable
			r.store.UpsertCandidate(ctx, *cand)
			// 将 candidate bundle 落盘为本地 stable 基线，供后续增量构建（stepRestoreBase）恢复。
			// best-effort：失败不阻塞 stable 状态——下次 stepRestoreBase 找不到文件会自然降级
			// 全量，不影响正确性（本地 stable bundle 仅是增量优化缓存，非事实来源）。
			if err := r.promoteCandidateToStable(repo, cand); err != nil {
				log.Printf("[reconcile] run %s: 提升本地 stable bundle 失败（下次将降级全量重建）: %v", run.RunID, err)
			}
		}
		r.transition(ctx, run.RunID, model.StateStable, "catalog PR merged: stable")
		res.To = model.StateStable
		res.Advanced = true
		return res, nil
	}
	if observed.State == "closed" && !observed.Merged {
		r.transition(ctx, run.RunID, model.StateBlocked, "catalog PR closed without merge")
		res.To = model.StateBlocked
		res.Advanced = true
		return res, nil
	}
	// 仍等待。
	res.To = model.StateAwaitCatalogPR
	res.Advanced = false
	return res, nil
}

func (r *Reconciler) stepRetry(ctx context.Context, run *model.Run, repo *model.ManagedRepo, repoCfg dkconfig.RepoConfig, res StepResult) (StepResult, error) {
	// 最大重试次数：超过后永久失败，不再无限重试。
	const maxAttempts = 10
	if run.Attempt >= maxAttempts {
		msg := fmt.Sprintf("重试次数耗尽（%d 次）: %s", run.Attempt, run.ErrorMessage)
		r.transition(ctx, run.RunID, model.StateFailedPermanent, msg)
		res.To = model.StateFailedPermanent
		res.Advanced = true
		res.Message = msg
		return res, nil
	}
	// 指数退避：若 next_retry_at 未到，跳过。
	if !run.NextRetryAt.IsZero() && time.Now().Before(run.NextRetryAt) {
		res.To = model.StateFailedRetryable
		res.Advanced = false
		return res, nil
	}
	run.Attempt++
	r.transition(ctx, run.RunID, model.StateFetchSource, fmt.Sprintf("retry attempt %d", run.Attempt))
	res.To = model.StateFetchSource
	res.Advanced = true
	return res, nil
}

// ─── 辅助 ────────────────────────────────────────────────────────

func (r *Reconciler) failRetryable(ctx context.Context, run *model.Run, code string, err error, res StepResult) (StepResult, error) {
	backoff := time.Duration(1<<uint(run.Attempt)) * time.Minute
	if backoff > 30*time.Minute {
		backoff = 30 * time.Minute
	}
	msg := fmt.Sprintf("%s: %v", code, err)
	r.store.SetRunError(ctx, run.RunID, code, err.Error(), time.Now().Add(backoff))
	r.transition(ctx, run.RunID, model.StateFailedRetryable, msg)
	res.To = model.StateFailedRetryable
	res.Advanced = true
	res.Message = msg
	// 返回 nil error：失败已记录到 run，调度器继续处理其他 run。
	return res, nil
}

func (r *Reconciler) candidateDBPath(runID string) string {
	p := filepath.Join(r.cfg.Service.CacheDir, runID, "candidate.db")
	os.MkdirAll(filepath.Dir(p), 0o755) // SQLite CANTOPEN=14 伪装成 OOM，实际是目录不存在
	return p
}

// builder fingerprint 的稳定输入（§7.3）。任一变化都会改变 fingerprint，
// 从而触发全量重建（旧 base 与新构建语义不兼容，不能做增量）。
const (
	builderVersion  = "v1.0.0"
	dbSchemaVersion = 4
)

// currentFingerprint 构造当前 builder 的 fingerprint（模型 / schema / builder 版本等）。
// 全流程统一由此构造，避免各处硬编码不一致导致 Digest 漂移。
func (r *Reconciler) currentFingerprint() publisher.Fingerprint {
	return publisher.Fingerprint{
		BuilderVersion:  builderVersion,
		DBSchemaVersion: dbSchemaVersion,
		Model:           r.cfg.LLM.Model,
	}
}

// fingerprintHex 返回当前 fingerprint 的 hex（不带 sha256: 前缀），用于持久化与比较。
func (r *Reconciler) fingerprintHex() string {
	return strings.TrimPrefix(r.currentFingerprint().Digest(), "sha256:")
}

func (r *Reconciler) wsHasChanges(ctx context.Context, wt string) (bool, error) {
	// 用 git status 检查（workspace 已有 hasChanges 但未导出；此处简化）。
	return workspaceHasChanges(ctx, wt)
}

// buildSourcePRBody 构建丰富的 Source PR body（机器标记 + 构建统计 + diff + 审查指引 + 可选 LLM 摘要）。
func (r *Reconciler) buildSourcePRBody(ctx context.Context, run *model.Run, repo *model.ManagedRepo, repoCfg dkconfig.RepoConfig, expectedDigest, candidateDB, diffStat, runMarker string) string {
	var b strings.Builder
	// ── 机器标记区（幂等查找 + 合并后校验，必须保留）──
	b.WriteString("<!-- dkg-controller\n")
	b.WriteString(fmt.Sprintf("cairn-run-id: %s\n", run.RunID))
	b.WriteString(fmt.Sprintf("expected_worktree_digest: %s\n", expectedDigest))
	b.WriteString(fmt.Sprintf("source_commit: %s\n", run.DesiredSourceSHA))
	b.WriteString(fmt.Sprintf("kg_group: %s\n", repo.KGGroup))
	b.WriteString(fmt.Sprintf("builder_fingerprint: %s\n", run.BuilderFingerprint))
	b.WriteString("-->\n\n")

	// ── LLM 摘要（可选）──
	if r.prSummarizer != nil && repoCfg.Rewrite.PRSummaryLLM {
		log.Printf("[reconcile] run %s: 生成 LLM PR 摘要...", run.RunID)
		nodeCount, edgeCount := r.countKG(candidateDB)
		input := runner.PRSummaryInput{
			KGGroup: repo.KGGroup,
			SourceRepo: repo.GitHubOwner + "/" + repo.GitHubName,
			SourceCommit: run.DesiredSourceSHA,
			NodesTotal: nodeCount, // 全图规模，非本次新增（本次改动量由 DiffStat 体现）
			EdgesTotal: edgeCount,
			DiffStat: diffStat,
		}
		if summary, err := r.prSummarizer.GeneratePRSummary(ctx, input); err == nil {
			b.WriteString("## 📝 变更摘要\n\n")
			b.WriteString(summary.Description)
			b.WriteString("\n\n---\n\n")
			log.Printf("[reconcile] run %s: PR 摘要已生成 (title=%q)", run.RunID, summary.Title)
		} else {
			log.Printf("[reconcile] run %s: PR 摘要生成失败（回退纯结构化）: %v", run.RunID, err)
		}
	}

	// ── 构建统计 ──
	b.WriteString("## 📊 构建统计\n\n")
	b.WriteString("| 项目 | 值 |\n|---|---|\n")
	b.WriteString(fmt.Sprintf("| 知识库 | `%s` |\n", repo.KGGroup))
	b.WriteString(fmt.Sprintf("| 源仓库 | `%s/%s` |\n", repo.GitHubOwner, repo.GitHubName))
	b.WriteString(fmt.Sprintf("| 源提交 | `%s` |\n", run.DesiredSourceSHA[:12]))
	b.WriteString(fmt.Sprintf("| 分支 | `%s` → `%s` |\n", "cairnd/"+repo.ID+"/"+run.RunID, repo.Branch))
	b.WriteString(fmt.Sprintf("| 重试次数 | %d |\n", run.Attempt))
	nodeCount, edgeCount := r.countKG(candidateDB)
	b.WriteString(fmt.Sprintf("| KG 节点数 | %d |\n", nodeCount))
	b.WriteString(fmt.Sprintf("| KG 边数 | %d |\n", edgeCount))
	b.WriteString(fmt.Sprintf("| 最低置信度 | %.2f |\n", repoCfg.Rewrite.MinConfidence))
	b.WriteString("\n")

	// ── 文件变更 ──
	if diffStat != "" {
		b.WriteString("## 📁 文件变更\n\n```\n")
		b.WriteString(diffStat)
		b.WriteString("\n```\n\n")
	}

	// ── 审查指引 ──
	b.WriteString("## 🔍 审查指引\n\n")
	b.WriteString("本 PR 由 DK Controller 自动生成，包含知识图谱构建后的结构化回写：\n\n")
	b.WriteString("- **可编辑区**（摘要/详述）的变更反映了 LLM 对文档的理解\n")
	b.WriteString("- **只读区**（UUID 锚点、归属行、标题）不应被修改\n")
	b.WriteString("- `.md.kg.yaml` sidecar 记录了节点元数据（UUID/Tag/Members/Span）\n")
	b.WriteString("- 合并后 Controller 会校验 tree digest 并发布 candidate Bundle\n\n")
	b.WriteString("如发现问题，可直接关闭 PR。Controller 会标记为 `Blocked` 等待人工处理。\n")

	return b.String()
}

// countKG 从 candidate DB 读取节点/边计数。
func (r *Reconciler) countKG(dbPath string) (nodes, edges int) {
	conn, err := sql.Open("sqlite", dbPath+"?mode=ro&query_only=1")
	if err != nil {
		return 0, 0
	}
	defer conn.Close()
	conn.QueryRow("SELECT COUNT(*) FROM nodes").Scan(&nodes)
	conn.QueryRow("SELECT COUNT(*) FROM edges").Scan(&edges)
	return
}

// buildCatalogPRBody 构建丰富的 Catalog PR body（机器标记 + 可选 LLM 摘要 + Bundle 信息 + 发布说明 + 审查指引）。
func (r *Reconciler) buildCatalogPRBody(ctx context.Context, run *model.Run, repo *model.ManagedRepo, repoCfg dkconfig.RepoConfig, cand *model.Candidate, candidateDB string) string {
	var b strings.Builder

	// ── 机器标记区（幂等查找 + 合并后校验，必须保留）──
	b.WriteString("<!-- dkg-controller\n")
	b.WriteString(fmt.Sprintf("cairn-run-id: %s\n", run.RunID))
	b.WriteString(fmt.Sprintf("bundle_digest: %s\n", cand.BundleDigest))
	b.WriteString(fmt.Sprintf("config_digest: %s\n", "sha256:"+run.BuilderFingerprint))
	b.WriteString(fmt.Sprintf("fingerprint: %s\n", r.currentFingerprint().Digest()))
	b.WriteString(fmt.Sprintf("kg_group: %s\n", repo.KGGroup))
	b.WriteString(fmt.Sprintf("release_tag: %s\n", cand.ReleaseTag))
	b.WriteString(fmt.Sprintf("source_commit: %s\n", run.DesiredSourceSHA))
	b.WriteString("-->\n\n")

	// ── LLM 摘要（可选）──
	if r.prSummarizer != nil && repoCfg.Rewrite.PRSummaryLLM {
		log.Printf("[reconcile] run %s: 生成 Catalog PR LLM 摘要...", run.RunID)
		nodeCount, edgeCount := r.countKG(candidateDB)
		input := runner.PRSummaryInput{
			KGGroup:              repo.KGGroup,
			SourceRepo:           repo.GitHubOwner + "/" + repo.GitHubName,
			SourceCommit:         run.DesiredSourceSHA,
			NodesTotal:           nodeCount, // bundle 内的全图规模
			EdgesTotal:           edgeCount,
			IsCatalog:            true,
			BundleDigest:         cand.BundleDigest,
			ReleaseTag:           cand.ReleaseTag,
			PreviousStableDigest: repo.LastStableBundleDigest,
		}
		if summary, err := r.prSummarizer.GeneratePRSummary(ctx, input); err == nil {
			b.WriteString("## 📝 发布摘要\n\n")
			b.WriteString(summary.Description)
			b.WriteString("\n\n---\n\n")
			log.Printf("[reconcile] run %s: Catalog PR 摘要已生成 (title=%q)", run.RunID, summary.Title)
		} else {
			log.Printf("[reconcile] run %s: Catalog PR 摘要生成失败（回退纯结构化）: %v", run.RunID, err)
		}
	}

	// ── Bundle 信息 ──
	nodeCount, edgeCount := r.countKG(candidateDB)
	b.WriteString("## 📦 Bundle 信息\n\n")
	b.WriteString("| 项目 | 值 |\n|---|---|\n")
	b.WriteString(fmt.Sprintf("| 知识库 | `%s` |\n", repo.KGGroup))
	b.WriteString(fmt.Sprintf("| Bundle Digest | `%s` |\n", cand.BundleDigest))
	b.WriteString(fmt.Sprintf("| Release Tag | `%s` |\n", cand.ReleaseTag))
	b.WriteString(fmt.Sprintf("| 源仓库 | `%s/%s` |\n", repo.GitHubOwner, repo.GitHubName))
	b.WriteString(fmt.Sprintf("| 源提交 | `%s` |\n", run.DesiredSourceSHA[:12]))
	b.WriteString(fmt.Sprintf("| KG 节点数 | %d |\n", nodeCount))
	b.WriteString(fmt.Sprintf("| KG 边数 | %d |\n", edgeCount))
	if repo.LastStableBundleDigest != "" {
		b.WriteString(fmt.Sprintf("| 上一个 Stable | `%s` |\n", repo.LastStableBundleDigest))
	} else {
		b.WriteString("| 上一个 Stable | _首次发布_ |\n")
	}
	b.WriteString("\n")

	// ── 发布说明 ──
	b.WriteString("## 🚀 发布说明\n\n")
	b.WriteString("本 PR 将 candidate Bundle 提升为 **stable**。\n\n")
	b.WriteString("**Catalog PR 合并后：**\n")
	b.WriteString("- `stable.json` manifest 更新为本次 Bundle 的 digest\n")
	b.WriteString("- GitHub Release 中的 `bundle.tar.gz` 成为正式发布产物\n")
	b.WriteString("- 消费者通过 `cairnctl bundle pull` 获取最新 stable 知识库\n\n")

	// ── 审查指引 ──
	b.WriteString("## 🔍 审查指引\n\n")
	b.WriteString("本 PR 由 DK Controller 自动生成，将已通过 Source PR 审查的构建产物发布为 stable：\n\n")
	b.WriteString("- Bundle digest 应与 Source PR 合并后的构建一致\n")
	b.WriteString("- Builder fingerprint 应与预期匹配（模型版本、schema 版本等）\n")
	b.WriteString("- **合并后不可撤销**——stable 是消费者依赖的不可变产物\n")
	b.WriteString("- 如发现问题，请关闭 PR 而非合并。Controller 会标记为 `Blocked`\n")

	return b.String()
}

// CreateRun 为某 repo 创建一条新 run（调度器调用）。
func (r *Reconciler) CreateRun(ctx context.Context, repoID string) (string, error) {
	runID := fmt.Sprintf("run-%d-%s", time.Now().UnixNano(), repoID)
	run := model.Run{
		RunID: runID, RepoID: repoID, State: model.StateIdle, Attempt: 0,
		Reason: "created", StartedAt: time.Now().UTC(),
	}
	if err := r.store.CreateRun(ctx, run); err != nil {
		return "", err
	}
	return runID, nil
}

// parseOwner/parseName 从 owner/name 解析。
func parseOwner(slug string) string {
	for i, c := range slug {
		if c == '/' {
			return slug[:i]
		}
	}
	return slug
}
func parseName(slug string) string {
	for i, c := range slug {
		if c == '/' {
			return slug[i+1:]
		}
	}
	return slug
}

// statFile 是 os.Stat 的简写（可被测试覆盖）。
func statFile(path string) (os.FileInfo, error) { return os.Stat(path) }

// copyFile 复制文件。
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// promoteCandidateToStable 把 candidate bundle 目录复制到 <bundleDir>/<kg>/stable，
// 作为后续增量构建（stepRestoreBase）恢复的本地基线。
//
// 原子性：先复制到 stable.tmp，再用 rename 交换，把「复制中途崩溃留下半个 stable」的
// 窗口降到最低。旧 stable 先移到 stable.old 再删除，交换失败时可人工恢复。
func (r *Reconciler) promoteCandidateToStable(repo *model.ManagedRepo, cand *model.Candidate) error {
	if cand == nil || cand.BundlePath == "" {
		return fmt.Errorf("candidate bundle path 为空")
	}
	srcDB := filepath.Join(cand.BundlePath, "knowledge.db")
	if _, err := statFile(srcDB); err != nil {
		return fmt.Errorf("candidate knowledge.db 缺失: %w", err)
	}
	stableDir := filepath.Join(r.cfg.Service.BundleDir, repo.KGGroup, "stable")
	tmpDir := stableDir + ".tmp"
	oldDir := stableDir + ".old"
	if err := os.RemoveAll(tmpDir); err != nil {
		return fmt.Errorf("清理 stable.tmp: %w", err)
	}
	// 复制整个 bundle 目录（knowledge.db / kb-manifest.json / checksums.sha256 / build-report 等）。
	if err := copyTree(cand.BundlePath, tmpDir); err != nil {
		os.RemoveAll(tmpDir)
		return fmt.Errorf("复制 candidate bundle: %w", err)
	}
	// 原子交换：旧 stable → stable.old，新 stable.tmp → stable，再清理 old。
	os.RemoveAll(oldDir)
	if _, err := os.Stat(stableDir); err == nil {
		if err := os.Rename(stableDir, oldDir); err != nil {
			os.RemoveAll(tmpDir)
			return fmt.Errorf("移走旧 stable: %w", err)
		}
	}
	if err := os.Rename(tmpDir, stableDir); err != nil {
		// 交换失败：尝试回滚旧 stable。
		if _, statErr := os.Stat(oldDir); statErr == nil {
			os.Rename(oldDir, stableDir)
		}
		os.RemoveAll(tmpDir)
		return fmt.Errorf("切换 stable 目录: %w", err)
	}
	os.RemoveAll(oldDir)
	return nil
}

// copyTree 递归复制目录 src 到 dst。
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
}

// workspaceHasChanges 检查 worktree 是否有未提交变更。
func workspaceHasChanges(ctx context.Context, wt string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	cmd.Dir = wt
	out, err := cmd.Output()
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) != "", nil
}
