package discovery

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// 包级常量：扫描器各项默认值
// Package-level constants: scanner defaults
const (
	// DefaultMarker 是未指定 Markers 时使用的默认标记文件名
	// DefaultMarker is the default marker filename when Markers is not specified
	DefaultMarker = "SKILL.md"

	// DefaultReferenceDir 是单个默认引用目录名（保留以兼容既有引用）。
	// DefaultReferenceDir is a single default reference directory name (kept
	// for backward compatibility with existing references).
	DefaultReferenceDir = "references"

	// DefaultReferenceGlob 是未指定 ReferenceGlob 时使用的默认 glob 模式
	// DefaultReferenceGlob is the default glob pattern when ReferenceGlob
	// is not specified
	DefaultReferenceGlob = "**/*.md"
)

// DefaultReferenceDirs 是未指定 ReferenceDirs 时搜索的默认引用目录名列表。
// 覆盖社区常见的两种拼写：references（复数，Anthropic Skill 规范约定）
// 与 reference（单数）。这样用户零配置即可扫到绝大多数真实大仓的引用目录，
// 无需事先知道自己仓库用的是哪种拼写。
//
// DefaultReferenceDirs is the list of default reference directory names
// searched when ReferenceDirs is unset. It covers the two common community
// spellings — "references" (plural, per the Anthropic Skill convention) and
// "reference" (singular) — so users need zero config to discover reference
// files in most real repositories regardless of which spelling they use.
var DefaultReferenceDirs = []string{"references", "reference"}

// FSScanner 是 Scanner 接口的具体实现，使用标准库 os 和 filepath 遍历文件树。
// 所有文件系统访问均通过 os 标准库完成，不依赖任何外部框架。
//
// FSScanner is the concrete implementation of the Scanner interface, using
// the standard library os and filepath packages to traverse the file tree.
// All filesystem access is done through the os standard library with no
// external framework dependencies.
type FSScanner struct{}

// NewFSScanner 创建一个新的 FSScanner 实例。
//
// NewFSScanner creates a new FSScanner instance.
func NewFSScanner() *FSScanner {
	return &FSScanner{}
}

// Scan 从指定仓库路径开始扫描，按照 rules 中定义的规则识别 Skill 目录。
// repoPath 应为仓库克隆后的本地工作区路径。
//
// Scan scans from the given repository path, identifying Skill directories
// according to the rules defined in rules. repoPath should be the local
// workspace path of a cloned repository.
func (s *FSScanner) Scan(ctx context.Context, repoPath string, rules DiscoveryRules) ([]Skill, error) {
	// 规范化配置参数 / Normalize config parameters
	rules = normalizeRules(rules)

	var skills []Skill

	// 确定 scan 起点：指定 SkillRootPaths 时限定扫描范围，否则扫描整个 repoPath
	// Determine scan starting points: restrict to SkillRootPaths if specified,
	// otherwise scan the entire repoPath
	scanRoots := rules.SkillRootPaths
	if len(scanRoots) == 0 {
		scanRoots = []string{"."}
	}

	for _, root := range scanRoots {
		absRoot := filepath.Join(repoPath, root)
		found, err := s.scanDir(ctx, repoPath, absRoot, rules)
		if err != nil {
			return nil, err
		}
		skills = append(skills, found...)
	}

	// 按路径排序以保证确定性输出 / Sort by path for deterministic output
	sort.Slice(skills, func(i, j int) bool {
		return skills[i].Path < skills[j].Path
	})

	return skills, nil
}

