package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"time"
)

// defaultOpenAIEndpoint 是未显式配置 Endpoint 时的兜底端点（DeepSeek OpenAI 兼容）。
// defaultOpenAIEndpoint is the fallback endpoint when Config.Endpoint is empty.
const defaultOpenAIEndpoint = "https://api.deepseek.com/chat/completions"

// OpenAICompatClient 是 OpenAI /chat/completions 兼容端点的 Client 实现，
// 主要用于接入 DeepSeek 的 JSON mode + thinking mode。
//
// OpenAICompatClient implements Client against an OpenAI-compatible
// /chat/completions endpoint (used for DeepSeek's JSON + thinking modes).
//
// 与 AnthropicClient 的关键差异（证据驱动，见 domain-extraction-contract.md 第七节）：
//   - 认证走 Authorization: Bearer 而非 x-api-key
//   - 请求体带 response_format:{type:json_object} 保证返回合法 JSON
//   - 请求体带 thinking:{type:enabled} 开启思维链，响应含 reasoning_content
//   - thinking 模式下 DeepSeek 禁用 temperature/top_p/presence_penalty/
//     frequency_penalty——这些参数一律不发送，否则返回 400
type OpenAICompatClient struct {
	config     Config
	httpClient *http.Client
	endpoint   string
	apiKeyEnv  string // 已解析的 API key 环境变量名 / resolved API key env var name
}

// NewOpenAICompatClient 创建 OpenAI 兼容 Client 实例。
// 复用现有 Config（Endpoint/Model/APIKeyEnv/Timeout/MaxRetries 均已够用）。
func NewOpenAICompatClient(cfg Config) (*OpenAICompatClient, error) {
	apiKeyEnv := cfg.APIKeyEnv
	if apiKeyEnv == "" {
		apiKeyEnv = "DK_LLM_API_KEY"
	}
	// 不强制启动时报错——与 AnthropicClient 保持一致，允许 dry_run 无 key。

	timeout := 60 * time.Second
	if cfg.Timeout != "" {
		if parsed, err := time.ParseDuration(cfg.Timeout); err == nil {
			timeout = parsed
		}
	}

	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = defaultOpenAIEndpoint
	}

	return &OpenAICompatClient{
		config:     cfg,
		httpClient: &http.Client{Timeout: timeout},
		endpoint:   endpoint,
		apiKeyEnv:  apiKeyEnv,
	}, nil
}

func (c *OpenAICompatClient) ProviderName() string { return "openai_compatible" }

func (c *OpenAICompatClient) Complete(ctx context.Context, req CompleteRequest) (*CompleteResponse, error) {
	// 复用共享的指数退避重试循环（见 retry.go），与 AnthropicClient 一致（DRY）。
	return retryComplete(ctx, "openai_compatible", c.config.MaxRetries, func(ctx context.Context) (*CompleteResponse, error) {
		return c.doComplete(ctx, req)
	})
}

