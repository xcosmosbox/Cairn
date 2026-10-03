package publisher

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/xcosmosbox/cairn/build/internal/controller/model"
	"github.com/xcosmosbox/cairn/build/internal/controller/store"
	"github.com/xcosmosbox/cairn/core/kbbundle"
)

// EffectJournal 是持久化 outbox。租约在 Step 层提供互斥，pending 重放依赖远端
// 的 Ensure 操作；applied 只在完整响应已落盘时跳过，防止丢失 remote-success 窗口。
// EffectJournal records immutable requests and their successful remote responses.
type EffectJournal interface {
	ClaimEffect(context.Context, model.ExternalEffect) (string, bool, error)
	GetEffect(context.Context, string) (*model.ExternalEffect, error)
	MarkEffectApplied(context.Context, string, string, string) error
	MarkEffectFailed(context.Context, string, string) error
}

func Effect[T any](ctx context.Context, journal EffectJournal, kind, target string, request any, call func() (T, error)) (T, error) {
	var zero T
	if err := store.CheckLease(ctx); err != nil {
		return zero, err
	}
	data, err := json.Marshal(request)
	if err != nil {
		return zero, err
	}
	fp := kbbundle.DigestPrefix(kbbundle.DigestBytes(data))
	key := kind + ":" + target + ":" + fp
	if journal == nil {
		if err := store.CheckLease(ctx); err != nil {
			return zero, err
		}
		return call()
	}
	_, applied, err := journal.ClaimEffect(ctx, model.ExternalEffect{EffectKey: key, EffectType: kind, TargetRepo: target, RequestFingerprint: fp})
	if err != nil {
		return zero, fmt.Errorf("publisher: claim %s effect: %w", kind, err)
	}
	if applied {
		e, err := journal.GetEffect(ctx, key)
		if err != nil {
			return zero, err
		}
		if e == nil || e.ResponseSummary == "" {
			return zero, fmt.Errorf("publisher: applied %s has no durable response", kind)
		}
		var value T
		if err := json.Unmarshal([]byte(e.ResponseSummary), &value); err != nil {
			return zero, fmt.Errorf("publisher: decode durable %s response: %w", kind, err)
		}
		return value, nil
	}
	if err := store.CheckLease(ctx); err != nil {
		return zero, err
	}
	value, err := call()
	if err != nil {
		if markErr := journal.MarkEffectFailed(ctx, key, err.Error()); markErr != nil {
			return zero, fmt.Errorf("publisher: %s failed (%v); journal failed: %w", kind, err, markErr)
		}
		return zero, err
	}
	response, err := json.Marshal(value)
	if err != nil {
		return zero, err
	}
	if err := journal.MarkEffectApplied(ctx, key, fp, string(response)); err != nil {
		return zero, fmt.Errorf("publisher: persist successful %s effect: %w", kind, err)
	}
	return value, nil
}
