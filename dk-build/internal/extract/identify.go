// Package extract 的本文件实现「04 id 规则化」（重构第二期 D 阶段）。
//
// 标注阶段（04）产出的 AnnotatedItem 没有稳定 id：提取阶段若用 name 引用做融合映射，
// 一旦 LLM 改写 name 就会对齐失败。为此在提取投喂之前，由**规则脚本**（而非 LLM）
// 为每个标注单元刷入确定性 id：
//
//	id = <slug(skill)>-<tag>-<slug(name)>
//
// 该 id 含 skill 段，故跨 skill 同名不冲突；同一 skill 下不同文档若出现同名单元会得到
// **相同**的 04 id——由下游按 id 去重 / 覆盖（collectUnitIDs、buildCovered 天然按 id 去重），
// 无需在此合并文档、破坏 per-document 结构与 FilePath 溯源。
//
// 注意：同 skill 同名单元的 detail 多源聚合，推迟到第三期「融合重写」阶段按 members(04 id)
// 聚合处理（那里才真正消费 detail）；本阶段只负责刷 id，保持文档结构与溯源完整。
//
// This file implements rule-based 04 id assignment (refactor phase 2, part D). Before
// extraction, each annotated item gets a deterministic id "<slug(skill)>-<tag>-<slug(name)>"
// by code (not the LLM). The skill segment keeps cross-skill same-names distinct; the
// same name across documents of one skill yields the same id, de-duplicated downstream by
// id — the per-document structure (and FilePath provenance) is preserved.
package extract

import (
	"github.com/xcosmosbox/domain-knowledge-layer/core/dktypes"
)

// Build04ID 按规则构造 04 标识：<slug(skill)>-<tag>-<slug(name)>。
// tag 已是小写枚举（entity/concept），skill/name 走 slugify（保留中文、非字母数字折叠为连字符）。
//
// Build04ID builds the 04 identifier "<slug(skill)>-<tag>-<slug(name)>".
func Build04ID(skill string, tag dktypes.AnnotationTag, name string) string {
	return slugify(skill) + "-" + string(tag) + "-" + slugify(name)
}

// AssignIDs 为所有标注单元刷入规则化 04 id，并**保留原有 per-document 结构**（不合并文档）。
// 每篇 reference 文档仍是独立的 AnnotatedDocument（FilePath 完整保留，供溯源与 dump）。
//
// AssignIDs assigns the rule-based 04 id to every item while preserving the per-document
// structure (FilePath intact).
func AssignIDs(docs []*dktypes.AnnotatedDocument) []*dktypes.AnnotatedDocument {
	out := make([]*dktypes.AnnotatedDocument, 0, len(docs))
	for _, d := range docs {
		if d == nil {
			continue
		}
		nd := &dktypes.AnnotatedDocument{Skill: d.Skill, FilePath: d.FilePath}
		for _, it := range d.Items {
			ni := it
			ni.ID = Build04ID(d.Skill, it.Tag, it.Name)
			nd.Items = append(nd.Items, ni)
		}
		out = append(out, nd)
	}
	return out
}