// scanDir 递归扫描一个目录，返回发现的 Skill 列表。
// foundMarker 参数表示当前层级是否已发现 Skill（用于控制递归行为）。
//
// scanDir recursively scans a directory, returning discovered Skills.
// The foundMarker parameter indicates whether a Skill has already been
// found at the current level (used to control recursion behavior).
func (s *FSScanner) scanDir(ctx context.Context, repoPath, absDir string, rules DiscoveryRules) ([]Skill, error) {
	// 检查 context 是否已取消 / Check if context is cancelled
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	var skills []Skill

	// 检查当前目录是否匹配忽略路径 / Check if current dir matches ignore paths
	if s.shouldIgnore(repoPath, absDir, rules.IgnorePaths) {
		return nil, nil
	}

	// 检查当前目录是否包含标记文件 / Check if current dir contains a marker file
	marker, found := s.findMarker(absDir, rules.Markers)
	if found {
		skill := s.buildSkill(repoPath, absDir, marker, rules)
		skills = append(skills, skill)

		// 如果配置要求不递归，则停止深入该 Skill 的子目录
		// If RecurseIntoSkills is false, stop descending into subdirectories
		if !rules.RecurseIntoSkills {
			return skills, nil
		}
	}

	// 读取目录条目并递归扫描子目录 / Read directory entries and recurse into subdirectories
	entries, err := os.ReadDir(absDir)
	if err != nil {
		// 跳过无法读取的目录（权限等）/ Skip unreadable directories (permissions, etc.)
		return skills, nil
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		// 跳过隐藏目录和常见非内容目录 / Skip hidden dirs and common non-content dirs
		if s.isSkipDir(entry.Name()) {
			continue
		}

		childAbs := filepath.Join(absDir, entry.Name())
		childSkills, err := s.scanDir(ctx, repoPath, childAbs, rules)
		if err != nil {
			return skills, err
		}
		skills = append(skills, childSkills...)
	}

	return skills, nil
}

// buildSkill 根据发现的标记文件构建 Skill 结构体。
// 解析 SKILL.md 的前置元数据（YAML front matter），提取 name 和 domain 提示。
// 同时收集 ReferenceDirs 中匹配 ReferenceGlob 的引用文件。
//
// buildSkill constructs a Skill struct from the discovered marker file.
// Parses the SKILL.md YAML front matter to extract name and domain hint.
// Also collects reference files matching ReferenceGlob within ReferenceDirs.
func (s *FSScanner) buildSkill(repoPath, absDir, markerFile string, rules DiscoveryRules) Skill {
	// 计算相对于仓库根目录的路径 / Compute relative path from repo root
	relDir, _ := filepath.Rel(repoPath, absDir)
	markerRel, _ := filepath.Rel(repoPath, filepath.Join(absDir, markerFile))

	skill := Skill{
		Name:       filepath.Base(relDir), // 默认使用目录名 / default to directory name
		Path:       relDir,
		MarkerFile: markerRel,
	}

	// 尝试解析 SKILL.md 获取 name 和 domain 提示 / Try parsing SKILL.md for name and domain hint
	markerPath := filepath.Join(absDir, markerFile)
	if data, err := os.ReadFile(markerPath); err == nil {
		name, domainHint := parseFrontMatter(data)
		if name != "" {
			skill.Name = name
		}
		skill.DomainHint = domainHint
	}

	// 收集 reference 文件 / Collect reference files
	skill.ReferenceFiles = s.collectReferenceFiles(repoPath, absDir, rules)

	return skill
}

// findMarker 检查目录中是否存在任意标记文件。
// 返回找到的第一个标记文件名和 true，或空字符串和 false。
//
// findMarker checks if any marker file exists in the directory.
// Returns the first marker filename found and true, or empty string and false.
func (s *FSScanner) findMarker(dir string, markers []string) (string, bool) {
	for _, marker := range markers {
		path := filepath.Join(dir, marker)
		if fileExists(path) {
			return marker, true
		}
	}
	return "", false
}

