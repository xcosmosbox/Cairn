// Package discovery 提供技能（Skill）目录扫描与识别功能。
// 在工作区文件树中按规则发现 SKILL.md 标记文件，收集关联的 reference 文件，
// 并提取 domain 提示信息。该包不依赖 internal/config，定义自己的配置类型。
//
// Package discovery provides skill directory scanning and identification.
// It discovers SKILL.md marker files in workspace file trees according to
// configurable rules, collects associated reference files, and extracts
// domain hints. This package has no dependency on internal/config — it
// defines its own configuration types.
package discovery

import "context"

// Scanner 在工作区文件树中扫描和识别 Skill 目录。
// 根据 DiscoveryRules 配置遍历目录树，发现标记文件（如 SKILL.md），
// 并收集关联的 reference 文件。
//
// Scanner scans and identifies Skill directories in a workspace file tree.
// It traverses the directory tree according to DiscoveryRules, discovers
// marker files (e.g., SKILL.md), and collects associated reference files.
type Scanner interface {
	// Scan 从指定仓库路径开始扫描，按照 rules 中定义的规则识别 Skill 目录。
	// 返回按路径排序的技能列表。repoPath 应为仓库克隆后的本地工作区路径。
	//
	// Scan scans from the given repository path, identifying Skill directories
	// according to the rules defined in rules. Returns a list of skills sorted
	// by path. repoPath should be the local workspace path of a cloned repository.
	Scan(ctx context.Context, repoPath string, rules DiscoveryRules) ([]Skill, error)
}

// DiscoveryRules 定义 Skill 发现规则，控制扫描行为和匹配条件。
// 所有字段均可选：未设置的字段使用包内默认值。
//
// DiscoveryRules defines the rules for Skill discovery, controlling scanning
// behavior and matching criteria. All fields are optional: unset fields
// use the package-level defaults.
type DiscoveryRules struct {
	// Markers 是 Skill 标记文件名列表，如 ["SKILL.md"]。
	// 扫描器在目录下查找这些文件来判定该目录是否为 Skill 目录。
	// Markers is the list of skill marker filenames, e.g., ["SKILL.md"].
	// The scanner looks for these files in a directory to determine
	// whether it is a Skill directory.
	Markers []string

	// ReferenceDirs 是 reference 文件存放的子目录名列表，如 ["ref"]。
	// 在 Skill 目录下依次查找这些子目录。
	// ReferenceDirs is the list of subdirectory names that contain reference
	// files, e.g., ["ref"]. Searched sequentially under the Skill directory.
	ReferenceDirs []string

	// ReferenceGlob 是匹配 reference 文件的 glob 模式，如 "**/*.md"。
	// 仅在 ReferenceDirs 下应用此模式。
	// ReferenceGlob is the glob pattern for matching reference files,
	// e.g., "**/*.md". Applied only within ReferenceDirs.
	ReferenceGlob string

	// IgnorePaths 是忽略路径模式列表（glob 语法）。
	// 匹配这些模式的路径不会被扫描。
	// IgnorePaths is a list of path patterns to ignore (glob syntax).
	// Paths matching these patterns are excluded from scanning.
	IgnorePaths []string

	// RecurseIntoSkills 控制是否递归进入已发现 Skill 的子目录继续扫描。
	// 设为 true 时允许嵌套 Skill 发现（Skill 目录内部可能包含子 Skill）。
	// RecurseIntoSkills controls whether to recurse into subdirectories of
	// an already-discovered Skill. Set to true to allow nested Skill discovery
	// (a Skill directory may contain child Skills).
	RecurseIntoSkills bool

	// SkillRootPaths 限定扫描范围的根路径列表（相对于 repoPath）。
	// 如果设置了此项，则只在这些路径下扫描；否则扫描整个 repoPath。
	// SkillRootPaths restricts the scan scope to specific root paths
	// (relative to repoPath). If set, only these paths are scanned;
	// otherwise the entire repoPath is scanned.
	SkillRootPaths []string
}

// Skill 表示一个被发现的 Skill 目录及其元数据。
//
// Skill represents a discovered Skill directory along with its metadata.
type Skill struct {
	// Name 是技能名称，取自目录名或 SKILL.md 中的 name 字段。
	// Name is the skill name, derived from the directory name or the
	// name field in SKILL.md.
	Name string

	// Path 是技能目录相对于仓库根目录的路径。
	// Path is the skill directory path relative to the repository root.
	Path string

	// MarkerFile 是发现的标记文件的相对路径。
	// MarkerFile is the relative path of the discovered marker file.
	MarkerFile string

	// ReferenceFiles 是关联的 reference 文件路径列表（相对路径）。
	// ReferenceFiles is the list of associated reference file paths (relative).
	ReferenceFiles []string

	// DomainHint 是从 SKILL.md 内容中提取的 domain 提示信息。
	// DomainHint is the domain hint extracted from the SKILL.md content.
	DomainHint string
}
