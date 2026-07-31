// Package config provides configuration loading, defaults, validation, and merging
// for the Cairn v2.0.
//
// 配置模块负责从 YAML 文件加载应用配置，支持环境变量覆盖、默认值填充和字段级深度合并。
// v2.0 新增 6 个顶层 Section：service, llm, output, defaults, repos, mcp。
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config 是应用的全部配置，对应 v2.0 YAML schema。
// 顶层 6 个大块：service / llm / output / defaults / repos / mcp。
type Config struct {
	Service  ServiceConfig   `yaml:"service"`
	LLM      LLMConfig       `yaml:"llm"`
	Output   OutputConfig    `yaml:"output"`
	Defaults DefaultsConfig  `yaml:"defaults"`
	Repos    []RepoConfig    `yaml:"repos"`
	MCP      MCPConfig       `yaml:"mcp"`
	GitHub   GitHubAppConfig `yaml:"github"`
}

// GitHubAppConfig — GitHub App 鉴权配置（MCP Server 复用 cairnd 的同一 App）。
// 配置后，catalog 自动更新使用 GitHub App installation token，
// 无需单独创建 PAT。与 cairnd 的 github 配置格式一致。
type GitHubAppConfig struct {
	AppID          int64  `yaml:"app_id"`
	PrivateKeyFile string `yaml:"private_key_file"`
	PrivateKeyEnv  string `yaml:"private_key_env"`
	APIBaseURL     string `yaml:"api_base_url"`
}

// ResolvePrivateKey 从文件或环境变量读取 PEM 编码的 private key。
func (g *GitHubAppConfig) ResolvePrivateKey() ([]byte, error) {
	if g.PrivateKeyFile != "" {
		return os.ReadFile(g.PrivateKeyFile)
	}
	if g.PrivateKeyEnv != "" {
		return []byte(os.Getenv(g.PrivateKeyEnv)), nil
	}
	return nil, fmt.Errorf("github: private_key_file 或 private_key_env 未配置")
}

// ServiceConfig — 流水线服务运行模式配置。
// 控制守护进程/一次性/监听三种运行模式及对应的目录路径。
type ServiceConfig struct {
	Mode          string `yaml:"mode"`           // daemon | once | watch
	PollInterval  string `yaml:"poll_interval"`  // 轮询间隔，如 "5m"，默认 "5m"
	WorkspacesDir string `yaml:"workspaces_dir"` // 工作区目录，默认 ".workspaces"
	CacheDir      string `yaml:"cache_dir"`      // 缓存目录，默认 ".cache"
	BuildDir      string `yaml:"build_dir"`      // 构建输出目录，默认 ".build"
}

// LLMConfig — LLM 提供者配置。
// 支持 Anthropic、OpenAI-compatible 和 Anthropic-compatible 三种 provider。
type LLMConfig struct {
	Provider    string  `yaml:"provider"`               // anthropic | openai_compatible | anthropic_compatible
	APIKeyEnv   string  `yaml:"api_key_env"`            // 存放 API Key 的环境变量名
	Model       string  `yaml:"model"`                  // 模型名称，如 "claude-opus-4-8"
	MaxTokens   int     `yaml:"max_tokens"`             // 最大输出 token 数，默认 4096
	Temperature float64 `yaml:"temperature"`            // 采样温度，默认 0.0
	Endpoint    string  `yaml:"endpoint,omitempty"`     // 自定义 API endpoint（openai_compatible 时使用）
	Timeout     string  `yaml:"timeout"`                // 请求超时，如 "60s"，默认 "60s"
	MaxRetries  int     `yaml:"max_retries"`            // 最大重试次数，默认 3
}

// OutputConfig — .db 知识库文件输出和分发配置。
// 定义输出的文件名模板及发布目标。
type OutputConfig struct {
	DBFilenameTemplate string            `yaml:"db_filename_template"` // 文件名模板，默认 "knowledge-{group}.db"
	Publishers         []PublisherConfig `yaml:"publishers"`           // 发布目标列表
}