// collectReferenceFiles 在 Skill 目录的 ReferenceDirs 中收集匹配 ReferenceGlob 的文件。
// 返回相对于 repoPath 的路径列表。
// ReferenceGlob 支持两种形式：简单 glob（如 "*.md"）和递归 glob（如 "**/*.md"）。
//
// collectReferenceFiles collects files matching ReferenceGlob within the
// Skill directory's ReferenceDirs. Returns a list of paths relative to repoPath.
// ReferenceGlob supports two forms: simple glob (e.g., "*.md") and recursive
// glob (e.g., "**/*.md").
func (s *FSScanner) collectReferenceFiles(repoPath, absDir string, rules DiscoveryRules) []string {
	var refs []string

	for _, refDirName := range rules.ReferenceDirs {
		refDir := filepath.Join(absDir, refDirName)
		if !dirExists(refDir) {
			continue
		}

		// 遍历 ref 目录收集匹配 glob 的文件 / Walk the ref dir to collect glob-matching files
		err := filepath.WalkDir(refDir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				log.Printf("discovery: walk error at %s: %v", path, err)
				return nil // 继续遍历 / continue walking
			}
			if d.IsDir() {
				return nil
			}

			if matchGlob(rules.ReferenceGlob, refDir, path) {
				// 计算相对于 repoPath 的路径 / Compute path relative to repoPath
				relPath, _ := filepath.Rel(repoPath, path)
				refs = append(refs, relPath)
			}
			return nil
			})
			if err != nil {
				log.Printf("discovery: walk %s failed: %v", refDir, err)
			}
		}

	sort.Strings(refs)
	return refs
}

// matchGlob 检查文件路径是否匹配给定的 glob 模式。
// 兼容 **/*.ext（递归匹配）和 *.ext（仅匹配文件名）两种风格。
// baseDir 为搜索根目录，filePath 为文件的绝对路径。
//
// matchGlob checks whether a file path matches the given glob pattern.
// Compatible with both **/*.ext (recursive match) and *.ext (filename-only match).
// baseDir is the search root directory, filePath is the absolute path of the file.
func matchGlob(glob, baseDir, filePath string) bool {
	// 计算相对于 baseDir 的路径 / Compute relative path from baseDir
	relPath, err := filepath.Rel(baseDir, filePath)
	if err != nil {
		return false
	}

	// 1. 先尝试完整 glob 与相对路径匹配 / Try matching glob against the relative path
	if matched, _ := filepath.Match(glob, relPath); matched {
		return true
	}

	// 2. 如果 glob 包含 **/ 前缀，提取文件名模式并与文件名匹配
	// If glob has a **/ prefix, extract the filename pattern and match against filename
	stripped := glob
	for strings.HasPrefix(stripped, "**/") {
		stripped = stripped[3:]
	}
	if stripped != glob {
		if matched, _ := filepath.Match(stripped, filepath.Base(relPath)); matched {
			return true
		}
	}

	// 3. 尝试直接匹配文件名（兼容仅指定扩展名的情况）
	// Try matching against just the filename (for extension-only patterns)
	if matched, _ := filepath.Match(glob, filepath.Base(relPath)); matched {
		return true
	}

	return false
}

// shouldIgnore 判断给定路径是否匹配任意忽略模式。
//
// shouldIgnore checks if the given path matches any ignore pattern.
func (s *FSScanner) shouldIgnore(repoPath, absPath string, patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}

	relPath, err := filepath.Rel(repoPath, absPath)
	if err != nil {
		return false
	}

	for _, pattern := range patterns {
		// 尝试精确匹配 / Try exact match
		if matched, _ := filepath.Match(pattern, relPath); matched {
			return true
		}
		// 尝试匹配路径中任意部分（glob 语义）/ Try matching any path component (glob semantics)
		if matched, _ := filepath.Match(pattern, filepath.Base(relPath)); matched {
			return true
		}
	}
	return false
}

// isSkipDir 判断目录是否应跳过（隐藏目录、版本控制目录等）。
//
// isSkipDir determines if a directory should be skipped (hidden dirs, VCS dirs, etc.).
func (s *FSScanner) isSkipDir(name string) bool {
	// 跳过隐藏目录（以 . 开头）和常见版本控制/构建目录
	// Skip hidden dirs (starting with .) and common VCS/build dirs
	if strings.HasPrefix(name, ".") {
		return true
	}
	return false
}

