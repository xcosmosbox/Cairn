// Package incremental 的本文件实现 I-9 增量回写：
// 仅重写「受影响」的文档与 _shared primary/镜像，其余文档零改动（R9）；
// 每篇受影响文档按 KG 权威值重新渲染其 uuid 块序列（更新/新增/删除块），
// 整篇覆盖写（R6）+ 更新 sidecar baseline；C3 被篡改的只读区在此被 KG 值覆盖还原。
//
// 关键设计：
//   - 文档应含块集合 = node_sources 中 file_path=该文档 的 distinct node
//     （I-8 已同步增删，KG 是唯一权威）；
//   - shared 状态按 node 当前 distinct 来源文档数（≥2）现算——shared 翻转
//     （完整块↔镜像块转换、primary 建立/删除）在此自然处理；
//   - 渲染结果与现文件字节比对：不变则不写盘（最小改动：内容未变的文档连
//     mtime 都不动）；sidecar 仅在 md 变化或缺失时重写；
//   - 镜像块内容 = primary 最新 summary（KG 权威值），C3 篡改自然还原；
//   - 整篇删除的文档：不重写（人已删 md），删其 sidecar 即可。
//
// This file implements I-9 incremental write-back: only affected docs and
// _shared primaries are rewritten (R9), each re-rendered from KG-authoritative
// values (restoring C3 tampering), whole-file replace (R6) with sidecar
// baseline refresh; no-op writes are skipped via byte comparison.
package incremental

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/xcosmosbox/domain-knowledge-layer/core/dktypes"
	"github.com/xcosmosbox/domain-knowledge-layer/core/storage"
	"github.com/xcosmosbox/domain-knowledge-layer/dk-build/internal/writeback"
)

// rewriteReport 是 I-9 的结果摘要（可观测）。
// rewriteReport summarizes I-9 (observability).
type rewriteReport struct {
	DocsRewritten    int      // 实际写盘的文档数（字节有变化才写）/ docs actually written
	DocsSkipped      int      // 渲染后与现文件一致而跳过的文档数 / docs skipped (no-op)
	PrimariesWritten int      // 写盘的 _shared primary 数 / primaries written
	PrimariesDeleted int      // 删除的 _shared primary 数（node 真删或翻转为非 shared）
	SidecarsDeleted  int      // 整篇删除文档的 sidecar 删除数 / sidecars removed
	FailedDocs       []string // 写盘失败的文档（baseline 不前移，下轮重试）
}

