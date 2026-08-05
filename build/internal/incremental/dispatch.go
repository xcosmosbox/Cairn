// Package incremental 的本文件实现 I-2 变更分派（双路径闸门）：
//
//   - C1 块内改 / C2 块删除 / C3 只读区改 → 旁路（不碰标注/提取 LLM）：
//     C1 → 旁路 R（直接改 KG + 脏传播），C2 → 旁路 D（引用计数软删 + 传播），
//     C3 → 忽略 + 告警（回写阶段用 KG 权威值还原）；
//   - C4 块外新增 → 管道 P（标注 → 对齐 → 融合 → 重算）。
//
// 本文件只做「分派」纯函数：把多篇文档的变更集拆成旁路计划与管道计划，
// 不触碰 KG/LLM（旁路执行在 bypass.go，管道在 annotate.go/align.go）。
//
// This file implements the I-2 dispatch gate as a pure function: C1/C2/C3 go to
// the bypass path (no LLM), C4 goes to the pipeline path. Execution lives in
// bypass.go / annotate.go / align.go.
package incremental

// ——————————————————————————————————————————————————————————————————————————————
// 分派产物 / Dispatch products
// ——————————————————————————————————————————————————————————————————————————————

// C3Warning 是一条带文档上下文的 C3 告警（可观测；回写阶段统一还原）。
// C3Warning is a C3 violation with its doc context (observability).
type C3Warning struct {
	Path string
	C3Violation
}

// BypassPlan 是旁路路径的工作计划（C1/C2/C3，零 LLM）。
// BypassPlan is the bypass-path work plan (C1/C2/C3, zero LLM).
type BypassPlan struct {
	// C1Edits 按文档分组的块内编辑（旁路 R 的输入）。
	// C1Edits groups editable-area changes by doc (bypass R input).
	C1Edits map[string][]BlockEdit
	// C2Deletions 按文档分组的被删块 uuid（旁路 D 的输入）。
	// C2Deletions groups deleted-block uuids by doc (bypass D input).
	C2Deletions map[string][]string
	// C3Warnings 是全部 C3 告警（忽略 + 告警；I-9 用 KG 权威值还原）。
	// C3Warnings collects all read-only violations (warn only; restored at I-9).
	C3Warnings []C3Warning
	// DeletedDocs 是整篇删除的文档路径（其 C2 已含在 C2Deletions；
	// I-9 需额外删 sidecar 与 file_states）。
	// DeletedDocs lists wholly deleted docs (their C2s are inside C2Deletions;
	// I-9 additionally removes sidecars and file_states).
	DeletedDocs []string
}

// PipelinePlan 是管道 P 的工作计划（C4，走标注 → 对齐 → 融合）。
// PipelinePlan is the pipeline-path work plan (C4: annotate → align → fuse).
type PipelinePlan struct {
	// C4ByDoc 按文档分组的块外新增段（I-3 标注的输入）。
	// C4ByDoc groups outside-block additions by doc (I-3 annotation input).
	C4ByDoc map[string][]TextSegment
	// SkillByDoc 是文档 → 所属 skill 的映射（标注单元刷 04 id 需要 skill 段）。
	// SkillByDoc maps doc → owning skill (needed to mint 04 ids).
	SkillByDoc map[string]string
}

// Dispatch 是 I-2 双路径闸门：把全部变更文档的 DocChangeSet 拆分为旁路计划
// （C1/C2/C3，零 LLM）与管道计划（C4，走完整管道）。
// skillByDoc 提供文档 → skill 的映射（编排器从 discovery 扫描结果构建），
// 供管道 P 标注时刷 04 id；查不到 skill 的文档以 "" 兜底（Build04ID 仍能产出 id）。
//
// Dispatch is the I-2 two-path gate: it splits all changed docs' change sets into
// a bypass plan (C1/C2/C3, zero LLM) and a pipeline plan (C4).
func Dispatch(changesets []*DocChangeSet, skillByDoc map[string]string) (*BypassPlan, *PipelinePlan) {
	bp := &BypassPlan{
		C1Edits:     make(map[string][]BlockEdit),
		C2Deletions: make(map[string][]string),
	}
	pp := &PipelinePlan{
		C4ByDoc:    make(map[string][]TextSegment),
		SkillByDoc: make(map[string]string),
	}
	for _, cs := range changesets {
		if cs == nil || (!cs.HasChanges() && !cs.Deleted) {
			continue
		}
		// —— 旁路：C1 块内改 → R；C2 块删除 → D；C3 → 告警 ——
		if len(cs.C1) > 0 {
			bp.C1Edits[cs.Path] = append(bp.C1Edits[cs.Path], cs.C1...)
		}
		if len(cs.C2) > 0 {
			bp.C2Deletions[cs.Path] = append(bp.C2Deletions[cs.Path], cs.C2...)
		}
		for _, v := range cs.C3 {
			bp.C3Warnings = append(bp.C3Warnings, C3Warning{Path: cs.Path, C3Violation: v})
		}
		if cs.Deleted {
			bp.DeletedDocs = append(bp.DeletedDocs, cs.Path)
		}
		// —— 管道：C4 块外新增 → P ——
		if len(cs.C4) > 0 {
			pp.C4ByDoc[cs.Path] = append(pp.C4ByDoc[cs.Path], cs.C4...)
			pp.SkillByDoc[cs.Path] = skillByDoc[cs.Path]
		}
	}
	return bp, pp
}
