// Package dktypes 定义领域知识层（Cairn）中所有跨包共享的核心类型、
// 枚举、数据结构及其校验方法。
//
// 本文件定义「LLM 标注阶段」的产出契约：AnnotatedDocument / AnnotatedItem。
// 该契约同时是标注器（internal/annotation.LLMAnnotator）的产出与提取器
// （internal/extract）的输入，因此放在中立的 dktypes 包中以避免 import cycle。
//
// 设计要点（与规则匹配旧流水线的根本差异）：
//   - 标注阶段只区分 entity / concept 两类，不涉及 domain / subdomain 归属；
//     领域层级在后续「提取阶段」由掌握全局视野的 LLM 归纳产生。
//   - 不区分 public / private —— 可见性概念已从全系统移除。
//   - 每篇 reference 文档独立标注（1 文档 = 1 次 LLM 调用），Items 是该文档内
//     被结构化出来的知识单元集合，要求「意思层面幂等」。
//
// This file defines the output contract of the LLM annotation stage:
// AnnotatedDocument / AnnotatedItem. It is both the output of the annotator
// (internal/annotation.LLMAnnotator) and the input of the extractor
// (internal/extract), so it lives in the neutral dktypes package to avoid an
// import cycle. Annotation only distinguishes entity/concept (no domain/subdomain,
// no public/private); the domain hierarchy is inferred later by the extraction
// stage from a global view.
package dktypes

// ——————————————————————————————————————————————————————————————————————————————
// AnnotationTag — 标注单元的类型标签
// ——————————————————————————————————————————————————————————————————————————————

// AnnotationTag 表示标注阶段一条知识单元的类型。标注阶段只允许两类，刻意不含
// domain / subdomain —— 那属于提取阶段的职责。
//
// AnnotationTag is the type of a knowledge item produced by the annotation stage.
// Only two kinds are allowed; domain/subdomain are intentionally excluded as they
// belong to the extraction stage.
type AnnotationTag string

const (
	// TagEntity 表示一条具体的实体：文档中描述的、有明确边界的对象 / 组件 / 系统 / 角色。
	// TagEntity marks a concrete entity: a bounded object/component/system/role.
	TagEntity AnnotationTag = "entity"
	// TagConcept 表示一条抽象的概念：文档中描述的概念 / 规则 / 方法 / 约束。
	// TagConcept marks an abstract concept: a concept/rule/method/constraint.
	TagConcept AnnotationTag = "concept"
)

// IsValid 校验当前 AnnotationTag 是否为合法枚举值。
// IsValid checks whether the current AnnotationTag is a valid enum value.
func (t AnnotationTag) IsValid() bool {
	switch t {
	case TagEntity, TagConcept:
		return true
	default:
		return false
	}
}

// ——————————————————————————————————————————————————————————————————————————————
// AnnotatedItem — 单条被标注的知识单元
// ——————————————————————————————————————————————————————————————————————————————

// AnnotatedItem 是标注阶段从一篇文档中梳理出的一条结构化知识单元。它把人类离散的
// 自然语言表述，标准化为「类型 + 名称 + 内容」的幂等三元组，供提取阶段带标签投喂。
//
// AnnotatedItem is one structured knowledge unit distilled from a document by the
// annotation stage. It standardizes discrete human prose into an idempotent
// (tag + name + content) triple, fed with its tag into the extraction stage.
type AnnotatedItem struct {
	// ID 是该单元的规则化 04 标识：<slug(skill)>-<tag>-<slug(name)>，由代码在提取投喂前
	// 刷入（非 LLM 产出），供提取阶段以 members 引用做融合映射，以及跨阶段溯源。
	// 标注器刚产出时为空，经 extract.AssignIDsAndMerge 刷入后非空。
	// ID is the rule-assigned 04 identifier (<slug(skill)>-<tag>-<slug(name)>), injected
	// by code before extraction (not by the LLM), used by the extraction stage's fusion
	// members and for cross-stage tracing. Empty right after annotation; filled by
	// extract.AssignIDsAndMerge.
	ID string `json:"id,omitempty"`
	// Tag 是该单元的类型，只能是 entity 或 concept。
	// Tag is the item type; only entity or concept.
	Tag AnnotationTag `json:"tag"`
	// Name 是该单元的标准化名称。
	// Name is the standardized name of the item.
	Name string `json:"name"`
	// Content 是 LLM 梳理后的标准化内容，要求在意思层面对同一原文幂等。
	// Content is the LLM-normalized content, idempotent in meaning for the same source.
	Content string `json:"content"`
	// Detail 是 LLM 在语义幂等前提下尽量保留原文长度与细节的详述，供后续查询端消费。
	// 与 Content 的区别：Content 是提炼摘要（供领域归纳阶段使用，短），Detail 保留细节（长）。
	// Detail 应比 Content 更长，且不允许为空——Gate A 会对空 Detail 触发回退重标注（不做向后兼容）。
	// Detail is a fuller description preserving original length/detail (semantically
	// idempotent), consumed by the query frontend. It must be longer than Content and
	// must not be empty; Gate A rolls back to re-annotate on an empty Detail.
	Detail string `json:"detail"`
	// Confidence 是该单元的置信度（0.0 ~ 1.0）。
	// Confidence is the confidence score of the item (0.0 to 1.0).
	Confidence float64 `json:"confidence"`
	// SourceSpan 是该单元在来源文档中的原文位置快照（1-based 闭区间）。
	// 它是首次构建时记录的「顺序 / 溯源」辅助信息：用于结构化块按原文顺序排列与审计。
	// 回写不依赖它定位（回写为整篇替换），缺失只退化排序、不阻塞、不回退；故可为 nil。
	// Gate A 会对其做自洽性归一化（越界 / 倒序 → 置 nil 降级），但不触发回退。
	//
	// SourceSpan is a best-effort snapshot of the item's location in its source document
	// (1-based, inclusive). It is only a first-build ordering/provenance hint: write-back
	// does NOT rely on it for locating text (write-back replaces the whole document), so a
	// missing span only degrades ordering, never blocks or rolls back; hence it may be nil.
	// Gate A normalizes it for self-consistency (out-of-range / inverted → nil) without rollback.
	SourceSpan *SourceSpan `json:"source_span,omitempty"`
}

