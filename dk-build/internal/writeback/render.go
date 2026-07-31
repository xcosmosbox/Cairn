// Package writeback 的本文件实现 md 块渲染：
//   - 非 shared node → 完整可编辑块（uuid 成对注释块，设计定稿 §4.1 格式）；
//   - shared node 在来源文档 → 只读镜像块（设计定稿 §4.2 格式）；
//   - shared node 在 _shared primary → 与非 shared 同结构的完整可编辑块。
//
// 铁律：
//   - R4：渲染代码绝不输出 relation 信息（relation 是纯派生物，只在 KG）。
//   - R7：name/归属/uuid/tag/shared 只读展示，仅 summary/description 标"（可编辑）"；
//     镜像块整体只读。
//
// This file implements markdown block rendering: full editable blocks for
// non-shared nodes (spec §4.1), read-only mirror blocks for shared nodes in
// source docs (spec §4.2), and full blocks for shared primaries. R4: no relation
// info is ever rendered; R7: only summary/description are marked editable.
package writeback

import (
	"fmt"
	"sort"
	"strings"
)

// docHeaderComment 是每篇回写文档顶部的说明注释。
// docHeaderComment is the header comment placed atop every written-back doc.
const docHeaderComment = `<!-- 本文档由领域知识层结构化生成。仅「摘要/详述」可编辑；其余为系统维护，请勿修改。 -->`

// 可编辑区占位符：KG 中 summary/description 为空时，md 块里展示占位文本而非空行。
// 增量 diff 把块内占位文本映射回空串再比对 hash（NormalizeEditable），
// 因此占位符字符串必须在 render 与 diff 之间保持单一事实源（本常量）。
//
// Placeholders shown in editable areas when the KG summary/description is empty.
// The incremental diff maps placeholder text back to "" before hashing
// (NormalizeEditable), so these constants are the single source of truth shared
// by render and diff.
const (
	emptySummaryPlaceholder     = "（暂无摘要）"
	emptyDescriptionPlaceholder = "（暂无详述）"
)

// FullSummaryMarker and FullDescriptionMarker are the stable delimiters of the
// two editable regions in a full block.  Keeping these values in writeback
// gives diffing and rendering one canonical source of truth.
const (
	FullSummaryMarker     = "**摘要**（可编辑）"
	FullDescriptionMarker = "**详述**（可编辑）"
)

// CanonicalFullOpenComment returns the exact opening anchor emitted for a full
// block.  The raw line is intentionally exposed to the incremental validator:
// indentation, spacing, and extra attributes are read-only structure.
func CanonicalFullOpenComment(uuid, tag string, shared bool) string {
	return fmt.Sprintf("<!-- kg:uuid=%s tag=%s shared=%s readonly-meta -->",
		uuid, strings.ToLower(tag), boolText(shared))
}

// CanonicalFullCloseComment returns the exact closing anchor emitted for a full
// block.
func CanonicalFullCloseComment(uuid string) string {
	return fmt.Sprintf("<!-- /kg:uuid=%s -->", uuid)
}

// CanonicalFullReadonlyPrefix returns the immutable prefix inside a full block,
// through (and including) the summary marker.  Summary/description bodies are
// deliberately excluded because they are the only editable regions.
func CanonicalFullReadonlyPrefix(name, domain, subdomain string) []string {
	return []string{
		fmt.Sprintf("## %s", name),
		fmt.Sprintf("- **归属**：%s / %s", domain, subdomain),
		"",
		FullSummaryMarker,
	}
}

