package annotation

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/xcosmosbox/cairn/core/dktypes"
)

// DefaultMaxNameRunes 是 entity/concept 名称的默认长度上限（字符数）。
// 名称应是标准化短名，超长通常意味着 LLM 把整句描述误当成了 name。
// DefaultMaxNameRunes is the default max length (in runes) for an item name.
const DefaultMaxNameRunes = 100

// FormatGate 是双门校验的第一道门（Gate A，纯代码，确定性）：
// 对 AnnotatedDocument 执行命名归一化 + 去重，并做字段合法性校验。
//
// 它不调用 LLM，因此成本低、可预测——先用它快速筛掉结构性问题，再进入昂贵的
// 语义门（Gate B）。校验不通过时返回包裹 ErrRollbackToAnnotate 的错误，触发回退重标注。
//
// FormatGate is the first gate (Gate A, pure code, deterministic): it normalizes
// names, de-duplicates items, and validates fields on an AnnotatedDocument without
// calling an LLM. On failure it returns an error wrapping ErrRollbackToAnnotate.
type FormatGate struct {
	maxNameRunes int
}

// NewFormatGate 创建一个 FormatGate。maxNameRunes ≤0 时取默认值。
// NewFormatGate creates a FormatGate; maxNameRunes ≤ 0 falls back to the default.
func NewFormatGate(maxNameRunes int) *FormatGate {
	if maxNameRunes <= 0 {
		maxNameRunes = DefaultMaxNameRunes
	}
	return &FormatGate{maxNameRunes: maxNameRunes}
}

// Check 对文档执行归一化 + 去重 + 校验，返回清洗后的新文档。
//
// 归一化（总是执行）：
//   - name：去首尾空白、剥离包裹的引号/反引号、折叠内部连续空白为单个空格。
//   - content：去首尾空白。
//   - confidence：钳制到 [0,1]。
//
// 去重（总是执行）：按 (tag, 归一化 name) 去重，保留置信度更高者。
//
// 校验（不通过则回退重标注，返回包裹 ErrRollbackToAnnotate 的错误）：
//   - tag 必须是 entity / concept；
//   - 归一化后 name 非空且不超过 maxNameRunes；
//   - content 非空；
//   - detail 非空（不做向后兼容）；
//   - 同一 name 不得同时被标注为 entity 和 concept（分类歧义）。
//
// 注意：空文档（无 items）不算失败——纯叙述性文档合法地不含 entity/concept。
//
// Check normalizes, de-duplicates, and validates the document, returning a cleaned
// copy. Validation failures return an error wrapping ErrRollbackToAnnotate. An empty
// document is not a failure.
func (g *FormatGate) Check(doc *dktypes.AnnotatedDocument) (*dktypes.AnnotatedDocument, error) {
	if doc == nil {
		return nil, fmt.Errorf("format gate: 文档为 nil")
	}

	cleaned := &dktypes.AnnotatedDocument{Skill: doc.Skill, FilePath: doc.FilePath}

	// dedupKey：(tag, name) → cleaned.Items 中的下标，用于去重合并。
	type dedupKey struct {
		tag  dktypes.AnnotationTag
		name string
	}
	indexByKey := make(map[dedupKey]int)
	// tagsByName：归一化 name → 出现过的 tag 集合，用于检测跨 tag 同名冲突。
	tagsByName := make(map[string]map[dktypes.AnnotationTag]bool)

	for _, it := range doc.Items {
		tag := it.Tag
		name := normalizeName(it.Name)
		content := strings.TrimSpace(it.Content)
		detail := strings.TrimSpace(it.Detail)
		conf := clampConfidence(it.Confidence)
		// SourceSpan 是尽力而为的顺序/溯源快照：仅做自洽性归一化（越界/倒序→nil 降级），
		// 不做行数上界裁剪（Gate A 拿不到原文行数），更不触发回退。
		// SourceSpan is a best-effort ordering/provenance snapshot: only self-consistency
		// normalization (out-of-range/inverted → nil downgrade), no upper-bound clamping
		// (Gate A has no access to source line count) and no rollback.
		span := normalizeSourceSpan(it.SourceSpan)

		// —— 字段合法性校验（不通过即整篇回退）——
		if !tag.IsValid() {
			return nil, fmt.Errorf("format gate: %s 出现非法 tag=%q (name=%q): %w",
				doc.FilePath, it.Tag, name, ErrRollbackToAnnotate)
		}
		if name == "" {
			return nil, fmt.Errorf("format gate: %s 归一化后 name 为空 (原始=%q): %w",
				doc.FilePath, it.Name, ErrRollbackToAnnotate)
		}
		if n := utf8.RuneCountInString(name); n > g.maxNameRunes {
			return nil, fmt.Errorf("format gate: %s name 过长(%d>%d): %q: %w",
				doc.FilePath, n, g.maxNameRunes, name, ErrRollbackToAnnotate)
		}
		if content == "" {
			return nil, fmt.Errorf("format gate: %s content 为空 (name=%q): %w",
				doc.FilePath, name, ErrRollbackToAnnotate)
		}
		// detail 必须非空（不做向后兼容）：为空即回退重标注，由 LLM 重新产出带 detail 的标注。
		if detail == "" {
			return nil, fmt.Errorf("format gate: %s detail 为空 (name=%q): %w",
				doc.FilePath, name, ErrRollbackToAnnotate)
		}

		// 记录 name → tag，用于稍后的跨 tag 冲突检测。
		if tagsByName[name] == nil {
			tagsByName[name] = make(map[dktypes.AnnotationTag]bool)
		}
		tagsByName[name][tag] = true

		// —— 去重：同 (tag, name) 保留置信度更高者 ——
		k := dedupKey{tag: tag, name: name}
		if idx, ok := indexByKey[k]; ok {
			if conf > cleaned.Items[idx].Confidence {
				cleaned.Items[idx].Content = content
				cleaned.Items[idx].Detail = detail
				cleaned.Items[idx].Confidence = conf
				// SourceSpan 透传：高置信度者替换时，其 span 也随之覆盖（span 是增强项，
				// 不影响去重判定本身，仅随胜出单元一起保留）。
				// Pass SourceSpan through: when the higher-confidence item wins, its span
				// replaces the previous one (span is an enhancement; it never affects dedup).
				cleaned.Items[idx].SourceSpan = span
			}
			continue
		}
		indexByKey[k] = len(cleaned.Items)
		cleaned.Items = append(cleaned.Items, dktypes.AnnotatedItem{
			Tag:        tag,
			Name:       name,
			Content:    content,
			Detail:     detail,
			Confidence: conf,
			// SourceSpan 透传：必须显式拷贝，否则 span 会被 Gate A 静默丢弃
			// （构造 AnnotatedItem 时 Go 不会自动从 it 复制未列出的字段）。
			// Pass SourceSpan through explicitly: Go does not auto-copy omitted fields
			// when constructing a struct literal, so span must be assigned here.
			SourceSpan: span,
		})
	}

	// —— 跨 tag 同名冲突：同一 name 既是 entity 又是 concept → 分类歧义，回退 ——
	for name, tags := range tagsByName {
		if len(tags) > 1 {
			return nil, fmt.Errorf("format gate: %s 名称 %q 同时被标注为 entity 和 concept，分类歧义: %w",
				doc.FilePath, name, ErrRollbackToAnnotate)
		}
	}

	return cleaned, nil
}

