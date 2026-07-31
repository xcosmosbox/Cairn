package config

// ApplyDefaults 填充所有默认值，并对每个 repo 执行字段级深度合并。
//
// 合并规则:
//   - repo.SkillDiscovery 非 nil 时，用 MergeDiscovery(defaults, repo.SkillDiscovery) 逐字段覆盖
//   - repo.Rewrite 非 nil 时，用 MergeRewrite(defaults, repo.Rewrite) 逐字段覆盖
//   - repo.Branch 为空时，默认 "main"
//   - repo.KgGroup 为空时，默认等于 repo.Name
//   - 顶层 Service/LLM/Output 使用各字段的零值判断来填充默认值
//
// 该函数在 LoadFromBytes 中自动调用，也可在测试中单独使用。
func ApplyDefaults(cfg *Config) {
	if cfg == nil {
		return
	}

	applyServiceDefaults(cfg)
	applyLLMDefaults(cfg)
	applyOutputDefaults(cfg)
	applyDefaultsDefaults(cfg)
	applyMCPDefaults(cfg)

	// 对每个 repo 执行字段级深度合并
	for i := range cfg.Repos {
		repo := &cfg.Repos[i]
		if repo.Branch == "" {
			repo.Branch = "main"
		}
		if repo.KgGroup == "" {
			repo.KgGroup = repo.Name
		}

		// 字段级深度合并：discovery
		repo.SkillDiscovery = ptrOrNil(MergeDiscovery(
			cfg.Defaults.SkillDiscovery,
			repo.SkillDiscovery,
		))
		// 字段级深度合并：rewrite
		repo.Rewrite = ptrOrNil(MergeRewrite(
			cfg.Defaults.Rewrite,
			repo.Rewrite,
		))
	}
}

// applyServiceDefaults 填充 ServiceConfig 默认值。
func applyServiceDefaults(cfg *Config) {
	if cfg.Service.Mode == "" {
		cfg.Service.Mode = "daemon"
	}
	if cfg.Service.PollInterval == "" {
		cfg.Service.PollInterval = "5m"
	}
	if cfg.Service.WorkspacesDir == "" {
		cfg.Service.WorkspacesDir = ".workspaces"
	}
	if cfg.Service.CacheDir == "" {
		cfg.Service.CacheDir = ".cache"
	}
	if cfg.Service.BuildDir == "" {
		cfg.Service.BuildDir = ".build"
	}
}

// applyLLMDefaults 填充 LLMConfig 默认值。
func applyLLMDefaults(cfg *Config) {
	if cfg.LLM.MaxTokens == 0 {
		cfg.LLM.MaxTokens = 4096
	}
	if cfg.LLM.Temperature == 0 {
		cfg.LLM.Temperature = 0.0
	}
	if cfg.LLM.Timeout == "" {
		cfg.LLM.Timeout = "60s"
	}
	if cfg.LLM.MaxRetries == 0 {
		cfg.LLM.MaxRetries = 3
	}
}

// applyOutputDefaults 填充 OutputConfig 默认值。
func applyOutputDefaults(cfg *Config) {
	if cfg.Output.DBFilenameTemplate == "" {
		cfg.Output.DBFilenameTemplate = "knowledge-{group}.db"
	}
}

// applyDefaultsDefaults 填充 DefaultsConfig 的默认值（即默认值的默认值）。
// 包括 DiscoveryConfig 和 RewriteConfig 的字段级默认值。
func applyDefaultsDefaults(cfg *Config) {
	d := &cfg.Defaults

	// SkillDiscovery defaults
	if len(d.SkillDiscovery.Markers) == 0 {
		d.SkillDiscovery.Markers = []string{"SKILL.md"}
	}
	if len(d.SkillDiscovery.ReferenceDirs) == 0 {
		// 默认覆盖社区常见的两种拼写：references（复数）与 reference（单数）。
		// 与 discovery.DefaultReferenceDirs 保持一致，用户零配置即可扫到引用目录。
		// Default to both common spellings — "references" and "reference" —
		// matching discovery.DefaultReferenceDirs so zero config still works.
		d.SkillDiscovery.ReferenceDirs = []string{"references", "reference"}
	}
	if d.SkillDiscovery.ReferenceGlob == "" {
		d.SkillDiscovery.ReferenceGlob = "**/*.md"
	}

	// Rewrite defaults
	if d.Rewrite.Mode == "" {
		d.Rewrite.Mode = "pr"
	}
	if d.Rewrite.MinConfidence == 0 {
		d.Rewrite.MinConfidence = 0.7
	}
	if d.Rewrite.PR.BaseBranch == "" {
		d.Rewrite.PR.BaseBranch = "main"
	}
	if d.Rewrite.PR.HeadBranchPrefix == "" {
		d.Rewrite.PR.HeadBranchPrefix = "cairn-bot/"
	}
}