// normalizeRules 将未设置的规则字段填充为默认值。
//
// normalizeRules fills unset rule fields with default values.
func normalizeRules(rules DiscoveryRules) DiscoveryRules {
	if len(rules.Markers) == 0 {
		rules.Markers = []string{DefaultMarker}
	}
	if len(rules.ReferenceDirs) == 0 {
		rules.ReferenceDirs = DefaultReferenceDirs
	}
	if rules.ReferenceGlob == "" {
		rules.ReferenceGlob = DefaultReferenceGlob
	}
	return rules
}

// parseFrontMatter 从 SKILL.md 内容中解析 YAML front matter。
// 格式为：以 "---" 开始和结束的 YAML 块。
// 返回解析出的 name 字段和第一个不包含冒号的行（作为 domain 提示）。
//
// parseFrontMatter parses YAML front matter from SKILL.md content.
// Format: a YAML block delimited by "---" at start and end.
// Returns the parsed name field and the first line without a colon (as domain hint).
func parseFrontMatter(data []byte) (name, domainHint string) {
	// 查找 front matter 边界 / Find front matter boundaries
	if !bytes.HasPrefix(data, []byte("---\n")) {
		return "", ""
	}

	// 跳过开头的 "---\n" / Skip leading "---\n"
	content := data[4:]

	// 查找结束标记 / Find closing marker
	endIdx := bytes.Index(content, []byte("\n---\n"))
	if endIdx < 0 {
		endIdx = bytes.Index(content, []byte("\n---"))
	}
	if endIdx < 0 {
		return "", ""
	}

	fmData := content[:endIdx]

	// 尝试使用 YAML 库解析 / Try parsing with YAML library
	var fm map[string]interface{}
	if err := yaml.Unmarshal(fmData, &fm); err == nil {
		if n, ok := fm["name"]; ok {
			if ns, ok2 := n.(string); ok2 {
				name = strings.TrimSpace(ns)
			}
		}
		if d, ok := fm["domain"]; ok {
			if ds, ok2 := d.(string); ok2 {
				domainHint = strings.TrimSpace(ds)
			}
		}
	}

	// 如果 domain 仍为空，从内容首行提取提示 / If domain still empty, extract hint from first content line
	if domainHint == "" {
		// 跳过 front matter 的结束标记和后续空行 / Skip closing delimiter and blank lines after front matter
		// content[endIdx:] 以 "\n---\n" 或 "\n---" 开头
		// content[endIdx:] starts with "\n---\n" or "\n---"
		rest := content[endIdx:]
		// 跳过 "\n---" 部分 / Skip the "\n---" part
		if idx := bytes.Index(rest[1:], []byte("\n")); idx >= 0 {
			// rest[1:] 跳过开头的 \n，idx 指向 --- 后的 \n
			// rest[1:] skips the leading \n, idx points to the \n after ---
			rest = rest[1+idx+1:]
		} else if len(rest) > 1 {
			// "---" 后无换行（文件结尾），跳过全部 / no newline after "---" (end of file), skip entirely
			rest = nil
		}
		rest = bytes.TrimSpace(rest)
		if len(rest) > 0 {
			// 取首个非空行作为 domain 提示 / Take first non-empty line as domain hint
			firstLineEnd := bytes.IndexAny(rest, "\r\n")
			if firstLineEnd < 0 {
				firstLineEnd = len(rest)
			}
			firstLine := string(rest[:firstLineEnd])
			// 去除 leading 标记符号（# 标题等）/ Strip leading markers (# for headings, etc.)
			firstLine = strings.TrimLeft(firstLine, "# \t")
			// 如果首行不包含冒号且非空，作为 domain 提示
			// If first line doesn't contain colon and is non-empty, use as domain hint
			if !strings.Contains(firstLine, ":") && len(firstLine) > 0 {
				domainHint = strings.TrimSpace(firstLine)
			}
		}
	}

	return name, domainHint
}

// fileExists 检查指定路径的文件是否存在。
//
// fileExists checks whether a file exists at the given path.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// dirExists 检查指定路径的目录是否存在。
//
// dirExists checks whether a directory exists at the given path.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}