// PublisherConfig — 单个发布目标配置。
// 目前支持 github_release 类型，将 .db 文件发布为 GitHub Release assets。
type PublisherConfig struct {
	Type                string `yaml:"type"`                   // github_release
	Repo                string `yaml:"repo"`                   // 目标仓库，如 "owner/repo"
	TagTemplate         string `yaml:"tag_template"`           // 版本标签模板
	RollingTagTemplate  string `yaml:"rolling_tag_template"`   // 滚动标签模板
	AssetsPerRelease    string `yaml:"assets_per_release"`     // per_group | bundled
	IncludeCrossKGLinks bool   `yaml:"include_cross_kg_links"` // 是否包含跨知识图谱链接
	IncludeManifest     bool   `yaml:"include_manifest"`       // 是否包含 manifest 文件
	TokenEnv            string `yaml:"token_env"`              // 存放 GitHub Token 的环境变量名
}

// DefaultsConfig — 全局默认值，可被 repo 级同名字段覆盖。
// 覆盖策略：字段级深度合并（非整个对象替换），repo 非零值覆盖 defaults。
type DefaultsConfig struct {
	SkillDiscovery DiscoveryConfig `yaml:"skill_discovery"` // Skill 发现规则的全局默认值
	Rewrite        RewriteConfig   `yaml:"rewrite"`          // 标注/重写工作流的全局默认值
}

// DiscoveryConfig — Skill 发现规则配置。
// 控制从仓库中检测和定位 SKILL.md 及相关引用文件的行为。
type DiscoveryConfig struct {
	Markers           []string `yaml:"markers"`              // Skill 标记文件名，默认 ["SKILL.md"]
	ReferenceDirs     []string `yaml:"reference_dirs"`       // 引用文件搜索目录，默认 ["ref"]
	ReferenceGlob     string   `yaml:"reference_glob"`       // 引用文件 glob 模式，默认 "**/*.md"
	IgnorePaths       []string `yaml:"ignore_paths"`         // 忽略的路径（glob 模式列表）
	RecurseIntoSkills bool     `yaml:"recurse_into_skills"`  // 是否递归进入 skill 子目录，默认 false
	SkillRootPaths    []string `yaml:"skill_root_paths"`     // Skill 根路径列表
}

// RewriteConfig — 标注/重写工作流配置。
// 控制 AI 标注结果的提交方式（PR / 直接推送 / 仅本地 / 干运行）。
type RewriteConfig struct {
	Mode          string  `yaml:"mode"`           // pr | push_direct | local_only | dry_run，默认 "pr"
	MinConfidence float64 `yaml:"min_confidence"` // 最低置信度阈值，默认 0.7
	PR            PRConfig `yaml:"pr"`            // PR 模式专属配置
}

// PRConfig — PR 创建相关配置。
// 仅在 rewrite.mode 为 "pr" 时生效。
type PRConfig struct {
	BaseBranch       string   `yaml:"base_branch"`         // 目标分支，默认 "main"
	HeadBranchPrefix string   `yaml:"head_branch_prefix"`  // 源分支前缀，默认 "cairn-bot/"
	Labels           []string `yaml:"labels"`               // PR 标签列表
	Reviewers        []string `yaml:"reviewers"`            // PR Reviewer 列表
	AutoMergeOnGreen bool     `yaml:"auto_merge_on_green"`  // CI 通过后自动合并
	TitleTemplate    string   `yaml:"title_template"`       // PR 标题模板
	BodyTemplate     string   `yaml:"body_template"`        // PR 正文模板
	Forge            string   `yaml:"forge"`                // 代码托管平台: github | gitlab | gitea
	TokenEnv         string   `yaml:"token_env"`            // 存放 API Token 的环境变量名
}

// RepoConfig — 单个被监控仓库的配置。
// 每个 repo 必须指定 name 和 url；其余字段可选，不填则继承 defaults。
type RepoConfig struct {
	Name           string           `yaml:"name"`                      // 必填，仓库唯一标识
	URL            string           `yaml:"url"`                       // 必填，git clone URL
	Branch         string           `yaml:"branch"`                    // 分支名，默认 "main"
	KgGroup        string           `yaml:"kg_group"`                  // 知识图谱分组，默认等于 name
	SkillDiscovery *DiscoveryConfig `yaml:"skill_discovery,omitempty"` // 字段级覆盖 defaults.skill_discovery
	Rewrite        *RewriteConfig   `yaml:"rewrite,omitempty"`         // 字段级覆盖 defaults.rewrite
}