// applyMCPDefaults 填充 MCPConfig 默认值。
func applyMCPDefaults(cfg *Config) {
	for i := range cfg.MCP.KnowledgeBases {
		kb := &cfg.MCP.KnowledgeBases[i]
		if kb.CatalogRepo != "" {
			if kb.CatalogBranch == "" {
				kb.CatalogBranch = "main"
			}
			if kb.PollInterval == "" {
				kb.PollInterval = "5m"
			}
		}
	}
}

// MergeDiscovery 对 DiscoveryConfig 做字段级合并。
//
// repo 中非零值的字段覆盖 defaults 对应字段，零值字段继承 defaults 值。
// 这确保了 repo 可以只指定自己关心的字段（如仅覆写 markers），其余字段自动继承全局默认。
//
// override 为 nil 时直接返回 defaults 的副本。
func MergeDiscovery(defaults DiscoveryConfig, override *DiscoveryConfig) DiscoveryConfig {
	if override == nil {
		return defaults
	}
	result := defaults
	if len(override.Markers) > 0 {
		result.Markers = override.Markers
	}
	if len(override.ReferenceDirs) > 0 {
		result.ReferenceDirs = override.ReferenceDirs
	}
	if override.ReferenceGlob != "" {
		result.ReferenceGlob = override.ReferenceGlob
	}
	if len(override.IgnorePaths) > 0 {
		result.IgnorePaths = override.IgnorePaths
	}
	// bool 字段：只有当 override 与 defaults 不同时才覆盖
	// 注意：我们无法区分「用户显式设为 false」和「未设置（零值为 false）」。
	// 因此采用简化策略：override 中 RecurseIntoSkills=true 时覆盖，false 时保留 defaults。
	// 如果 needs 需要区分，应使用 *bool 指针。当前保持简单。
	if override.RecurseIntoSkills != defaults.RecurseIntoSkills {
		result.RecurseIntoSkills = override.RecurseIntoSkills
	}
	if len(override.SkillRootPaths) > 0 {
		result.SkillRootPaths = override.SkillRootPaths
	}
	return result
}

// MergeRewrite 对 RewriteConfig 做字段级合并。
//
// 合并规则与 MergeDiscovery 一致：非零值字段覆盖，零值字段继承。
// override 为 nil 时直接返回 defaults 的副本。
func MergeRewrite(defaults RewriteConfig, override *RewriteConfig) RewriteConfig {
	if override == nil {
		return defaults
	}
	result := defaults
	if override.Mode != "" {
		result.Mode = override.Mode
	}
	if override.MinConfidence != 0 {
		result.MinConfidence = override.MinConfidence
	}

	// PR 子配置：字段级合并
	result.PR = MergePRConfig(defaults.PR, override.PR)

	return result
}

// MergePRConfig 对 PRConfig 做字段级合并。
func MergePRConfig(defaults PRConfig, override PRConfig) PRConfig {
	result := defaults
	if override.BaseBranch != "" {
		result.BaseBranch = override.BaseBranch
	}
	if override.HeadBranchPrefix != "" {
		result.HeadBranchPrefix = override.HeadBranchPrefix
	}
	if len(override.Labels) > 0 {
		result.Labels = override.Labels
	}
	if len(override.Reviewers) > 0 {
		result.Reviewers = override.Reviewers
	}
	if override.AutoMergeOnGreen != defaults.AutoMergeOnGreen {
		result.AutoMergeOnGreen = override.AutoMergeOnGreen
	}
	if override.TitleTemplate != "" {
		result.TitleTemplate = override.TitleTemplate
	}
	if override.BodyTemplate != "" {
		result.BodyTemplate = override.BodyTemplate
	}
	if override.Forge != "" {
		result.Forge = override.Forge
	}
	if override.TokenEnv != "" {
		result.TokenEnv = override.TokenEnv
	}
	return result
}

// ptrOrNil 将值包装为指针，当值为零值时返回 nil。
// 用于避免分配零值结构体的指针，减少不必要的内存分配。
func ptrOrNil[T any](v T) *T {
	// 始终返回指针 — 调用方需要指针类型来赋值给 *RepoConfig
	return &v
}

// DefaultConfig 返回带所有默认值填充的配置实例。
// 等价于 ApplyDefaults(&Config{})，提供向后兼容的快捷方式。
//
// 该函数替代了 v1.x 中的 DefaultConfig()，返回 v2.0 完整 schema。
// 调用方可以直接使用 cfg.MCP.KnowledgeBases 获取 DB 信息。
func DefaultConfig() *Config {
	cfg := &Config{}
	ApplyDefaults(cfg)
	return cfg
}
