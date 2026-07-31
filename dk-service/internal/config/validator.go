package config

import (
	"fmt"
	"slices"
	"strings"
)

// ConfigError describes a single validation issue with its field path, message, and severity level.
// Validation errors are collected into a list so callers can report all issues at once.
type ConfigError struct {
	Field   string // 点路径表示，如 "repos[0].url"、"llm.provider"
	Message string // 人类可读的错误描述
	Level   string // "error" | "warning"
}

// Error 实现 error 接口，将所有错误格式化为一行一条。
func (e ConfigError) Error() string {
	return fmt.Sprintf("[%s] %s: %s", e.Level, e.Field, e.Message)
}

// ConfigErrors 是 ConfigError 的列表，实现 error 接口。
type ConfigErrors []ConfigError

// Error 将所有错误连接成多行字符串。
func (errs ConfigErrors) Error() string {
	var sb strings.Builder
	for i, e := range errs {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(e.Error())
	}
	return sb.String()
}

// HasErrors 检查列表中是否包含 Level="error" 的项。
func (errs ConfigErrors) HasErrors() bool {
	for _, e := range errs {
		if e.Level == "error" {
			return true
		}
	}
	return false
}

// validServiceModes 是 ServiceConfig.Mode 的合法值集合。
var validServiceModes = []string{"daemon", "once", "watch"}

// validRewriteModes 是 RewriteConfig.Mode 的合法值集合。
var validRewriteModes = []string{"pr", "push_direct", "local_only", "dry_run"}

// Validate 检查配置完整性，返回错误列表（每个错误精确到字段路径）。
//
// 检查项:
//   - service.mode 必须是 daemon|once|watch
//   - llm.provider 非空
//   - repos 每个条目 name 和 url 非空
//   - kg_group 跨 repo 无拼写不一致警告
//   - reference_dirs 非空时给出警告（提醒）但不是错误
//   - output.publishers 中 type=github_release 时 repo 非空
func (cfg *Config) Validate() ConfigErrors {
	var errs ConfigErrors

	// --- Service ---
	if !slices.Contains(validServiceModes, cfg.Service.Mode) {
		errs = append(errs, ConfigError{
			Field:   "service.mode",
			Message: fmt.Sprintf("must be one of %v, got %q", validServiceModes, cfg.Service.Mode),
			Level:   "error",
		})
	}
	if cfg.Service.PollInterval == "" {
		errs = append(errs, ConfigError{
			Field:   "service.poll_interval",
			Message: "should not be empty; default is \"5m\"",
			Level:   "warning",
		})
	}

	// --- LLM ---
	if cfg.LLM.Provider == "" {
		errs = append(errs, ConfigError{
			Field:   "llm.provider",
			Message: "must not be empty; valid values: anthropic, openai_compatible, anthropic_compatible",
			Level:   "error",
		})
	}

	// --- Repos ---
	kgGroups := make(map[string]int) // kg_group → repo index (for dedup/cross-check)
	for i := range cfg.Repos {
		repo := &cfg.Repos[i]
		prefix := fmt.Sprintf("repos[%d]", i)

		if repo.Name == "" {
			errs = append(errs, ConfigError{
				Field:   prefix + ".name",
				Message: "repo name is required",
				Level:   "error",
			})
		}
		if repo.URL == "" {
			errs = append(errs, ConfigError{
				Field:   prefix + ".url",
				Message: "repo URL is required",
				Level:   "error",
			})
		}

		// 收集 kg_group 用于跨 repo 比对
		if repo.KgGroup != "" {
			if prevIdx, exists := kgGroups[repo.KgGroup]; exists {
				errs = append(errs, ConfigError{
					Field:   prefix + ".kg_group",
					Message: fmt.Sprintf("kg_group %q also used by repos[%d]; consider using unique groups or verify this is intentional", repo.KgGroup, prevIdx),
					Level:   "warning",
				})
			}
			kgGroups[repo.KgGroup] = i
		}

		// Rewrite mode 校验（如果 repo 有自定义 rewrite 配置）
		if repo.Rewrite != nil {
			if repo.Rewrite.Mode != "" && !slices.Contains(validRewriteModes, repo.Rewrite.Mode) {
				errs = append(errs, ConfigError{
					Field:   prefix + ".rewrite.mode",
					Message: fmt.Sprintf("must be one of %v, got %q", validRewriteModes, repo.Rewrite.Mode),
					Level:   "error",
				})
			}
		}
	}

	// --- Defaults: Rewrite mode ---
	if cfg.Defaults.Rewrite.Mode != "" && !slices.Contains(validRewriteModes, cfg.Defaults.Rewrite.Mode) {
		errs = append(errs, ConfigError{
			Field:   "defaults.rewrite.mode",
			Message: fmt.Sprintf("must be one of %v, got %q", validRewriteModes, cfg.Defaults.Rewrite.Mode),
			Level:   "error",
		})
	}

	// --- Defaults: reference_dirs 非空警告 ---
	if len(cfg.Defaults.SkillDiscovery.ReferenceDirs) == 0 {
		errs = append(errs, ConfigError{
			Field:   "defaults.skill_discovery.reference_dirs",
			Message: "reference_dirs is empty; skill discovery will not search for reference files",
			Level:   "warning",
		})
	}

	// --- Output: publishers ---
	for i, pub := range cfg.Output.Publishers {
		prefix := fmt.Sprintf("output.publishers[%d]", i)
		if pub.Type == "github_release" && pub.Repo == "" {
			errs = append(errs, ConfigError{
				Field:   prefix + ".repo",
				Message: "repo is required for github_release publisher",
				Level:   "error",
			})
		}
		if pub.Type == "" {
			errs = append(errs, ConfigError{
				Field:   prefix + ".type",
				Message: "publisher type is required",
				Level:   "error",
			})
		}
	}

	return errs
}