// normalizeName 归一化名称：去首尾空白、剥离包裹的引号/反引号、折叠内部连续空白。
// normalizeName normalizes a name: trim, strip wrapping quotes/backticks, collapse
// internal whitespace.
func normalizeName(s string) string {
	s = strings.TrimSpace(s)
	// 剥离包裹的中英文引号与反引号（LLM 偶尔会给 name 加引号）。
	s = strings.Trim(s, "`\"'“”‘’")
	// strings.Fields 按 Unicode 空白切分并丢弃空段，等效折叠内部连续空白。
	return strings.Join(strings.Fields(s), " ")
}

// clampConfidence 将置信度钳制到 [0,1]（防御 LLM 偶发越界值，不因此回退）。
// clampConfidence clamps confidence into [0,1] (defensive; not a rollback cause).
func clampConfidence(c float64) float64 {
	switch {
	case c < 0:
		return 0
	case c > 1:
		return 1
	default:
		return c
	}
}

// normalizeSourceSpan 对 SourceSpan 做自洽性归一化：
//   - nil → 直接返回 nil（合法的缺失态，不阻塞）。
//   - StartLine<1 或 EndLine<1 或 StartLine>EndLine → 返回 nil（降级，丢弃非法 span）。
//
// 关键约束：本函数**绝不触发回退**。SourceSpan 是「尽力而为的顺序/溯源快照」
// （决策总账第一块·source_span 定稿），其非法只退化排序，不影响标注单元本身的有效性。
// 亦不做行数上界裁剪——Gate A 拿不到原文行数，无法判断上界。
//
// normalizeSourceSpan normalizes a SourceSpan for self-consistency:
//   - nil → nil (legal absence; never blocks).
//   - StartLine<1 or EndLine<1 or StartLine>EndLine → nil (downgrade, drop the bad span).
//
// This function NEVER triggers a rollback. SourceSpan is a best-effort
// ordering/provenance snapshot (decision ledger · first block); an invalid span only
// degrades ordering and does not invalidate the item itself. No upper-bound clamping is
// done — Gate A has no access to the source line count.
func normalizeSourceSpan(s *dktypes.SourceSpan) *dktypes.SourceSpan {
	if s == nil {
		return nil
	}
	if s.StartLine < 1 || s.EndLine < 1 || s.StartLine > s.EndLine {
		return nil
	}
	return s
}