func (c *OpenAICompatClient) doComplete(ctx context.Context, req CompleteRequest) (*CompleteResponse, error) {
	// 组装 OpenAI /chat/completions 请求体。
	// system + user 两条消息；开启 JSON mode 与 thinking mode。
	body := openAIReq{
		Model: c.config.Model,
		Messages: []openAIMsg{
			{Role: "system", Content: req.System},
			{Role: "user", Content: req.User},
		},
		MaxTokens:      pickInt(req.MaxTokens, c.config.MaxTokens, 4096),
		ResponseFormat: &openAIResponseFormat{Type: "json_object"},
		Thinking:       &openAIThinking{Type: "enabled"},
	}
	if len(req.StopSequences) > 0 {
		body.Stop = req.StopSequences
	}
	// 关键：thinking 模式下 DeepSeek 禁用 temperature/top_p/presence_penalty/
	// frequency_penalty，发送任一都会 400。故这些字段一律不设置——请求体里根本没有。
	// NOTE: temperature 等被禁参数在 openAIReq 中已省略，绝不发送。

	b, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("openai_compatible: marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.endpoint, bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("openai_compatible: build request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+os.Getenv(c.apiKeyEnv))
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		// 区分超时 / context 取消 / 其他网络错误，便于排查。
		if ctx.Err() != nil {
			log.Printf("[llm] http 调用失败: context 已取消 (ctx_err=%v): %v", ctx.Err(), err)
			return nil, fmt.Errorf("openai_compatible: context canceled: %w", err)
		}
		if isTimeoutErr(err) {
			log.Printf("[llm] http 调用超时 (Client.Timeout=%s): %v", c.httpClient.Timeout, err)
			return nil, fmt.Errorf("openai_compatible: timeout (Client.Timeout=%s): %w", c.httpClient.Timeout, err)
		}
		log.Printf("[llm] http 调用失败 (非超时): %v", err)
		return nil, fmt.Errorf("openai_compatible: http do: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 429 {
		// 复用 anthropic.go 已有的 RateLimitError / parseRetryAfter（DRY）。
		return nil, &RateLimitError{RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
	}
	if resp.StatusCode != 200 {
		errBody, _ := io.ReadAll(resp.Body)
		log.Printf("[llm] API 返回非 200: status=%d body=%s", resp.StatusCode, truncateForLog(errBody))
		return nil, fmt.Errorf("openai_compatible: status %d: %s", resp.StatusCode, string(errBody))
	}

	// 先读取完整 body 到 buffer，再解码——decode 失败时可记录原始响应用于排查。
	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("[llm] 读取响应 body 失败 (可能超时截断): status=%d err=%v", resp.StatusCode, err)
		return nil, fmt.Errorf("openai_compatible: read body: %w", err)
	}

	var or openAIResp
	if err := json.Unmarshal(rawBody, &or); err != nil {
		log.Printf("[llm] JSON 解码失败: status=%d body_len=%d err=%v\nraw_body: %s",
			resp.StatusCode, len(rawBody), err, truncateForLog(rawBody))
		return nil, fmt.Errorf("openai_compatible: decode: %w", err)
	}
	if len(or.Choices) == 0 {
		return nil, fmt.Errorf("openai_compatible: response has no choices")
	}

	msg := or.Choices[0].Message
	// 日志化每次调用：content 与 reasoning_content 都写入日志，便于审计与调试；
	// 仅 content 作为返回进入领域知识层（reasoning_content 是思维链，不入库）。
	// Log both content and reasoning_content per call; only content is returned.
	logExtraction(c.config.Model, msg.Content, msg.ReasoningContent,
		or.Usage.PromptTokens, or.Usage.CompletionTokens)

	return &CompleteResponse{
		Text:         msg.Content,
		InputTokens:  or.Usage.PromptTokens,
		OutputTokens: or.Usage.CompletionTokens,
		FinishReason: or.Choices[0].FinishReason,
	}, nil
}

// logExtraction 记录 LLM 提取调用的摘要（不打 content/reasoning 全文，避免刷屏）。
// 如需查看完整响应，请在 dump-dir 中查看标注/提取 JSON 中间产物。
func logExtraction(model, content, reasoning string, promptTok, completionTok int) {
	log.Printf("[llm-extract] model=%s tokens(prompt=%d completion=%d) content_len=%d reasoning_len=%d",
		model, promptTok, completionTok, len(content), len(reasoning))
}

// isTimeoutErr 判断是否为超时类错误（net.Error.Timeout() 或 context.DeadlineExceeded）。
// isTimeoutErr reports whether the error is a timeout (net.Error.Timeout or context deadline).
func isTimeoutErr(err error) bool {
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		return true
	}
	if ctx, ok := err.(interface{ Deadline() (time.Time, bool) }); ok {
		_, ok2 := ctx.Deadline()
		return ok2
	}
	return false
}

// truncateForLog 把 byte slice 截断到 2000 字符，避免日志过长。
// truncateForLog truncates a byte slice to 2000 chars for logging.
func truncateForLog(b []byte) string {
	s := string(b)
	if len(s) > 2000 {
		return s[:2000] + "...(truncated)"
	}
	return s
}

// ---- 请求 / 响应结构（OpenAI /chat/completions 形态）----
// 字段与 /tmp/claude/dk-llm-verify 已验证通过的形态一致。

type openAIReq struct {
	Model          string                `json:"model"`
	Messages       []openAIMsg           `json:"messages"`
	MaxTokens      int                   `json:"max_tokens"`
	ResponseFormat *openAIResponseFormat `json:"response_format,omitempty"`
	Thinking       *openAIThinking       `json:"thinking,omitempty"`
	Stop           []string              `json:"stop,omitempty"`
	// 刻意不含 temperature/top_p/presence_penalty/frequency_penalty：
	// thinking 模式下发送它们会导致 DeepSeek 返回 400。
}

type openAIMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIResponseFormat struct {
	Type string `json:"type"` // "json_object"
}

type openAIThinking struct {
	Type string `json:"type"` // "enabled"
}

type openAIResp struct {
	Choices []struct {
		Message struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}
