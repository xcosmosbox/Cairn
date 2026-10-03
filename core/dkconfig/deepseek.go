package dkconfig

import (
	"net/url"
	"strings"
)

// DeepSeek 默认值按官方 Chat Completions 规格，64K 是标准 thinking 默认输出预算。
// DeepSeek defaults follow the official API; 384K is a limit, not its default budget.
const (
	DeepSeekEndpoint              = "https://api.deepseek.com/chat/completions"
	DeepSeekFlashModel            = "deepseek-flash"
	DeepSeekDefaultThinkingTokens = 64 * 1024
	DeepSeekMaxOutputTokens       = 384 * 1024
)

// IsDeepSeekEndpoint 只对官方主机应用 DeepSeek 的限制，保留其他兼容后端的规格。
// IsDeepSeekEndpoint keeps provider-specific limits away from other compatible APIs.
func IsDeepSeekEndpoint(endpoint string) bool {
	u, err := url.Parse(endpoint)
	return err == nil && strings.EqualFold(u.Hostname(), "api.deepseek.com")
}
