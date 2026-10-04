// Package dkconfig 提供领域知识层控制器（cairnd）、构建端与查询端共用的规范配置。
//
// 设计要点（产品化 Prompt §5）：
//   - secret 只存「环境变量名」或「文件路径」，绝不存值本身（§4.3）。
//   - GitHub App private key 的 file/env 二选一，启动时冲突即报错。
//   - id 是 controller 内稳定身份，不由本地路径推导。
//   - owner/name、branch、catalog path 等严格校验。
//   - 默认 auto_merge_on_green=false；默认不执行源仓库脚本/workflow/hook/submodule。
//   - 每repo串行、跨repo并发。
//
// 禁止让新 controller 跨包依赖 service/internal/config；本包是模块级共享包。
package dkconfig

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

// Mode 决定 cairnd 运行模式。
type Mode string

const (
	ModeDaemon Mode = "daemon" // 常驻轮询 + webhook
	ModeOnce   Mode = "once"   // 跑一轮 reconcile 后退出
)

// Config 是 cairnd / cairnctl / 构建端共用的顶层配置。
type Config struct {
	Service ServiceConfig `yaml:"service"`
	GitHub  GitHubConfig  `yaml:"github"`
	LLM     LLMConfig     `yaml:"llm"`
	Catalog CatalogConfig `yaml:"catalog"`
	Repos   []RepoConfig  `yaml:"repos"`
}

// ServiceConfig 是控制器自身的服务层配置。
type ServiceConfig struct {
	Mode              Mode        `yaml:"mode"`
	PollInterval      Duration    `yaml:"poll_interval"`
	StateDB           string      `yaml:"state_db"`
	WorkspacesDir     string      `yaml:"workspaces_dir"`
	CacheDir          string      `yaml:"cache_dir"`
	BundleDir         string      `yaml:"bundle_dir"`
	ListenAddr        string      `yaml:"listen_addr"`
	AdminTokenEnv     string      `yaml:"admin_token_env"`
	MaxConcurrentRepos int         `yaml:"max_concurrent_repos"`
}

// GitHubConfig 是 GitHub App 凭证与端点配置（§4）。
// private_key_file 与 private_key_env 互斥；二者皆空时仅允许 fake/mock 模式。
type GitHubConfig struct {
	AppID            int64  `yaml:"app_id"`
	PrivateKeyFile   string `yaml:"private_key_file"`
	PrivateKeyEnv    string `yaml:"private_key_env"`
	WebhookSecretEnv string `yaml:"webhook_secret_env"`
	APIBaseURL       string `yaml:"api_base_url"`
}

// LLMConfig 是构建端 LLM 客户端配置（OpenAI 兼容）。
type LLMConfig struct {
	Provider   string   `yaml:"provider"`
	Endpoint   string   `yaml:"endpoint"`
	APIKeyEnv  string   `yaml:"api_key_env"`
	Model      string   `yaml:"model"`
	MaxTokens  int      `yaml:"max_tokens"`
	Timeout    Duration `yaml:"timeout"`
	MaxRetries int      `yaml:"max_retries"`
}

// CatalogConfig 是 Catalog Repo 发布配置。
type CatalogConfig struct {
	Repo               string `yaml:"repo"`
	Branch             string `yaml:"branch"`
	ManifestDir        string `yaml:"manifest_dir"`
	ReleaseTagTemplate string `yaml:"release_tag_template"`
	ReleasePrerelease  bool   `yaml:"release_prerelease"`
}

// RewriteConfig 控制 Source Repo 回写 PR 策略。
type RewriteConfig struct {
	Mode            string  `yaml:"mode"`              // pr | direct
	MinConfidence   float64 `yaml:"min_confidence"`
	AutoMergeOnGreen bool   `yaml:"auto_merge_on_green"`
	PRSummaryLLM    bool    `yaml:"pr_summary_llm"`     // 启用 LLM 生成 PR 描述
}

// RepoConfig 是单个被观测 Source Repo 的配置。
type RepoConfig struct {
	ID       string        `yaml:"id"`
	URL      string        `yaml:"url"`
	Owner    string        `yaml:"owner"`
	Name     string        `yaml:"name"`
	Branch   string        `yaml:"branch"`
	KGGroup  string        `yaml:"kg_group"`
	Rewrite  RewriteConfig `yaml:"rewrite"`
}

// Duration 是可从 YAML 字符串解析的 time.Duration（如 "5m"、"600s"）。
type Duration time.Duration