// applyIncrementalWriteback 执行 I-9：重写受影响文档 + shared primary/镜像 + sidecar。
// deletedDocs 是整篇删除的文档（跳过重写，删 sidecar）；ao 提供 pending node
// （其全部来源文档都纳入受影响集——含 shared 翻转的旧来源文档）。
//
// 返回 (report, error)：单文档/primary 写失败不中断（其余照常），但逐个记入
// rs.failedDocs 并聚合为返回 error（问题 5：失败路径必须显式，不得仅 warning 掩盖）。
//
// applyIncrementalWriteback runs I-9: rewrites affected docs and _shared
// primaries, refreshes sidecars, and removes sidecars of deleted docs. Per-file
// failures are recorded in rs.failedDocs and aggregated into the returned error.
func applyIncrementalWriteback(ctx context.Context, st *stores, rs *runState,
	ao *alignOutcome, deletedDocs []string, repoRoot string) (*rewriteReport, error) {
	rpt := &rewriteReport{}
	now := time.Now().UTC()

	// 展示名缓存：domain/subdomain slug → 中文名（渲染「归属」行用）。
	names, err := loadLayerDisplayNames(ctx, st)
	if err != nil {
		return nil, err
	}

	// —— 1. 受影响文档全集：rs.affectedDocs ∪ 全部 pending node 的来源文档 ——
	affected := rs.affectedDocsSnapshot()
	if ao != nil {
		for _, uuid := range sortedPendingKeys(ao) {
			srcs, err := st.sources.ListByNode(ctx, uuid)
			if err != nil {
				return nil, fmt.Errorf("I-9 list sources of %s: %w", uuid, err)
			}
			for _, fp := range distinctFilePaths(srcs) {
				affected[fp] = true
			}
		}
	}
	// _shared primary 文档不作为普通文档处理（单独走 primary 通道）。
	deletedSet := make(map[string]bool, len(deletedDocs))
	for _, p := range deletedDocs {
		deletedSet[p] = true
	}

	// —— 2. 整篇删除的文档：删 sidecar（md 人已删），不重写 ——
	for _, p := range deletedDocs {
		// A deleted md must never enter the ordinary rewrite loop, even when
		// sidecar cleanup fails. Keeping it in affected would resurrect the
		// human-deleted document from KG during the same failed run.
		delete(affected, p)
		if err := writeback.DeleteSidecar(repoRoot, p); err != nil {
			log.Printf("[incremental-writeback] 删 sidecar %s 失败（下轮重试）: %v", p, err)
			rs.failDoc(p)
			rs.warnf(fmt.Sprintf("I-9 删 sidecar %s 失败: %v", p, err))
			continue
		}
		rpt.SidecarsDeleted++
	}

	// —— 3. 逐受影响文档：按 KG 权威值重渲染 + 字节比对 + 按需写盘 ——
	// _shared primary 绝不进入普通文档通道（问题 1）：它在 node_sources 中查不到
	// file_path=primary 的行，普通通道会把它渲染成只有头注释的空文档——
	// primary 只走下方的 syncPrimaries 通道。
	paths := make([]string, 0, len(affected))
	for p := range affected {
		if IsSharedPrimaryPath(p) {
			continue
		}
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, docPath := range paths {
		updates, err := buildDocNodeUpdates(ctx, st, docPath, names)
		if err != nil {
			return nil, fmt.Errorf("I-9 build views for %s: %w", docPath, err)
		}
		rendered := writeback.RenderDoc(updates)
		// 问题 2：C4 未解决文档若有其它变更需重写，未吸收散文原样追加保留
		// （绝不静默删除尚未被 KG 持久化的人类输入）。
		if prose, ok := rs.c4Preserve[docPath]; ok && strings.TrimSpace(prose) != "" {
			rendered = strings.TrimRight(rendered, "\n") + "\n\n" + prose + "\n"
		}
		current, readErr := os.ReadFile(filepath.Join(repoRoot, docPath))
		if readErr == nil && string(current) == rendered {
			// md 字节一致：不写 md（R9 最小改动）；但 sidecar 缺失/陈旧时仍要刷新——
			// 否则「上轮 md 写好、sidecar 写失败」的现场会永远跳过重试（问题 5）。
			if writeback.SidecarStale(repoRoot, docPath, rendered, updates) {
				if err := writeback.WriteSidecarForDoc(repoRoot, docPath, rendered, updates, now); err != nil {
					log.Printf("[incremental-writeback] 补写 sidecar %s 失败（下轮重试）: %v", docPath, err)
					rs.failDoc(docPath)
					rs.warnf(fmt.Sprintf("I-9 补写 sidecar %s 失败: %v", docPath, err))
					continue
				}
				rpt.DocsRewritten++ // sidecar 刷新也算一次成功回写（baseline 可前移）
			} else {
				rpt.DocsSkipped++
			}
			continue
		}
		if err := writeback.WriteDocContent(repoRoot, docPath, rendered, updates, now); err != nil {
			// 单文档失败降级（不阻断其它文档），记 failedDocs（baseline 不前移，下轮重试）。
			log.Printf("[incremental-writeback] 写文档 %s 失败（下轮重试）: %v", docPath, err)
			rs.failDoc(docPath)
			rs.warnf(fmt.Sprintf("I-9 写文档 %s 失败: %v", docPath, err))
			continue
		}
		rpt.DocsRewritten++
	}

	// —— 4. _shared primary 通道：dirty/touched/pending/deleted node 的 primary 处理 ——
	if err := rpt.syncPrimaries(ctx, st, rs, ao, repoRoot, now); err != nil {
		return rpt, err
	}

	// 聚合一处失败信息（问题 5：失败路径显式向上报告）。
	failedSnap := rs.failedDocsSnapshot()
	if len(failedSnap) > 0 {
		rpt.FailedDocs = rpt.FailedDocs[:0]
		for p := range failedSnap {
			rpt.FailedDocs = append(rpt.FailedDocs, p)
		}
		sort.Strings(rpt.FailedDocs)
		return rpt, fmt.Errorf("I-9 有 %d 篇文档/primary 写盘失败（baseline 未前移，下轮可重试）: %s",
			len(rpt.FailedDocs), strings.Join(rpt.FailedDocs, ", "))
	}
	return rpt, nil
}

// syncPrimaries 同步 _shared primary 文档：
//   - 真删的 shared node → 删其 primary；
//   - 当前 shared（≥2 来源）的 dirty/touched/pending node → 渲染 primary（字节比对后写盘）；
//   - 翻转为非 shared（<2 来源）但 primary 文件存在 → 删 primary（shared 状态翻转）。
//
// syncPrimaries syncs _shared primaries: delete for truly deleted shared nodes,
// render+write for currently shared dirty nodes, delete on shared→non-shared flips.
func (rpt *rewriteReport) syncPrimaries(ctx context.Context, st *stores, rs *runState,
	ao *alignOutcome, repoRoot string, now time.Time) error {
	names, err := loadLayerDisplayNames(ctx, st)
	if err != nil {
		return err
	}

	// KG 权威映射：primary 路径 → 真 node UUID。引入 file_slug 后 primary 文件名是
	// 可读 slug、不再等于 UUID，因此禁止从路径 basename 反解身份——否则健康的 slug
	// 命名 primary 会以 slug 作 uuid 查库、查不到而走 stale 分支被误删。
	_, primaryUUIDByPath, err := listExpectedSharedPrimaryDocs(ctx, st)
	if err != nil {
		return err
	}

	// 候选 uuid：touched（C1/C2 存活）∪ pending（融入/新建）∪ deleted（真删）
	// ∪ affectedDocs 中的 _shared primary 路径（问题 1：C3 篡改 primary 只有路径
	// 没有 touched 记录——经 KG 权威映射解析为真 uuid 加入候选，确保被恢复）。
	candidates := make(map[string]bool)
	for uuid := range rs.touched {
		candidates[uuid] = true
	}
	if ao != nil {
		for uuid := range ao.pending {
			candidates[uuid] = true
		}
	}
	for uuid := range rs.deleted {
		candidates[uuid] = true
	}
	for p := range rs.affectedDocsSnapshot() {
		if IsSharedPrimaryPath(p) {
			if id, ok := primaryUUIDByPath[p]; ok && id != "" {
				candidates[id] = true // 在册 primary：用 KG 权威 uuid
			} else if uuid := primaryUUIDFromPath(p); uuid != "" {
				candidates[uuid] = true // 未在册：回退 basename，保留孤儿自愈清理
			}
		}
	}
	// Enumerate filesystem remnants independently of runState. This preserves
	// retryability across real Runs after a partial md/sidecar delete failure.
	artifacts, err := listSharedPrimaryArtifacts(repoRoot)
	if err != nil {
		return err
	}
	artifactByUUID := make(map[string][]primaryArtifact)
	for _, artifact := range artifacts {
		// 同理：产物的身份也以 KG 权威映射为准，basename 仅作孤儿兜底。
		uuid := artifact.UUID
		if id, ok := primaryUUIDByPath[artifact.PrimaryRel]; ok && id != "" {
			uuid = id
		}
		if uuid == "" {
			continue
		}
		candidates[uuid] = true
		artifactByUUID[uuid] = append(artifactByUUID[uuid], artifact)
	}

	for uuid := range candidates {
		if del, ok := rs.deleted[uuid]; ok {
			// 真删：shared node 的 primary 删除（镜像文档已在文档通道重写）。
			if del.WasShared {
				primaryRel := writeback.PrimaryRelPath(del.Node.Domain, del.Node.FileSlug, uuid)
				if err := deletePrimaryArtifacts(repoRoot, del.Node.Domain, uuid, artifactByUUID[uuid]); err != nil {
					log.Printf("[incremental-writeback] 删 primary %s 失败（下轮对账重试）: %v", uuid, err)
					rs.failDoc(primaryRel)
					rs.warnf(fmt.Sprintf("I-9 删 primary %s 失败: %v", uuid, err))
					continue
				}
				rpt.PrimariesDeleted++
			}
			continue
		}
		node, err := st.nodes.GetByID(ctx, uuid)
		if err != nil {
			return fmt.Errorf("I-9 load node %s: %w", uuid, err)
		}
		if node == nil {
			// stale primary 对账（问题 5.7）：uuid 已不在 KG（上轮真删已提交但 primary
			// 清理失败/未执行）→ 删除残留 primary + sidecar，保证自愈收敛。
			domainSlug := primaryDomainFromPath(rs.affectedDocsSnapshot(), uuid)
			if domainSlug != "" || len(artifactByUUID[uuid]) > 0 {
				primaryRel := ""
				if domainSlug != "" {
					primaryRel = writeback.PrimaryRelPath(domainSlug, "", uuid) // node 已删除，无 file_slug，回退 UUID
				} else if len(artifactByUUID[uuid]) > 0 {
					primaryRel = artifactByUUID[uuid][0].PrimaryRel
				}
				primaryAbs := filepath.Join(repoRoot, filepath.FromSlash(primaryRel))
				if primaryRel != "" && (primaryArtifactsExist(primaryAbs) || len(artifactByUUID[uuid]) > 0) {
					if err := deletePrimaryArtifacts(repoRoot, domainSlug, uuid, artifactByUUID[uuid]); err != nil {
						log.Printf("[incremental-writeback] 清 stale primary %s 失败（下轮重试）: %v", uuid, err)
						rs.failDoc(primaryRel)
						rs.warnf(fmt.Sprintf("I-9 清 stale primary %s 失败: %v", uuid, err))
						continue
					}
					rpt.PrimariesDeleted++
					log.Printf("[incremental-writeback] 清理 stale primary: %s（node 已不在 KG）", uuid)
				}
			}
			continue
		}
		srcs, err := st.sources.ListByNode(ctx, uuid)
		if err != nil {
			return fmt.Errorf("I-9 list sources of %s: %w", uuid, err)
		}
		shared := len(distinctFilePaths(srcs)) >= 2
		primaryRel := writeback.PrimaryRelPath(node.Domain, node.FileSlug, uuid)
		primaryAbs := filepath.Join(repoRoot, primaryRel)
		if !shared {
			// 翻转为非 shared：primary md 或 sidecar 任一残留都必须删除。不能只看 md，
			// 否则「md 已删、sidecar 删除失败」的部分现场将永久失去重试机会。
			if primaryArtifactsExist(primaryAbs) || len(artifactByUUID[uuid]) > 0 {
				if err := deletePrimaryArtifacts(repoRoot, node.Domain, uuid, artifactByUUID[uuid]); err != nil {
					log.Printf("[incremental-writeback] 删非 shared 残留 primary %s 失败（下轮重试）: %v", uuid, err)
					rs.failDoc(primaryRel)
					rs.warnf(fmt.Sprintf("I-9 删非 shared 残留 primary %s 失败: %v", uuid, err))
					continue
				}
				rpt.PrimariesDeleted++
			}
			continue
		}
		// 当前 shared：渲染 primary（KG 权威值），字节比对后写盘。
		u := buildNodeUpdate(node, srcs, names, "")
		// A domain move or an older buggy path may leave an artifact under a
		// different _shared directory.  Keep exactly the KG-canonical path and
		// converge all stale siblings.
		var staleArtifacts []primaryArtifact
		for _, artifact := range artifactByUUID[uuid] {
			if artifact.PrimaryRel != primaryRel {
				staleArtifacts = append(staleArtifacts, artifact)
			}
		}
		if len(staleArtifacts) > 0 {
			if err := deletePrimaryArtifacts(repoRoot, "", uuid, staleArtifacts); err != nil {
				log.Printf("[incremental-writeback] 清理 stale primary %s 失败（下轮重试）: %v", uuid, err)
				rs.failDoc(primaryRel)
				rs.warnf(fmt.Sprintf("I-9 清理 stale primary %s 失败: %v", uuid, err))
			}
		}
		rendered := writeback.RenderPrimary(u)
		current, readErr := os.ReadFile(primaryAbs)
		if readErr == nil && string(current) == rendered {
			// primary 内容一致：不写 primary；但 sidecar 缺失/陈旧（如 human_curated
			// 升级后 provenance/hash 未同步）时仍要刷新——与文档通道同一自愈语义。
			if writeback.SidecarStale(repoRoot, primaryRel, rendered, []writeback.NodeUpdate{u}) {
				if err := writeback.WriteSidecarForDoc(repoRoot, primaryRel, rendered,
					[]writeback.NodeUpdate{u}, now); err != nil {
					log.Printf("[incremental-writeback] 补写 primary sidecar %s 失败（下轮重试）: %v", primaryRel, err)
					rs.failDoc(primaryRel)
					rs.warnf(fmt.Sprintf("I-9 补写 primary sidecar %s 失败: %v", primaryRel, err))
					continue
				}
				rpt.PrimariesWritten++ // sidecar 刷新也算一次成功回写
			}
			continue
		}
		if err := writeback.WritePrimaryContent(repoRoot, u, rendered, now); err != nil {
			// primary 写失败：记 failedDocs（baseline 不前移），不中断（问题 5）。
			log.Printf("[incremental-writeback] 写 primary %s 失败（下轮重试）: %v", primaryRel, err)
			rs.failDoc(primaryRel)
			rs.warnf(fmt.Sprintf("I-9 写 primary %s 失败: %v", primaryRel, err))
			continue
		}
		rpt.PrimariesWritten++
	}
	return nil
}

// primaryArtifactsExist 报告 primary md 或其 sidecar 是否仍有任一残留。
// stat 的非 NotExist 错误也按「可能存在」处理，让 DeletePrimary 返回可观测错误。
func primaryArtifactsExist(primaryAbs string) bool {
	for _, p := range []string{primaryAbs, primaryAbs + ".kg.yaml"} {
		if _, err := os.Stat(p); err == nil || !os.IsNotExist(err) {
			return true
		}
	}
	return false
}

// deletePrimaryArtifacts removes the canonical primary pair and every stale
// pair discovered for the same UUID.  All targets are attempted so a failure
// in one abnormal directory does not hide removable artifacts elsewhere; the
// aggregate error keeps the UUID in failedDocs for the next Run retry.
func deletePrimaryArtifacts(repoRoot, domainSlug, uuid string, artifacts []primaryArtifact) error {
	targets := make(map[string]bool)
	if domainSlug != "" {
		if rel := writeback.PrimaryRelPath(domainSlug, "", uuid); IsSharedPrimaryPath(rel) {
			targets[rel] = true
		}
	}
	for _, artifact := range artifacts {
		if artifact.PrimaryRel != "" && artifact.UUID == uuid {
			targets[artifact.PrimaryRel] = true
		}
	}
	var errs []error
	for rel := range targets {
		clean := filepath.ToSlash(filepath.Clean(rel))
		if clean != rel || !strings.HasPrefix(clean, "_shared/") {
			errs = append(errs, fmt.Errorf("invalid primary artifact path %q", rel))
			continue
		}
		abs := filepath.Join(repoRoot, filepath.FromSlash(rel))
		for _, p := range []string{abs, abs + ".kg.yaml"} {
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				errs = append(errs, fmt.Errorf("remove primary artifact %s: %w", p, err))
			}
		}
	}
	return errors.Join(errs...)
}

