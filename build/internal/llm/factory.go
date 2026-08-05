package llm

import "fmt"

// NewClient 根据配置创建对应的 LLM Client 实现。
func NewClient(cfg Config) (Client, error) {
	switch cfg.Provider {
	case "anthropic":
		return NewAnthropicClient(cfg)
	case "openai_compatible":
		return NewOpenAICompatClient(cfg)
	case "mock":
		return NewMockClient(), nil
	default:
		return nil, fmt.Errorf("unsupported llm provider: %s", cfg.Provider)
	}
}
