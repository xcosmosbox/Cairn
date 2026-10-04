package dkconfig

import "testing"

func deepSeekTestConfig() Config {
	return Config{
		Service: ServiceConfig{Mode: ModeOnce, StateDB: "controller.db"},
		Repos:   []RepoConfig{{ID: "example", URL: "https://github.com/owner/source.git", Owner: "owner", Name: "source", Branch: "main", KGGroup: "example"}},
	}
}

// 默认/模板曾分别发送 Pro 与 383000/384000，无法复现同一请求规格。
// Zero config and Defaults must agree on the official Flash thinking defaults.
func TestDeepSeekDefaultsAndLimits(t *testing.T) {
	t.Parallel()
	cfg := deepSeekTestConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	defaults := Defaults().LLM
	if cfg.LLM.Model != "deepseek-flash" || cfg.LLM.MaxTokens != 65536 || cfg.LLM.Model != defaults.Model || cfg.LLM.MaxTokens != defaults.MaxTokens || cfg.LLM.Endpoint != defaults.Endpoint {
		t.Fatalf("inconsistent defaults: %+v, %+v", cfg.LLM, defaults)
	}
	cfg.LLM.MaxTokens = 393216
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.LLM.MaxTokens++
	if err := cfg.Validate(); err == nil {
		t.Fatal("official endpoint accepted too large a generation budget")
	}
	cfg.LLM.Endpoint = "https://other.example/chat/completions"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("other provider incorrectly inherits DeepSeek limit: %v", err)
	}
}
