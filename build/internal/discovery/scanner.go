package discovery

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

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
type FSScanner struct {
	// Optional package-local reader supports deterministic I/O failure tests.
	readFile func(string) ([]byte, error)
}

// NewFSScanner 创建一个新的 FSScanner 实例。
//
// NewFSScanner creates a new FSScanner instance.
func NewFSScanner() *FSScanner {
	return &FSScanner{}
}

// Scan 保持既有技能列表接口，并显式返回读取失败；完整处置请使用 ScanReport。
//
// Scan preserves the legacy skills view, but never reports a failed read as an
// empty successful discovery. Call ScanReport to inspect all dispositions.
func (s *FSScanner) Scan(ctx context.Context, repoPath string, rules DiscoveryRules) ([]Skill, error) {
	report, err := s.ScanReport(ctx, repoPath, rules)
	return report.Skills, err
}

// ScanReport 按路径唯一收集完整标记原文和引用，保留成功来源并显式报告失败或取消边界。
//
// ScanReport discovers full marker sources and reference sources exactly once.
// Explicit roots, reference directories and their overlaps share path identity.
// Read failures do not prevent independent roots/siblings from being inspected;
// cancellation stops traversal and reports the unfinished boundary.
func (s *FSScanner) ScanReport(ctx context.Context, repoPath string, rules DiscoveryRules) (Report, error) {
	rules = normalizeRules(rules)
	if err := validateRules(rules); err != nil {
		return Report{}, err
	}
	root, err := filepath.Abs(repoPath)
	if err != nil {
		return Report{}, err
	}
	state := scanState{scanner: s, ctx: ctx, root: root, rules: rules,
		sources: make(map[string]Source), visited: make(map[string]bool)}
	roots := rules.SkillRootPaths
	if len(roots) == 0 {
		roots = []string{"."}
	}
	// Parent roots first: configuration order must not change ownership.
	roots = append([]string(nil), roots...)
	sort.Strings(roots)
	for _, rel := range roots {
		state.scanDir(filepath.Join(root, rel))
		if ctx.Err() != nil {
			break
		}
	}
	if ctx.Err() != nil {
		state.cancelled(root)
	}
	sort.Slice(state.skills, func(i, j int) bool { return state.skills[i].Path < state.skills[j].Path })
	// Compatibility callers also receive each reference at most once. A nested
	// marker has body ownership even if an enclosing reference glob matched it.
	owned := make(map[string]bool)
	for i := range state.skills {
		refs := state.skills[i].ReferenceFiles[:0]
		for _, path := range state.skills[i].ReferenceFiles {
			if state.sources[path].Kind == SourceReference && !owned[path] {
				refs = append(refs, path)
				owned[path] = true
			}
		}
		state.skills[i].ReferenceFiles = refs
	}
	report := Report{Skills: state.skills}
	for _, source := range state.sources {
		report.Sources = append(report.Sources, source)
	}
	sort.Slice(report.Sources, func(i, j int) bool { return report.Sources[i].Path < report.Sources[j].Path })
	return report, errors.Join(state.errors...)
}

type scanState struct {
	scanner *FSScanner
	ctx     context.Context
	root    string
	rules   DiscoveryRules
	skills  []Skill
	sources map[string]Source
	visited map[string]bool
	errors  []error
}

func (s *scanState) relative(path string) string {
	rel, _ := filepath.Rel(s.root, path)
	return filepath.ToSlash(rel)
}

func (s *scanState) record(path string, kind SourceKind, status Disposition, reason string) {
	rel := s.relative(path)
	// A general directory traversal must not overwrite an already classified
	// reference. A supported marker takes priority when discovered inside refs.
	if old, ok := s.sources[rel]; ok && (old.Kind != SourcePath || (status == Excluded && old.Disposition != Excluded)) {
		return
	}
	s.sources[rel] = Source{Path: rel, Kind: kind, Disposition: status, Reason: reason}
}

func (s *scanState) failure(path string, kind SourceKind, err error) {
	s.record(path, kind, Unreadable, err.Error())
	s.errors = append(s.errors, fmt.Errorf("discovery %s: %w", s.relative(path), err))
}

func (s *scanState) cancelled(path string) bool {
	if err := s.ctx.Err(); err != nil {
		s.record(path, SourcePath, Cancelled, err.Error())
		s.errors = append(s.errors, err)
		return true
	}
	return false
}

func (s *scanState) ignored(path string) bool {
	if s.scanner.shouldIgnore(s.root, path, s.rules.IgnorePaths) {
		s.record(path, SourcePath, Excluded, "configured IgnorePaths")
		return true
	}
	return false
}

func (s *scanState) readSource(path string, kind SourceKind) Source {
	rel := s.relative(path)
	if old, ok := s.sources[rel]; ok && old.Kind != SourcePath {
		if kind == SourceSkillBody {
			old.Kind = kind
			s.sources[rel] = old
		}
		return old
	}
	source := Source{Path: rel, Kind: kind}
	info, err := os.Lstat(path)
	if err != nil {
		s.failure(path, kind, err)
		return s.sources[rel]
	}
	if !info.Mode().IsRegular() {
		source.Disposition = Unsupported
		source.Reason = "symlinks and non-regular files are unsupported"
	} else {
		reader := s.scanner.readFile
		if reader == nil {
			reader = os.ReadFile
		}
		data, err := reader(path)
		if err != nil {
			s.failure(path, kind, err)
			return s.sources[rel]
		}
		if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
			source.Disposition = Unsupported
			source.Reason = "source is not UTF-8 text without NUL bytes"
		} else {
			source.Disposition = Supported
			source.Content = data
		}
	}
	s.sources[rel] = source
	return source
}

