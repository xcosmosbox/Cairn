// Package llm 提供 LLM 提供者的统一抽象接口。
// 支持通过配置切换不同的 LLM 后端（Anthropic、OpenAI 兼容等）。
// 所有实现直接放在本包内，避免子包带来的 import cycle。
package llm

import "context"

// Client 是 LLM 提供者的统一抽象接口。
type Client interface {
	Complete(ctx context.Context, req CompleteRequest) (*CompleteResponse, error)
	ProviderName() string
}

// CompleteRequest 是一次 LLM 补全请求的参数。
type CompleteRequest struct {
	System        string   `json:"system"`
	User          string   `json:"user"`
	MaxTokens     int      `json:"max_tokens,omitempty"`
	Temperature   float64  `json:"temperature,omitempty"`
	StopSequences []string `json:"stop_sequences,omitempty"`
}

// CompleteResponse 是一次 LLM 补全请求的响应。
type CompleteResponse struct {
	Text         string `json:"text"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	FinishReason string `json:"finish_reason"`
}

// Config LLM 提供者配置。
type Config struct {
	Provider    string  // anthropic | openai_compatible | anthropic_compatible | mock
	APIKeyEnv   string  // 环境变量名
	Model       string  // 模型标识
	MaxTokens   int     // 默认 4096
	Temperature float64 // 默认 0.0
	Endpoint    string  // 可选，覆盖默认 endpoint
	Timeout     string  // 默认 "60s"
	MaxRetries  int     // 默认 3
}
