// Package incremental 的本文件实现 I-2 旁路（零 LLM）的三条执行路径：
//
//   - 旁路 R（C1 块内编辑）：uuid 稳定 → 直接定位 node，把块内当前 summary/
//     description 写入 KG，provenance 升 human_curated（R7）；只脏 node 内容
//     （重写 + FTS），不脏 subdomain relation（V1 定稿：信任编辑者）。
//   - 旁路 D（C2 块删除）：引用计数软删——先删 (node, 文档) 来源行，再查剩余
//     distinct file_path：>0 存活（uuid 钉死不变，R2，不写 uuid_lineage）；
//     ==0 真删 node + 清出入边（R5 绝不悬空）+ 脏其 subdomain relation。
//   - 旁路 C3：只读区篡改 → 忽略 + 告警，不改 KG；I-9 用 KG 权威值还原。
//
// 铁律映射：R2（uuid 钉死）、R5（无悬空边）、R7（human_curated 升级）、
// R8（human_curated 的 description 后续不被重融合覆盖）、R9（只动脏集）。
//
// This file implements the zero-LLM bypass paths: R (C1 — write block content
// into the KG, upgrade provenance to human_curated, dirty content only), D
// (C2 — reference-counted soft delete; true delete at zero with edge cleanup),
// and C3 (warn-only; restored at write-back).
package incremental

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/xcosmosbox/cairn/core/storage"
)

// applyBypass 执行旁路计划（R/D/C3），把产生的脏标记与受影响文档记入 runState。
// 返回错误仅在存储层失败（不是篡改/告警——那些只记录不失败）。
//
// applyBypass executes the bypass plan (R/D/C3), recording dirty marks and
// affected docs into rs. Errors are storage-layer failures only.
func applyBypass(ctx context.Context, st *stores, bp *BypassPlan, rs *runState) error {
	var c1Applied, c1Phantom, c2Handled int
	deletedBefore := len(rs.deleted)
	// —— 旁路 R：C1 块内编辑 ——
	for path, edits := range bp.C1Edits {
		for _, edit := range edits {
			if err := applyBypassR(ctx, st, edit, rs); err != nil {
				return fmt.Errorf("bypass R %s (%s): %w", path, edit.UUID, err)
			}
			// 可观测性：区分「人的编辑真的写进 KG」与「指向 phantom node 被忽略」。
			// 两者此前在日志上完全不可分（I-8 只报新建/融入），人的编辑被静默丢弃时
			// 无从察觉——必须靠查库才能确认，这正是同类缺陷长期潜伏的原因。
			if _, ok := rs.touched[edit.UUID]; ok {
				c1Applied++
			} else {
				c1Phantom++
			}
		}
		rs.affectDocs(path)
	}
	// —— 旁路 D：C2 块删除（引用计数软删）——
	for path, uuids := range bp.C2Deletions {
		for _, uuid := range uuids {
			if err := applyBypassD(ctx, st, path, uuid, rs); err != nil {
				return fmt.Errorf("bypass D %s (%s): %w", path, uuid, err)
			}
			c2Handled++
		}
		rs.affectDocs(path)
	}
	if c1Applied+c1Phantom+c2Handled > 0 {
		log.Printf("[incremental-bypass] 旁路写入: C1 写入 KG %d 个 node（phantom 忽略 %d），C2 处理 %d（其中真删 %d）",
			c1Applied, c1Phantom, c2Handled, len(rs.deleted)-deletedBefore)
	}
	// —— 旁路 C3：只读区篡改 → 忽略 + 告警（I-9 统一用 KG 权威值还原）——
	for _, w := range bp.C3Warnings {
		msg := fmt.Sprintf("C3 只读区篡改（忽略+还原）: %s uuid=%s field=%s want=%q got=%q",
			w.Path, w.UUID, w.Field, w.Want, w.Got)
		log.Printf("[incremental] ⚠ %s", msg)
		rs.warnf(msg)
		rs.affectDocs(w.Path) // 该文档需在 I-9 用 KG 值重渲染还原 / restore at I-9
		// 问题 1：被篡改的若是 _shared primary，必须把 uuid 加入 primary 同步候选，
		// 由 syncPrimaries 用 KG 权威值恢复完整 primary（普通文档通道不写 primary，
		// 不得出现「无人恢复」或「先写空再写对」的双写）。
		if IsSharedPrimaryPath(w.Path) && w.UUID != "" {
			if _, ok := rs.touched[w.UUID]; !ok {
				rs.touched[w.UUID] = &touchedNodeInfo{}
			}
		}
	}
	return nil
}

