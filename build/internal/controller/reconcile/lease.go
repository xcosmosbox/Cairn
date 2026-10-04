package reconcile

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/xcosmosbox/cairn/build/internal/controller/model"
)

// ErrLeaseBusy 表示另一个执行正处理同一仓库；不是可覆盖的失败。
// A busy repository must not be processed by another execution.
var ErrLeaseBusy = errors.New("reconcile: repository lease held by another execution")

// withRepoLease 每次调用使用独立 owner，包括同一实例的并发 Step。
// Renew ownership for the full action lifetime and cancel work when ownership is lost.
func (r *Reconciler) withRepoLease(ctx context.Context, repoID string, fn func(context.Context) error) (bool, error) {
	return r.withRepoLeaseTicks(ctx, repoID, fn, nil)
}

// withRepoLeaseTicks 保持同一租约生命周期，只把心跳触发与数据库的真实时钟分开。
// This private entry lets tests observe committed renewals without assuming scheduler timing.
func (r *Reconciler) withRepoLeaseTicks(ctx context.Context, repoID string, fn func(context.Context) error, ticks <-chan time.Time) (bool, error) {
	owner := r.holderID + "/" + uuid.NewString()
	acquired, err := r.store.AcquireLease(ctx, repoID, owner, r.leaseTTL)
	if err != nil || !acquired {
		return acquired, err
	}
	actionCtx, cancel := context.WithCancelCause(r.store.WithRepoLease(ctx, repoID, owner))
	defer cancel(nil)
	stop := make(chan struct{})
	done := make(chan struct{})
	interval := r.leaseTTL / 3
	if interval < time.Millisecond {
		interval = time.Millisecond
	}
	var ticker *time.Ticker
	if ticks == nil {
		ticker = time.NewTicker(interval)
		ticks = ticker.C
	}
	go func() {
		defer close(done)
		if ticker != nil {
			defer ticker.Stop()
		}
		for {
			select {
			case <-stop:
				return
			case <-actionCtx.Done():
				return
			case <-ticks:
				heartbeatCtx, stopHeartbeat := context.WithTimeout(actionCtx, interval)
				err := r.store.RenewLease(heartbeatCtx, repoID, owner, r.leaseTTL)
				stopHeartbeat()
				if err != nil {
					cancel(fmt.Errorf("reconcile: lease heartbeat: %w", err))
					return
				}
			}
		}
	}()
	actionErr := fn(actionCtx)
	close(stop)
	<-done
	// 函数忽略 ctx 错误也不能报告成功；检查最后一次动作后的所有权。
	// Fail even if an action ignored cancellation; writes were fenced transactionally.
	if cause := context.Cause(actionCtx); cause != nil {
		actionErr = errors.Join(actionErr, cause)
	} else if err := r.store.RenewLease(actionCtx, repoID, owner, r.leaseTTL); err != nil {
		actionErr = errors.Join(actionErr, err)
	}
	cleanupCtx, stopCleanup := context.WithTimeout(context.Background(), 5*time.Second)
	releaseErr := r.store.ReleaseLease(cleanupCtx, repoID, owner)
	stopCleanup()
	return true, errors.Join(actionErr, releaseErr)
}

// RequestRetry 使用与 Step 相同的租约边界处理管理员恢复请求。
// Manual recovery cannot race an active repository action.
func (r *Reconciler) RequestRetry(ctx context.Context, runID string) (string, error) {
	run, err := r.store.GetRun(ctx, runID)
	if err != nil {
		return "", err
	}
	retriedID := runID
	acquired, err := r.withRepoLease(ctx, run.RepoID, func(ownedCtx context.Context) error {
		current, err := r.store.GetRun(ownedCtx, runID)
		if err != nil {
			return err
		}
		if current.State == model.StateBlocked {
			// 已关闭的远端提案及其收据不再复用，显式恢复建立新 run。
			// Preserve the blocked audit record and start a new proposal identity.
			active, err := r.store.ActiveRunForRepo(ownedCtx, current.RepoID)
			if err != nil {
				return err
			}
			if active != "" {
				retriedID = active
				return nil
			}
			retriedID = "run-" + uuid.NewString() + "-" + current.RepoID
			return r.store.CreateRun(ownedCtx, model.Run{RunID: retriedID, RepoID: current.RepoID, State: model.StateIdle, Reason: "manual recovery of " + runID, StartedAt: time.Now().UTC()})
		}
		return r.store.RequestRetry(ownedCtx, runID)
	})
	if err != nil {
		return "", err
	}
	if !acquired {
		return "", ErrLeaseBusy
	}
	return retriedID, nil
}