// UnmarshalYAML 支持 "5m" / "600s" 等字符串解析。
func (d *Duration) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var s string
	if err := unmarshal(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("dkconfig: 解析时长 %q 失败: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

// Std 返回标准 time.Duration。
func (d Duration) Std() time.Duration { return time.Duration(d) }

// ─── 校验正则 ────────────────────────────────────────────────────

var (
	repoIDRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	ownerRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)
	nameRe     = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
	branchRe   = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,200}$`)
	kgGroupRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	envNameRe  = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	pathSafeRe = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
)

// Validate 校验整份配置的合法性与一致性。返回第一个错误。
func (c *Config) Validate() error {
	if c.Service.Mode != "" && c.Service.Mode != ModeDaemon && c.Service.Mode != ModeOnce {
		return fmt.Errorf("dkconfig: service.mode 非法 %q（允许 daemon|once）", c.Service.Mode)
	}
	if c.Service.PollInterval.Std() <= 0 && c.Service.Mode != ModeOnce {
		c.Service.PollInterval = Duration(1 * time.Minute)
	}
	if c.Service.ListenAddr == "" {
		c.Service.ListenAddr = "127.0.0.1:7810"
	}
	if c.Service.MaxConcurrentRepos <= 0 {
		c.Service.MaxConcurrentRepos = 1
	}
	if c.Service.StateDB == "" {
		return fmt.Errorf("dkconfig: service.state_db 不可为空")
	}

	// GitHub private key file/env 互斥（§4.3）。
	if c.GitHub.PrivateKeyFile != "" && c.GitHub.PrivateKeyEnv != "" {
		return fmt.Errorf("dkconfig: github.private_key_file 与 private_key_env 不可同时设置")
	}
	if c.GitHub.PrivateKeyEnv != "" && !envNameRe.MatchString(c.GitHub.PrivateKeyEnv) {
		return fmt.Errorf("dkconfig: github.private_key_env 非法环境变量名 %q", c.GitHub.PrivateKeyEnv)
	}
	if c.GitHub.WebhookSecretEnv != "" && !envNameRe.MatchString(c.GitHub.WebhookSecretEnv) {
		return fmt.Errorf("dkconfig: github.webhook_secret_env 非法环境变量名 %q", c.GitHub.WebhookSecretEnv)
	}
	if c.GitHub.APIBaseURL == "" {
		c.GitHub.APIBaseURL = "https://api.github.com"
	}

	// LLM。
	if c.LLM.Provider == "" {
		c.LLM.Provider = "openai_compatible"
	}
	if c.LLM.Endpoint == "" {
		c.LLM.Endpoint = DeepSeekEndpoint
	}
	if c.LLM.APIKeyEnv == "" {
		c.LLM.APIKeyEnv = "CAIRN_LLM_API_KEY"
	}
	if !envNameRe.MatchString(c.LLM.APIKeyEnv) {
		return fmt.Errorf("dkconfig: llm.api_key_env 非法环境变量名 %q", c.LLM.APIKeyEnv)
	}
	if c.LLM.Model == "" {
		c.LLM.Model = DeepSeekFlashModel
	}
	if c.LLM.MaxTokens <= 0 {
		c.LLM.MaxTokens = DeepSeekDefaultThinkingTokens
	}
	if IsDeepSeekEndpoint(c.LLM.Endpoint) && c.LLM.MaxTokens > DeepSeekMaxOutputTokens {
		return fmt.Errorf("dkconfig: llm.max_tokens %d exceeds DeepSeek maximum %d", c.LLM.MaxTokens, DeepSeekMaxOutputTokens)
	}
	if c.LLM.Timeout.Std() <= 0 {
		c.LLM.Timeout = Duration(600 * time.Second)
	}
	if c.LLM.MaxRetries < 0 {
		c.LLM.MaxRetries = 3
	}

	// Catalog。
	if c.Catalog.Repo != "" {
		if err := validateRepoSlug(c.Catalog.Repo); err != nil {
			return fmt.Errorf("dkconfig: catalog.repo: %w", err)
		}
	}
	if c.Catalog.Branch == "" {
		c.Catalog.Branch = "main"
	}
	if !branchRe.MatchString(c.Catalog.Branch) {
		return fmt.Errorf("dkconfig: catalog.branch 非法 %q", c.Catalog.Branch)
	}
	if c.Catalog.ManifestDir == "" {
		c.Catalog.ManifestDir = "knowledge-bases"
	}
	if !pathSafeRe.MatchString(c.Catalog.ManifestDir) || strings.Contains(c.Catalog.ManifestDir, "..") {
		return fmt.Errorf("dkconfig: catalog.manifest_dir 非法或含逃逸 %q", c.Catalog.ManifestDir)
	}
	if c.Catalog.ReleaseTagTemplate == "" {
		c.Catalog.ReleaseTagTemplate = "kb-{group}-{source_sha}"
	}

	// Repos。
	if len(c.Repos) == 0 {
		return fmt.Errorf("dkconfig: repos 不可为空")
	}
	seen := map[string]bool{}
	for i := range c.Repos {
		r := &c.Repos[i]
		if err := r.validate(); err != nil {
			return fmt.Errorf("dkconfig: repos[%d] (%s): %w", i, r.ID, err)
		}
		if seen[r.ID] {
			return fmt.Errorf("dkconfig: repos[%d] id %q 重复", i, r.ID)
		}
		seen[r.ID] = true
	}
	return nil
}

func (r *RepoConfig) validate() error {
	if !repoIDRe.MatchString(r.ID) {
		return fmt.Errorf("id 非法 %q（小写字母数字与连字符，不以连字符开头）", r.ID)
	}
	if r.URL == "" {
		return fmt.Errorf("url 不可为空")
	}
	if !ownerRe.MatchString(r.Owner) {
		return fmt.Errorf("owner 非法 %q", r.Owner)
	}
	if !nameRe.MatchString(r.Name) {
		return fmt.Errorf("name 非法 %q", r.Name)
	}
	if !branchRe.MatchString(r.Branch) {
		return fmt.Errorf("branch 非法 %q", r.Branch)
	}
	if r.Branch == "" {
		r.Branch = "main"
	}
	if !kgGroupRe.MatchString(r.KGGroup) {
		return fmt.Errorf("kg_group 非法 %q", r.KGGroup)
	}
	if r.Rewrite.Mode == "" {
		r.Rewrite.Mode = "pr"
	}
	if r.Rewrite.Mode != "pr" && r.Rewrite.Mode != "direct" {
		return fmt.Errorf("rewrite.mode 非法 %q（允许 pr|direct）", r.Rewrite.Mode)
	}
	// 默认 auto_merge_on_green=false（§5）。
	return nil
}

func validateRepoSlug(slug string) error {
	parts := strings.SplitN(slug, "/", 2)
	if len(parts) != 2 || !ownerRe.MatchString(parts[0]) || !nameRe.MatchString(parts[1]) {
		return fmt.Errorf("repo slug 非法 %q（应为 owner/name）", slug)
	}
	return nil
}

// ResolveSecret 从环境变量名读取 secret 值。envName 为空返回空串。
// 调用方负责不在日志中输出返回值。
func ResolveSecret(envName string) string {
	if envName == "" {
		return ""
	}
	return os.Getenv(envName)
}

// ResolvePrivateKey 解析 GitHub App private key：优先 env 变量值，其次文件路径。
// 二者皆空返回 ("", nil)（fake/mock 模式）。
func (g *GitHubConfig) ResolvePrivateKey() ([]byte, error) {
	if g.PrivateKeyEnv != "" {
		v := os.Getenv(g.PrivateKeyEnv)
		if v == "" {
			return nil, fmt.Errorf("dkconfig: 环境变量 %s 未设置或为空", g.PrivateKeyEnv)
		}
		return []byte(v), nil
	}
	if g.PrivateKeyFile != "" {
		data, err := os.ReadFile(g.PrivateKeyFile)
		if err != nil {
			return nil, fmt.Errorf("dkconfig: 读取 private key 文件 %s: %w", g.PrivateKeyFile, err)
		}
		return data, nil
	}
	return nil, nil
}

// HasGitHubApp 报告是否配置了真实 GitHub App 凭证。
func (g *GitHubConfig) HasGitHubApp() bool {
	return g.AppID != 0 && (g.PrivateKeyFile != "" || g.PrivateKeyEnv != "")
}

// RepoByID 按 id 查找 repo 配置。
func (c *Config) RepoByID(id string) (*RepoConfig, bool) {
	for i := range c.Repos {
		if c.Repos[i].ID == id {
			return &c.Repos[i], true
		}
	}
	return nil, false
}

// Defaults 返回带合理默认值的零配置（用于测试与 fake 模式）。
func Defaults() Config {
	return Config{
		Service: ServiceConfig{
			Mode:               ModeDaemon,
			PollInterval:       Duration(1 * time.Minute),
			ListenAddr:         "127.0.0.1:7810",
			MaxConcurrentRepos: 1,
		},
		GitHub: GitHubConfig{
			APIBaseURL: "https://api.github.com",
		},
		LLM: LLMConfig{
			Provider:   "openai_compatible",
			Endpoint:   DeepSeekEndpoint,
			APIKeyEnv:  "CAIRN_LLM_API_KEY",
			Model:      DeepSeekFlashModel,
			MaxTokens:  DeepSeekDefaultThinkingTokens,
			Timeout:    Duration(600 * time.Second),
			MaxRetries: 3,
		},
		Catalog: CatalogConfig{
			Branch:             "main",
			ManifestDir:        "knowledge-bases",
			ReleaseTagTemplate: "kb-{group}-{source_sha}",
			ReleasePrerelease:  true,
		},
	}
}
