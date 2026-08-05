package llm

import (
	"context"
	"fmt"
	"time"
)

// retryComplete 是所有 provider 共享的指数退避重试循环。
// retryComplete is the shared exponential-backoff retry loop used by all providers.
//
// 抽出此工具是为了 DRY：Anthropic / OpenAI 兼容等实现只需提供一次真正的
// HTTP 调用（doFn），重试策略（退避、可重试判定、次数上限）在此统一。
//   - provider：用于错误信息前缀（如 "anthropic" / "openai_compatible"）
//   - maxRetries：≤0 时回退为默认 3
//   - doFn：执行单次补全；返回可重试错误（见 isRetryable）时才会重试
func retryComplete(
	ctx context.Context,
	provider string,
	maxRetries int,
	doFn func(context.Context) (*CompleteResponse, error),
) (*CompleteResponse, error) {
	if maxRetries <= 0 {
		maxRetries = 3
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// 指数退避：2^attempt 秒，封顶 30 秒，防止长时间空等。
			// Exponential backoff capped at 30s.
			shift := 1 << attempt
			if shift > 30 {
				shift = 30
			}
			backoff := time.Duration(shift) * time.Second
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
		}
		resp, err := doFn(ctx)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		// 仅对可重试错误（如 429 限流）继续；其余立即返回。
		if !isRetryable(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("%s: failed after %d retries: %w", provider, maxRetries, lastErr)
}