// primaryDomainFromPath 在 affectedDocs 中找 uuid 对应的 _shared primary 路径，
// 反解其 domain-slug 段（stale primary 对账用——node 已删时 KG 查不到 domain）。
// primaryDomainFromPath finds a uuid's _shared primary path in affectedDocs and
// extracts its domain-slug segment (used by stale-primary reconciliation).
func primaryDomainFromPath(affectedDocs map[string]bool, uuid string) string {
	suffix := "/" + uuid + ".md"
	for p := range affectedDocs {
		if IsSharedPrimaryPath(p) && strings.HasSuffix(p, suffix) {
			rest := strings.TrimPrefix(p, "_shared/")
			if idx := strings.Index(rest, "/"); idx > 0 {
				return rest[:idx]
			}
		}
	}
	return ""
}

// layerDisplayNames 是 domain/subdomain slug → 展示名（中文名）的缓存。
// layerDisplayNames caches slug → display name for the ownership line.
type layerDisplayNames struct {
	domains    map[string]string       // domain slug → 中文名
	subdomains map[SubdomainKey]string // (domain, subdomain) slug → 中文名
}

// primaryUUIDFromPath 从 _shared/<domain-slug>/<uuid>.md 路径反解 node uuid。
// primaryUUIDFromPath extracts the node uuid from a _shared primary path.
func primaryUUIDFromPath(relPath string) string {
	if !IsSharedPrimaryPath(relPath) {
		return ""
	}
	return strings.TrimSuffix(filepath.Base(relPath), ".md")
}

