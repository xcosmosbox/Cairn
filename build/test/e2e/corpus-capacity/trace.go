package main

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/xcosmosbox/cairn/build/internal/llm"
)

// 并发限制只用于调度；不创建累计时限、计费或历史准入门。
// Caller cancellation is the only shared cancellation source.
type boundedClient struct {
	next llm.Client
	sem  chan struct{}
}

func (c *boundedClient) ProviderName() string { return c.next.ProviderName() }
func (c *boundedClient) Complete(ctx context.Context, req llm.CompleteRequest) (*llm.CompleteResponse, error) {
	select {
	case c.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-c.sem }()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.next.Complete(ctx, req)
}
func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }
func safeError(err error) any {
	if err == nil {
		return nil
	}
	text := err.Error()
	if secret := os.Getenv("CAIRN_LLM_API_KEY"); secret != "" {
		text = strings.ReplaceAll(text, secret, "[REDACTED]")
	}
	return text
}
