package annotation

import (
	"fmt"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"log"
	"math"
	"strings"
)

// Kept as a constructor compatibility value; names have no arbitrary length limit.
const DefaultMaxNameRunes = 100

type FormatGate struct{}

func NewFormatGate(_ int) *FormatGate { return &FormatGate{} }

// Check normalizes each item independently. Malformed rows are observable local
// failures; distinct facts and source incidences sharing a name are all retained.
func (g *FormatGate) Check(doc *dktypes.AnnotatedDocument) (*dktypes.AnnotatedDocument, error) {
	if doc == nil {
		return nil, fmt.Errorf("format gate: nil document")
	}
	cleaned := &dktypes.AnnotatedDocument{Skill: doc.Skill, FilePath: doc.FilePath}
	for i, it := range doc.Items {
		it.Name = normalizeName(it.Name)
		it.Content = strings.TrimSpace(it.Content)
		it.Detail = strings.TrimSpace(it.Detail)
		if !it.Tag.IsValid() || it.Name == "" || it.Content == "" || math.IsNaN(it.Confidence) || math.IsInf(it.Confidence, 0) || it.Confidence < 0 || it.Confidence > 1 {
			log.Printf("[format-gate] rejected malformed item %d in %s", i, doc.FilePath)
			continue
		}
		it.SourceSpan = normalizeSourceSpan(it.SourceSpan)
		cleaned.Items = append(cleaned.Items, it)
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