// loadLayerDisplayNames 从层节点（Domain/Subdomain label）构建展示名缓存。
// loadLayerDisplayNames builds the display-name cache from layer nodes.
func loadLayerDisplayNames(ctx context.Context, st *stores) (*layerDisplayNames, error) {
	all, err := st.nodes.ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("I-9 load layer nodes: %w", err)
	}
	n := &layerDisplayNames{
		domains:    make(map[string]string),
		subdomains: make(map[SubdomainKey]string),
	}
	for _, node := range all {
		switch node.Label {
		case dktypes.LabelDomain:
			n.domains[node.Domain] = node.Name
		case dktypes.LabelSubdomain:
			n.subdomains[SubdomainKey{Domain: node.Domain, Subdomain: node.Subdomain}] = node.Name
		}
	}
	return n, nil
}

// buildDocNodeUpdates 构建一篇文档应包含的全部 node 的回写视图（KG 权威值）：
// 集合 = node_sources 中 file_path=docPath 的 distinct node；shared 按当前来源数现算；
// span = 该 node 在本文档的最小 start_line（块排序用）。
//
// buildDocNodeUpdates builds the KG-authoritative node views for one doc:
// membership comes from node_sources, shared is recomputed live, and the span
// is the node's min start_line in this doc.
func buildDocNodeUpdates(ctx context.Context, st *stores, docPath string, names *layerDisplayNames) ([]writeback.NodeUpdate, error) {
	rows, err := st.sources.ListByFile(ctx, docPath)
	if err != nil {
		return nil, err
	}
	// distinct uuid（稳定序）。
	uuidSet := make(map[string]bool)
	var uuids []string
	for _, r := range rows {
		if !uuidSet[r.NodeUUID] {
			uuidSet[r.NodeUUID] = true
			uuids = append(uuids, r.NodeUUID)
		}
	}
	sort.Strings(uuids)

	// 该文档内每个 uuid 的最小 start_line（span 排序）。
	spanByUUID := make(map[string][2]int)
	for _, r := range rows {
		if r.StartLine <= 0 {
			continue
		}
		cur, ok := spanByUUID[r.NodeUUID]
		if !ok || r.StartLine < cur[0] {
			spanByUUID[r.NodeUUID] = [2]int{r.StartLine, r.EndLine}
		}
	}

	nodeMap, err := st.nodes.GetByIDs(ctx, uuids)
	if err != nil {
		return nil, err
	}
	var updates []writeback.NodeUpdate
	for _, uuid := range uuids {
		node := nodeMap[uuid]
		if node == nil {
			continue // 防御：node_sources 残留行指向已删 node（I-9 跳过即清理）
		}
		srcs, err := st.sources.ListByNode(ctx, uuid)
		if err != nil {
			return nil, err
		}
		u := buildNodeUpdate(node, srcs, names, docPath)
		if sp, ok := spanByUUID[uuid]; ok {
			u.SpanStart, u.SpanEnd = sp[0], sp[1]
		}
		updates = append(updates, u)
	}
	return updates, nil
}