// SourceSpan 描述一条 04 标注单元在其来源文档中的原文位置快照。
// StartLine / EndLine 均为 1-based 闭区间（即 [StartLine, EndLine] 含两端）。
// Quote 是原文首尾片段（建议首尾各 ~30 字），便于人工核对，可空。
//
// 重要：本结构仅作「首次构建的顺序 / 溯源快照」，回写不依赖它定位（决策总账第一块·
// source_span 定稿），可为 nil；Gate A 对其只做自洽性归一化，不做行数上界裁剪、不触发回退。
//
// SourceSpan captures where a 04 annotated unit sits in its source document.
// StartLine / EndLine are 1-based inclusive ([StartLine, EndLine]). Quote holds short
// head/tail snippets (~30 chars each) for manual cross-checking and may be empty.
//
// Note: this struct is only a first-build ordering/provenance snapshot; write-back does
// NOT rely on it for locating text (decision ledger · first block), so it may be nil.
// Gate A only normalizes it for self-consistency, never clamps by document length or rolls back.
type SourceSpan struct {
	// StartLine 是原文起始行号（1-based，含）。
	// StartLine is the 1-based inclusive starting line number.
	StartLine int `json:"start_line"`
	// EndLine 是原文结束行号（1-based，含），应 ≥ StartLine。
	// EndLine is the 1-based inclusive ending line number; must be ≥ StartLine.
	EndLine int `json:"end_line"`
	// Quote 是原文首尾片段，便于人工核对，可空。
	// Quote is a short snippet of the source text for manual cross-checking; may be empty.
	Quote string `json:"quote,omitempty"`
}

// ——————————————————————————————————————————————————————————————————————————————
// AnnotatedDocument — 单篇 reference 文档的标注产出
// ——————————————————————————————————————————————————————————————————————————————

// AnnotatedDocument 是对单篇 reference/*.md 文档标注后的产出。每篇文档独立标注、
// 独立重试，文档之间在标注阶段完全隔离。
//
// AnnotatedDocument is the annotation output for a single reference/*.md file.
// Each document is annotated and retried independently; documents are fully
// isolated during the annotation stage.
type AnnotatedDocument struct {
	// Skill 是该文档所属的 skill 名称（来自 discovery）。
	// Skill is the name of the skill this document belongs to (from discovery).
	Skill string `json:"skill"`
	// FilePath 是该文档相对于仓库根目录的路径（用于溯源与状态跟踪）。
	// FilePath is the document path relative to the repo root (provenance & state).
	FilePath string `json:"file_path"`
	// Items 是从该文档中梳理出的全部 entity / concept 单元。
	// Items are all entity/concept units distilled from this document.
	Items []AnnotatedItem `json:"items"`
}

// EntityItems 返回文档中所有 entity 类单元（便于统计与校验）。
// EntityItems returns all entity-tagged items in the document.
func (d *AnnotatedDocument) EntityItems() []AnnotatedItem {
	return d.itemsByTag(TagEntity)
}

// ConceptItems 返回文档中所有 concept 类单元。
// ConceptItems returns all concept-tagged items in the document.
func (d *AnnotatedDocument) ConceptItems() []AnnotatedItem {
	return d.itemsByTag(TagConcept)
}

func (d *AnnotatedDocument) itemsByTag(tag AnnotationTag) []AnnotatedItem {
	var out []AnnotatedItem
	for _, it := range d.Items {
		if it.Tag == tag {
			out = append(out, it)
		}
	}
	return out
}