// Configured nested paths must not bypass the no-symlink traversal rule.
func (s *scanState) symlinkAncestor(path string) bool {
	rel := s.relative(path)
	current := s.root
	parts := strings.Split(filepath.FromSlash(rel), string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			s.failure(current, SourcePath, err)
			return true
		}
		if info.Mode()&os.ModeSymlink != 0 {
			s.record(current, SourcePath, Unsupported, "symlink directory is not followed")
			return true
		}
	}
	return false
}

func (s *scanState) scanDir(path string) {
	if s.cancelled(path) || s.ignored(path) || s.symlinkAncestor(path) {
		return
	}
	if s.visited[path] {
		return
	}
	s.visited[path] = true
	info, err := os.Lstat(path)
	if err != nil {
		s.failure(path, SourcePath, err)
		return
	}
	if !info.IsDir() {
		s.record(path, SourcePath, Unsupported, "scan root must be a real directory; symlinks are not followed")
		return
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		s.failure(path, SourcePath, err)
		return
	}
	byName := make(map[string]os.DirEntry, len(entries))
	for _, entry := range entries {
		byName[entry.Name()] = entry
	}
	var skill *Skill
	for _, marker := range s.rules.Markers {
		if _, exists := byName[marker]; !exists {
			continue
		}
		markerPath := filepath.Join(path, marker)
		if s.ignored(markerPath) {
			continue
		}
		if s.cancelled(markerPath) {
			return
		}
		source := s.readSource(markerPath, SourceSkillBody)
		if source.Disposition == Unsupported {
			continue
		}
		if skill == nil {
			skill = &Skill{Name: filepath.Base(path), Path: s.relative(path), MarkerFile: source.Path}
			name, domain := parseFrontMatter(source.Content)
			if name != "" {
				skill.Name = name
			}
			skill.DomainHint = domain
		}
	}
	if skill != nil {
		skill.ReferenceFiles = s.collectReferences(path)
		s.skills = append(s.skills, *skill)
	}
	for _, entry := range entries {
		child := filepath.Join(path, entry.Name())
		if s.cancelled(child) {
			return
		}
		if s.ignored(child) {
			continue
		}
		if entry.IsDir() {
			if s.scanner.isSkipDir(entry.Name()) {
				s.record(child, SourcePath, Excluded, "hidden directory excluded from skill discovery")
			} else if skill != nil && !s.rules.RecurseIntoSkills {
				s.record(child, SourcePath, Excluded, "nested skill discovery disabled; configured references inspected separately")
			} else {
				s.scanDir(child)
			}
		} else if entry.Type()&os.ModeSymlink != 0 {
			s.record(child, SourcePath, Unsupported, "symlink is not followed")
		} else {
			s.record(child, SourcePath, Excluded, "outside configured marker and reference scope")
		}
	}
}

func (s *scanState) collectReferences(skillDir string) []string {
	refs := make(map[string]bool)
	for _, name := range s.rules.ReferenceDirs {
		base := filepath.Join(skillDir, name)
		if s.cancelled(base) {
			break
		}
		info, err := os.Lstat(base)
		if errors.Is(err, os.ErrNotExist) {
			continue
		} // optional reference directory
		if err != nil {
			s.failure(base, SourcePath, err)
			continue
		}
		if s.symlinkAncestor(base) {
			continue
		}
		if !info.IsDir() {
			s.record(base, SourcePath, Unsupported, "reference root must be a real directory; symlinks are not followed")
			continue
		}
		err = filepath.WalkDir(base, func(path string, entry os.DirEntry, walkErr error) error {
			if s.cancelled(path) {
				return s.ctx.Err()
			}
			if walkErr != nil {
				s.failure(path, SourceReference, walkErr)
				return nil
			}
			if s.ignored(path) {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.IsDir() {
				return nil
			}
			if !matchGlob(s.rules.ReferenceGlob, base, path) {
				s.record(path, SourcePath, Unsupported, "does not match configured ReferenceGlob")
				return nil
			}
			source := s.readSource(path, SourceReference)
			// Keep failed candidates in the legacy list too; the error prevents them
			// from masquerading as a smaller successful discovery.
			if source.Disposition != Unsupported {
				refs[source.Path] = true
			}
			return nil
		})
		if err != nil && s.ctx.Err() == nil {
			s.failure(base, SourcePath, err)
		}
	}
	result := make([]string, 0, len(refs))
	for path := range refs {
		result = append(result, path)
	}
	sort.Strings(result)
	return result
}

func validateRules(rules DiscoveryRules) error {
	for _, marker := range rules.Markers {
		if marker == "" || marker == "." || marker == ".." || filepath.Base(marker) != marker {
			return fmt.Errorf("discovery: marker must be a filename: %q", marker)
		}
	}
	for _, paths := range [][]string{rules.SkillRootPaths, rules.ReferenceDirs} {
		for _, path := range paths {
			clean := filepath.Clean(path)
			if filepath.IsAbs(path) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
				return fmt.Errorf("discovery: path escapes its configured root: %q", path)
			}
		}
	}
	for _, pattern := range append(append([]string(nil), rules.IgnorePaths...), rules.ReferenceGlob) {
		if _, err := filepath.Match(pattern, ""); err != nil {
			return fmt.Errorf("discovery: invalid pattern %q: %w", pattern, err)
		}
	}
	return nil
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