// buildNodeUpdate 把 KG node + 来源行装配为 writeback.NodeUpdate（KG 权威值）。
// docPath 仅用于日志区分（视图与文档无关的部分一致）。
// buildNodeUpdate assembles a KG-authoritative NodeUpdate from node + sources.
func buildNodeUpdate(node *dktypes.Node, srcs []storage.NodeSource, names *layerDisplayNames, docPath string) writeback.NodeUpdate {
	files := distinctFilePaths(srcs)
	// members = distinct member_id（稳定排序；node_sources 是 members 的权威存储）。
	memberSet := make(map[string]bool)
	var members []string
	for _, s := range srcs {
		if s.MemberID != "" && !memberSet[s.MemberID] {
			memberSet[s.MemberID] = true
			members = append(members, s.MemberID)
		}
	}
	sort.Strings(members)

	domainName := names.domains[node.Domain]
	if domainName == "" {
		domainName = node.Domain // 防御：层节点缺失时以 slug 兜底展示
	}
	subName := names.subdomains[SubdomainKey{Domain: node.Domain, Subdomain: node.Subdomain}]
	if subName == "" {
		subName = node.Subdomain
	}
	u := writeback.NodeUpdate{
		UUID: node.ID, FileSlug: node.FileSlug, Tag: string(node.Label), Name: node.Name,
		Domain: domainName, Subdomain: subName,
		DomainSlug: node.Domain, SubdomainSlug: node.Subdomain,
		Summary: node.Summary, Description: node.Description,
		Members: members, Shared: len(files) >= 2, SourceFiles: files,
		Provenance: string(node.Provenance),
	}
	// The full writer stores the first available source span in every view,
	// including shared primaries.  Keep the same deterministic ledger-derived
	// value for incremental writes; document-local callers may override it with
	// their span below.
	for _, source := range srcs {
		if source.StartLine > 0 {
			u.SpanStart = source.StartLine
			u.SpanEnd = source.EndLine
			break
		}
	}
	return u
}