func boolText(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

// renderFullBlock 渲染一个非 shared node 的完整可编辑块（设计定稿 §4.1）。
// 格式：
//
//	<!-- kg:uuid=<uuid> tag=<entity|concept> shared=<true|false> readonly-meta -->
//	## <name>
//	- **归属**：<domain> / <subdomain>
//
//	**摘要**（可编辑）
//	<summary>
//
//	**详述**（可编辑）
//	<description>
//	<!-- /kg:uuid=<uuid> -->
//
// renderFullBlock renders a full editable block for a non-shared node (spec §4.1).
func renderFullBlock(v nodeView) string {
	var sb strings.Builder
	sb.WriteString(CanonicalFullOpenComment(v.UUID, v.Tag, v.Shared))
	sb.WriteByte('\n')
	fmt.Fprintf(&sb, "## %s\n", v.Name)
	fmt.Fprintf(&sb, "- **归属**：%s / %s\n\n", v.Domain, v.Subdomain)
	sb.WriteString(FullSummaryMarker)
	sb.WriteByte('\n')
	sb.WriteString(nonEmpty(v.Summary, emptySummaryPlaceholder))
	sb.WriteString("\n\n")
	sb.WriteString(FullDescriptionMarker)
	sb.WriteByte('\n')
	sb.WriteString(nonEmpty(v.Description, emptyDescriptionPlaceholder))
	sb.WriteString("\n")
	sb.WriteString(CanonicalFullCloseComment(v.UUID))
	sb.WriteByte('\n')
	return sb.String()
}

// renderMirrorBlock 渲染一个 shared node 在来源文档中的只读镜像块（设计定稿 §4.2）。
// 格式：
//
//	<!-- kg:uuid=<uuid> shared=true mirror=true source=_shared/<domain-slug>/<uuid>.md -->
//	> 🔒 **[共享镜像 · 只读]** 本内容由 `_shared/<domain-slug>/<uuid>.md` 维护，请勿在此编辑。
//	> **<name>** — <summary>
//	<!-- /kg:uuid=<uuid> -->
//
// renderMirrorBlock renders a read-only mirror block for a shared node in a source doc (spec §4.2).
func renderMirrorBlock(v nodeView) string {
	primaryPath := primaryFilePath(v)
	var sb strings.Builder
	fmt.Fprintf(&sb, "<!-- kg:uuid=%s shared=true mirror=true source=%s -->\n",
		v.UUID, primaryPath)
	fmt.Fprintf(&sb, "> 🔒 **[共享镜像 · 只读]** 本内容由 `%s` 维护，请勿在此编辑。\n", primaryPath)
	for _, line := range MirrorSummaryLines(v.Name, v.Summary) {
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	fmt.Fprintf(&sb, "<!-- /kg:uuid=%s -->\n", v.UUID)
	return sb.String()
}

// MirrorSummaryLines 返回镜像摘要的 canonical 引用行。summary 可多行：首行与
// node name 同行，后续每行均加 "> "，确保整段始终留在只读镜像块内并可无损解析。
// MirrorSummaryLines returns canonical quoted lines for a possibly multi-line
// mirror summary.
func MirrorSummaryLines(name, summary string) []string {
	parts := strings.Split(DisplaySummary(summary), "\n")
	lines := make([]string, 0, len(parts))
	lines = append(lines, fmt.Sprintf("> **%s** — %s", name, parts[0]))
	for _, part := range parts[1:] {
		lines = append(lines, "> "+part)
	}
	return lines
}

// renderDoc 把一篇文档的全部 node 块（非 shared 完整块 / shared 镜像块）渲染为整篇 md。
// 块按 node 的 span.StartLine 升序排列；无 span 的排末尾（按 name 稳定序）。
// 文档顶部加 docHeaderComment。
//
// renderDoc renders a whole doc: blocks sorted by span.StartLine (no-span last,
// by name), preceded by the header comment.
func renderDoc(blocks []nodeView, isSharedMirror func(nodeView) bool) string {
	// 排序：有 span 的按 StartLine 升序；无 span 的排末尾，按 name 稳定序。
	// Sort: spanned by StartLine asc; unspanned last by name.
	sorted := append([]nodeView(nil), blocks...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		ai, aj := spanStart(a), spanStart(b)
		if ai == 0 && aj == 0 {
			return a.Name < b.Name
		}
		if ai == 0 {
			return false // a 无 span → 排末尾 / a unspanned → last
		}
		if aj == 0 {
			return true // b 无 span → a 在前 / b unspanned → a first
		}
		if ai != aj {
			return ai < aj
		}
		return a.Name < b.Name
	})

	var sb strings.Builder
	sb.WriteString(docHeaderComment)
	sb.WriteString("\n\n")
	for _, v := range sorted {
		if isSharedMirror != nil && isSharedMirror(v) {
			sb.WriteString(renderMirrorBlock(v))
		} else {
			sb.WriteString(renderFullBlock(v))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// spanStart 返回 nodeView 的排序行号；无 span 返回 0。
// spanStart returns the ordering line number; 0 if no span.
func spanStart(v nodeView) int {
	if v.Span == nil {
		return 0
	}
	return v.Span.StartLine
}

// nonEmpty 返回 s；若 s 为空白则返回 fallback。
// nonEmpty returns s, or fallback if s is blank.
func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// primaryFilePath 返回 shared node 的 primary 文件相对路径：
// _shared/<domain-slug>/<uuid>.md。
// primaryFilePath returns the relative path of a shared node's primary file.
func primaryFilePath(v nodeView) string {
	return PrimaryRelPath(v.DomainSlug, v.FileSlug, v.UUID)
}