// applyBypassR 应用一条 C1 块内编辑：把块内当前 summary/description 写入 KG 并
// 升级 provenance=human_curated（R7）。uuid 稳定直接定位；node 不存在（phantom）
// 只告警不改 KG（该块在 I-9 重写时随 node_sources 成员关系自然消失）。
//
// applyBypassR applies one C1 edit: writes the block's current summary/description
// into the KG and upgrades provenance to human_curated (R7).
func applyBypassR(ctx context.Context, st *stores, edit BlockEdit, rs *runState) error {
	result, err := st.mutations.ApplyContentEdit(ctx, edit.UUID,
		edit.SummaryChanged, edit.NewSummary, edit.DescriptionChanged, edit.NewDescription,
		time.Now().UTC())
	if err != nil {
		return err
	}
	if result.Node == nil {
		rs.warnf(fmt.Sprintf("C1 指向 KG 中不存在的 node（忽略，I-9 重写时丢弃该块）: uuid=%s", edit.UUID))
		return nil
	}
	node := result.Node

	// 脏传播（V1 定稿）：C1 只脏 node 内容（重写 + FTS），不脏 subdomain relation。
	key := SubdomainKey{Domain: node.Domain, Subdomain: node.Subdomain}
	rs.dirty.DirtyNodeContent(node.ID, key)

	// I-9 受影响文档：该 node 的全部来源文档（shared 时镜像块含 name+summary，
	// summary 变了镜像必须同步；primary 由 I-9 对 dirty shared node 单独重写）。
	docs := result.SourceDocs
	rs.touched[node.ID] = &touchedNodeInfo{
		SourceDocs: docs,
		WasShared:  len(docs) >= 2,
	}
	rs.affectDocs(docs...)
	return nil
}

// applyBypassD 应用一条 C2 块删除（引用计数软删，坑点 5）：
//  1. 先删 (node, 文档) 来源行——删的是"该文档对该 node 的贡献"，不是无条件删 node；
//  2. 查剩余 distinct file_path：>0 → node 存活（其他文档还贡献它），仅该文档不再
//     展示它；uuid 钉死不变（R2），不写 uuid_lineage；若删的是 _shared primary 文档，
//     因 primary 在独立区、存活只取决于引用计数，无需重选（primary 永不漂移）；
//  3. ==0 → 真删：NodeRepo.Delete + EdgeRepo.DeleteBySource/DeleteByTarget（R5 无悬空）
//     + 脏其 subdomain relation + I-9 删 primary、重写镜像文档。
//
// applyBypassD applies one C2 deletion with reference-counted soft delete:
// remove the (node, doc) source rows first, then check remaining distinct
// file_path count; >0 the node survives (uuid pinned, R2), ==0 the node is
// truly deleted with all edges cleaned (R5).
func applyBypassD(ctx context.Context, st *stores, docPath, uuid string, rs *runState) error {
	result, err := st.mutations.ApplySourceDeletion(ctx, uuid, docPath)
	if err != nil {
		return err
	}
	preDocs := result.SourceDocs
	if result.Phantom {
		// phantom：KG 无此 node（可能是旧版本留下的半提交删除）→ storage 已在
		// 同一事务清完该 UUID 的全部 orphan 来源行与悬空出入边。必须把清理前的
		// 全部来源文档发布给 I-9，才能移除那些文档中的残留块/sidecar；不能因
		// node 快照缺失就提前丢掉 runState。
		rs.affectDocs(preDocs...)
		rs.warnf(fmt.Sprintf("C2 指向 KG 中不存在的 node（已清全部残留来源行与出入边）: %s uuid=%s", docPath, uuid))
		return nil
	}
	if result.NoContribution {
		// fail-closed：node 仍存在，但目标文档没有任何来源行。此 C2 可能来自
		// stale sidecar，绝不能用“全局引用数恰为 0”推断真删。
		rs.warnf(fmt.Sprintf("C2 目标文档未贡献该 node（忽略删除，fail-closed）: %s uuid=%s", docPath, uuid))
		return nil
	}
	node := result.Node

	rs.affectDocs(preDocs...) // 含 docPath 与该 node 的其他来源（shared 翻转需重渲染）

	if !result.Deleted {
		// 存活：members 随来源行自然收缩，uuid 钉死（R2）。shared 可能从 ≥2 翻转为 1
		// （镜像块 → 完整块、primary 删除）——I-9 按 node_sources 实况重渲染处理。
		rs.touched[uuid] = &touchedNodeInfo{SourceDocs: preDocs, WasShared: len(preDocs) >= 2}
		return nil
	}

	// 脏传播：node 删除 → 其 subdomain relation 拓扑变了（I-7 重算排除已删端点）。
	rs.dirty.DirtySubdomain(SubdomainKey{Domain: node.Domain, Subdomain: node.Subdomain})
	rs.deleted[uuid] = &deletedNodeInfo{
		Node:       node,
		WasShared:  len(preDocs) >= 2,
		SourceDocs: preDocs,
		RowID:      result.RowID,
	}
	return nil
}

// distinctFilePaths 提取来源行集合的 distinct file_path（稳定排序，输出确定）。
// distinctFilePaths extracts distinct file paths from source rows (stable order).
func distinctFilePaths(srcs []storage.NodeSource) []string {
	seen := make(map[string]bool)
	var out []string
	for _, s := range srcs {
		if s.FilePath == "" || seen[s.FilePath] {
			continue
		}
		seen[s.FilePath] = true
		out = append(out, s.FilePath)
	}
	return out
}
