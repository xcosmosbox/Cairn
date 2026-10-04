package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/controller/model"
)

type leaseContextKey struct{}
type leaseOwnership struct {
	repoID, holderID string
	guard            *Store
}

// WithRepoLease 将当前执行的租约身份绑定到写操作；过期或被接管的执行不能继续写状态。
// Bind writes to the execution lease; ownership is checked inside each write transaction.
func WithRepoLease(ctx context.Context, repoID, holderID string) context.Context {
	return context.WithValue(ctx, leaseContextKey{}, leaseOwnership{repoID: repoID, holderID: holderID})
}

func (s *Store) beginWrite(ctx context.Context) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if owner, ok := ctx.Value(leaseContextKey{}).(leaseOwnership); ok {
		var valid int
		err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM repo_leases WHERE repo_id=? AND holder_id=? AND julianday(expires_at)>julianday('now')`, owner.repoID, owner.holderID).Scan(&valid)
		if err != nil || valid != 1 {
			tx.Rollback()
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("controller store: execution lease lost: repo=%s", owner.repoID)
		}
	}
	return tx, nil
}

func (s *Store) execWrite(ctx context.Context, query string, args ...any) (sql.Result, error) {
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

// FailRunRetryable 原子保存错误、失败前阶段及按持久化计数计算的退避时间。
// Save the checkpoint and retry schedule atomically, without losing PR/candidate progress.
func (s *Store) FailRunRetryable(ctx context.Context, runID string, from model.RunState, code, msg string) error {
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state string
	var attempt int
	if err := tx.QueryRowContext(ctx, `SELECT state,attempt FROM runs WHERE run_id=?`, runID).Scan(&state, &attempt); err != nil {
		return err
	}
	if model.RunState(state) != from {
		return fmt.Errorf("controller store: failed stage changed: %s != %s", state, from)
	}
	if !model.CanTransition(from, model.StateFailedRetryable) {
		return fmt.Errorf("controller store: illegal retry transition: %s", from)
	}
	backoff := time.Minute
	for i := 0; i < attempt && backoff < 30*time.Minute; i++ {
		backoff *= 2
	}
	if backoff > 30*time.Minute {
		backoff = 30 * time.Minute
	}
	_, err = tx.ExecContext(ctx, `UPDATE runs SET state=?,retry_state=?,reason=?,error_code=?,error_message=?,next_retry_at=?,finished_at='' WHERE run_id=?`, string(model.StateFailedRetryable), string(from), code+": "+msg, code, msg, time.Now().UTC().Add(backoff).Format(time.RFC3339Nano), runID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// RetryRun 到期后原子增加计数并恢复失败前阶段，耗尽则永久失败。
// Resume the checkpoint and increment attempts in one transaction, honoring the persisted deadline.
func (s *Store) RetryRun(ctx context.Context, runID string, maxAttempts int) (*model.Run, bool, error) {
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	run, err := scanRun(tx.QueryRowContext(ctx, `SELECT run_id,repo_id,desired_source_sha,base_bundle_digest,builder_fingerprint,state,retry_state,attempt,reason,report_path,error_code,error_message,started_at,finished_at,next_retry_at FROM runs WHERE run_id=?`, runID))
	if err != nil {
		return nil, false, err
	}
	if run.State != model.StateFailedRetryable {
		return nil, false, fmt.Errorf("controller store: run is not retryable: %s", run.State)
	}
	now := time.Now().UTC()
	to := run.RetryState
	if run.Attempt >= maxAttempts {
		to = model.StateFailedPermanent
	} else if now.Before(run.NextRetryAt) {
		return run, false, tx.Commit()
	}
	// 升级前的失败没有 checkpoint；仅兼容旧行回退 fetch。
	// Old controller rows had no checkpoint, so only those rows fall back to FetchSource.
	if to == "" {
		to = model.StateFetchSource
	}
	if !model.CanTransition(run.State, to) || to == model.StateFailedRetryable {
		return nil, false, fmt.Errorf("controller store: invalid retry checkpoint: %s", to)
	}
	reason := fmt.Sprintf("retry attempt %d: resume %s", run.Attempt+1, to)
	finished := ""
	if to == model.StateFailedPermanent {
		reason = fmt.Sprintf("retry attempts exhausted (%d): %s", run.Attempt, run.ErrorMessage)
		finished = now.Format(time.RFC3339Nano)
	} else {
		run.Attempt++
	}
	_, err = tx.ExecContext(ctx, `UPDATE runs SET state=?,attempt=?,reason=?,finished_at=?,next_retry_at='' WHERE run_id=?`, string(to), run.Attempt, reason, finished, runID)
	if err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	run.State = to
	run.Reason = reason
	run.NextRetryAt = time.Time{}
	run.FinishedAt = parseTS(finished)
	return run, true, nil
}

// GetEffect 返回外部动作的持久化收据，供重放时复用同一远端结果。
// Read the durable receipt for idempotent replay.
func (s *Store) GetEffect(ctx context.Context, key string) (*model.ExternalEffect, error) {
	e := &model.ExternalEffect{}
	var status, created, updated string
	err := s.db.QueryRowContext(ctx, `SELECT effect_key,effect_type,target_repo,request_fingerprint,external_id,status,response_summary,created_at,updated_at FROM external_effects WHERE effect_key=?`, key).Scan(&e.EffectKey, &e.EffectType, &e.TargetRepo, &e.RequestFingerprint, &e.ExternalID, &status, &e.ResponseSummary, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	e.Status = model.EffectStatus(status)
	e.CreatedAt = parseTS(created)
	e.UpdatedAt = parseTS(updated)
	return e, nil
}

// ActiveRunForRepo 不依赖历史条数限制，避免已有活跃任务时创建第二条 run。
// Find any active run, regardless of how many historical terminal runs exist.
func (s *Store) ActiveRunForRepo(ctx context.Context, repoID string) (string, error) {
	var runID string
	err := s.db.QueryRowContext(ctx, `SELECT run_id FROM runs WHERE repo_id=? AND state NOT IN ('Stable','Blocked','FailedPermanent','Stale') ORDER BY started_at LIMIT 1`, repoID).Scan(&runID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return runID, err
}

// SchemaVersion 返回控制器当前 schema，用于构建 fingerprint 的实际版本输入。
// Expose the migrated controller version for build identity.
func SchemaVersion() int { return schemaVersion }

// RequestRetry 手工重试保留已存在的 PR/candidate，永久失败从持久化 checkpoint 恢复。
// Manual recovery does not reset an existing PR flow to an unrelated full build.
func (s *Store) RequestRetry(ctx context.Context, runID string) error {
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state, retryState string
	if err := tx.QueryRowContext(ctx, `SELECT state,retry_state FROM runs WHERE run_id=?`, runID).Scan(&state, &retryState); err != nil {
		return err
	}
	switch model.RunState(state) {
	case model.StateFailedRetryable, model.StateFailedPermanent:
		if retryState == "" {
			retryState = string(model.StateFetchSource)
		}
	default:
		return fmt.Errorf("controller store: manual retry requires blocked or failed run: %s", state)
	}
	if !model.CanTransition(model.StateFailedRetryable, model.RunState(retryState)) {
		return fmt.Errorf("controller store: invalid manual retry checkpoint: %s", retryState)
	}
	_, err = tx.ExecContext(ctx, `UPDATE runs SET state=?,retry_state=?,attempt=0,reason='manual retry',finished_at='',next_retry_at='' WHERE run_id=?`, string(model.StateFailedRetryable), retryState, runID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// WithRepoLease 附带同步校验器，外部动作不能仅依赖异步心跳发现失权。
// Bind a synchronous ownership guard for filesystem, Git and Forge actions.
func (s *Store) WithRepoLease(ctx context.Context, repoID, holderID string) context.Context {
	return context.WithValue(ctx, leaseContextKey{}, leaseOwnership{repoID: repoID, holderID: holderID, guard: s})
}

// CheckLease 在每个动作边界确认租约和上下文；允许独立工作流使用无租约上下文。
// Check action boundaries synchronously, even when an outbox is not configured.
func CheckLease(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	owner, ok := ctx.Value(leaseContextKey{}).(leaseOwnership)
	if !ok {
		return nil
	}
	if owner.guard == nil {
		return fmt.Errorf("controller store: lease context has no synchronous guard")
	}
	return owner.guard.CheckLease(ctx)
}

// CheckLease 使用数据库执行时的时钟，避免排队后仍用过期的调用时刻判断。
// Validate ownership using the SQL execution time rather than an earlier queued timestamp.
func (s *Store) CheckLease(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	owner, ok := ctx.Value(leaseContextKey{}).(leaseOwnership)
	if !ok {
		return nil
	}
	var valid int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM repo_leases WHERE repo_id=? AND holder_id=? AND julianday(expires_at)>julianday('now')`, owner.repoID, owner.holderID).Scan(&valid); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if valid != 1 {
		return fmt.Errorf("controller store: execution lease lost: repo=%s", owner.repoID)
	}
	return nil
}