// MCPConfig — MCP Server 侧配置。
// 定义对外暴露的知识库列表。
type MCPConfig struct {
	KnowledgeBases []KBConfig `yaml:"knowledge_bases"` // 知识库列表
	Listen         string     `yaml:"listen"`          // HTTP/SSE 监听地址（空=stdio，非空=HTTP）

	// AbbreviationsFile 是缩写映射表路径（可选）。
	// 配置后，查询改写会把用户输入的缩写展开为全称（如 "OM" → "OrderManagement"）。
	// 留空则不做缩写扩展。加载失败只告警不中断启动——缩写表是增强项，
	// 缺它检索仍可正常工作。
	//
	// AbbreviationsFile is an optional path to the abbreviation map. A load
	// failure only warns instead of aborting startup, since it merely enhances
	// recall rather than being required for retrieval.
	AbbreviationsFile string `yaml:"abbreviations_file,omitempty"`
}

// KBConfig — 单个知识库配置条目。
type KBConfig struct {
	Name        string `yaml:"name"`        // 知识库名称
	Path        string `yaml:"path"`        // .db 文件路径
	Description string `yaml:"description"` // 知识库描述

	// Catalog auto-update 配置（全部可选）。
	// 配置 CatalogRepo 后，MCP Server 后台轮询 catalog repo，
	// 发现新 stable bundle 时自动拉取（PullRemote）并热切换 DB，
	// 确保 agent 查询始终使用最新数据。
	CatalogRepo   string `yaml:"catalog_repo,omitempty"`   // catalog repo，如 "acme/knowledge-catalog"
	CatalogBranch string `yaml:"catalog_branch,omitempty"` // catalog 分支，默认 "main"
	ManifestDir   string `yaml:"manifest_dir,omitempty"`   // manifest 目录，如 "knowledge-bases"
	InstallDir    string `yaml:"install_dir,omitempty"`    // 本地安装目录（bundle 落盘位置）
	TokenEnv      string `yaml:"token_env,omitempty"`      // GitHub Token 环境变量名
	PollInterval  string `yaml:"poll_interval,omitempty"`  // 轮询间隔，如 "5m"，默认 "5m"
}

// Load 从 YAML 文件加载配置，并应用默认值和环境变量覆盖。
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file %s: %w", path, err)
	}
	return LoadFromBytes(data)
}

// LoadFromBytes 从 YAML 字节数组加载配置。
// 加载后自动调用 ApplyDefaults 填充默认值，然后应用环境变量覆盖。
// 适用于测试场景和嵌入式配置加载。
func LoadFromBytes(data []byte) (*Config, error) {
	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	// 填充默认值并执行字段级深度合并
	ApplyDefaults(cfg)

	// 环境变量覆盖 — 支持 CI/CD 和容器化部署的运行时参数注入
	applyEnvOverrides(cfg)

	return cfg, nil
}

// applyEnvOverrides 应用环境变量覆盖。
// 优先级: 环境变量 > 配置文件 > 默认值。
func applyEnvOverrides(cfg *Config) {
	// Service
	if v := os.Getenv("CAIRN_SERVICE_MODE"); v != "" {
		cfg.Service.Mode = v
	}
	if v := os.Getenv("CAIRN_WORKSPACES_DIR"); v != "" {
		cfg.Service.WorkspacesDir = v
	}
	if v := os.Getenv("CAIRN_CACHE_DIR"); v != "" {
		cfg.Service.CacheDir = v
	}
	if v := os.Getenv("CAIRN_BUILD_DIR"); v != "" {
		cfg.Service.BuildDir = v
	}

	// LLM
	if v := os.Getenv("CAIRN_LLM_API_KEY"); v != "" {
		cfg.LLM.APIKeyEnv = v
	}
	if v := os.Getenv("CAIRN_LLM_PROVIDER"); v != "" {
		cfg.LLM.Provider = v
	}
	if v := os.Getenv("CAIRN_LLM_MODEL"); v != "" {
		cfg.LLM.Model = v
	}

	// Output
	if v := os.Getenv("CAIRN_OUTPUT_DB_TEMPLATE"); v != "" {
		cfg.Output.DBFilenameTemplate = v
	}
}
