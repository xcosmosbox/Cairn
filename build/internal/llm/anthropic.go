package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"
)

const (
	defaultAnthropicEndpoint = "https://api.anthropic.com/v1/messages"
	anthropicAPIVersion     = "2023-06-01"
)

// AnthropicClient 是 Anthropic Messages API 的 LLM Client 实现。
type AnthropicClient struct {
	config     Config
	httpClient *http.Client
	endpoint   string
	apiKeyEnv  string // resolved API key env var name
}

// NewAnthropicClient 创建 Anthropic Client 实例。
func NewAnthropicClient(cfg Config) (*AnthropicClient, error) {
	apiKeyEnv := cfg.APIKeyEnv
	if apiKeyEnv == "" {
		apiKeyEnv = "ANTHROPIC_API_KEY"
	}
	if os.Getenv(apiKeyEnv) == "" {
		// 不强制启动时报错——允许 dry_run 模式无 key
	}

	timeout := 60 * time.Second
	if cfg.Timeout != "" {
		if parsed, err := time.ParseDuration(cfg.Timeout); err == nil {
			timeout = parsed
		}
	}

	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = defaultAnthropicEndpoint
	}

	return &AnthropicClient{
		config:     cfg,
		httpClient: &http.Client{Timeout: timeout},
		endpoint:   endpoint,
		apiKeyEnv:  apiKeyEnv,
	}, nil
}

func (c *AnthropicClient) ProviderName() string { return "anthropic" }

func (c *AnthropicClient) Complete(ctx context.Context, req CompleteRequest) (*CompleteResponse, error) {
	// 复用共享的指数退避重试循环（见 retry.go），避免各 provider 重复实现。
	return retryComplete(ctx, "anthropic", c.config.MaxRetries, func(ctx context.Context) (*CompleteResponse, error) {
		return c.doComplete(ctx, req)
	})
}

func (c *AnthropicClient) doComplete(ctx context.Context, req CompleteRequest) (*CompleteResponse, error) {
	body := anthropicReq{
		Model:       c.config.Model,
		MaxTokens:   pickInt(req.MaxTokens, c.config.MaxTokens, 4096),
		Temperature: pickFloat(req.Temperature, c.config.Temperature, 0.0),
		System:      req.System,
		Messages:    []anthropicMsg{{Role: "user", Content: req.User}},
	}
	if len(req.StopSequences) > 0 {
		body.StopSequences = req.StopSequences
	}

	b, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("anthropic: marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.endpoint, bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("anthropic: build request: %w", err)
	}
	httpReq.Header.Set("x-api-key", os.Getenv(c.apiKeyEnv))
	httpReq.Header.Set("anthropic-version", anthropicAPIVersion)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil { return nil, fmt.Errorf("anthropic: %w", err) }
	defer resp.Body.Close()

	if resp.StatusCode == 429 {
		return nil, &RateLimitError{RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
	}
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("anthropic: status %d: %s", resp.StatusCode, string(body))
	}

	var ar anthropicResp
	if err := json.NewDecoder(resp.Body).Decode(&ar); err != nil {
		return nil, fmt.Errorf("anthropic: decode: %w", err)
	}
	return &CompleteResponse{
		Text: ar.firstText(), InputTokens: ar.Usage.InputTokens,
		OutputTokens: ar.Usage.OutputTokens, FinishReason: ar.StopReason,
	}, nil
}

type anthropicReq struct {
	Model         string         `json:"model"`
	Messages      []anthropicMsg `json:"messages"`
	System        string         `json:"system,omitempty"`
	MaxTokens     int            `json:"max_tokens"`
	Temperature   float64        `json:"temperature,omitempty"`
	StopSequences []string       `json:"stop_sequences,omitempty"`
}
type anthropicMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type anthropicResp struct {
	Content    []struct{ Type, Text string } `json:"content"`
	StopReason string                        `json:"stop_reason"`
	Usage      struct{ InputTokens, OutputTokens int } `json:"usage"`
}
func (r *anthropicResp) firstText() string {
	for _, c := range r.Content { if c.Type == "text" { return c.Text } }
	return ""
}

type RateLimitError struct{ RetryAfter time.Duration }
func (e *RateLimitError) Error() string { return fmt.Sprintf("rate limited, retry after %v", e.RetryAfter) }

func isRetryable(err error) bool {
	if err == nil { return false }
	var re *RateLimitError
	return errors.As(err, &re)
}

func parseRetryAfter(v string) time.Duration {
	if v == "" { return 5 * time.Second }
	if s, err := strconv.Atoi(v); err == nil {
		d := time.Duration(s) * time.Second
		if d > 5*time.Minute { d = 5 * time.Minute }
		return d
	}
	return 5 * time.Second
}

func pickInt(req, cfg, def int) int {
	if req > 0 { return req }
	if cfg > 0 { return cfg }
	return def
}
func pickFloat(req, cfg, def float64) float64 {
	if req > 0 { return req }
	if cfg > 0 { return cfg }
	return def
}
