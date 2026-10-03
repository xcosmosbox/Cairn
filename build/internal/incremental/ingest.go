// Package incremental 的本文件实现 I-8 增量入库（局部 upsert，非全量重建）：
//
//   - 新建 domain/subdomain 层节点（含 provides/composes 边，端点存在性校验，R5）；
//   - 新建 entity/concept node（Insert）与融入/重融合的 node（Update）；
//   - node_sources 同步增删（新增来源行 InsertBatch；删除行已在旁路 D 即时删）；
//   - 脏子域语义边「先删后插」（DeleteSemanticBySubdomain——只删 entity/concept
//     源节点的语义边，保留层节点 composes 层级边——再端点校验重插）；
//   - FTSIndex.RebuildForSubdomain 仅重建脏子域（绝不 RebuildAll，R9）；
//   - KBVersionRepo.Bump。
//
// 铁律：绝不 os.Remove(db)（局部 upsert）；绝不产生悬空边（insertEdge 端点校验）；
// 未涉及对象零改动（R9 脏集封闭）。
//
// This file implements I-8 incremental ingest (local upsert — never a full
// rebuild): layer-node materialization, node insert/update, node_sources sync,
// delete-then-reinsert of dirty subdomains' semantic edges (hierarchy edges
// preserved), per-subdomain FTS rebuilds, and a version bump.
package incremental

import (
	"context"
	"fmt"
	"github.com/xcosmosbox/cairn/build/internal/controller/store"
	"log"
	"strings"
	"time"

	"github.com/xcosmosbox/cairn/core/dktypes"
)

// applyIncrementalIngest 执行 I-8 局部 upsert。依赖顺序：先层节点与 entity/concept
// 节点（Insert/Update）→ 再语义边（端点已就绪）→ node_sources → FTS → Bump。
// ao 为 nil（无管道产出）时只做脏子域边重插与 FTS 重建。
//
// applyIncrementalIngest runs I-8 local upsert in dependency order.
func applyIncrementalIngest(ctx context.Context, st *stores, dirty *DirtySet,
	ao *alignOutcome, rr *reflowResult, rs *runState) error {
	if err := store.CheckLease(ctx); err != nil {
		return err
	}
	if err := normalizePendingForPersistence(ao); err != nil {
		return err
	}
	now := time.Now().UTC()

	// —— 1. 新建 domain/subdomain 层节点（含 provides/composes 边）——
	if ao != nil {
		if err := materializeNewLayers(ctx, st, ao, now, rs); err != nil {
			return err
		}
	}

	// —— 2. entity/concept 节点：新建 Insert；融入/重融合 Update ——
	if ao != nil {
		for _, uuid := range sortedPendingKeys(ao) {
			pn := ao.pending[uuid]
			if pn.IsNew {
				var desc string
				if rr != nil {
					desc = rr.descriptions[uuid]
				}
				if desc == "" {
					desc = pn.Summary // I-6 失败的兜底（ingest 侧 summary 兜底风格一致）
				}
				desc = normalizePersistedSummary(desc)
				node := &dktypes.Node{
					ID: uuid, Label: dktypes.Label(pn.Label), Name: pn.Name,
					Summary: firstNonEmptyStr(pn.Summary, pn.Name), Description: desc,
					Domain: pn.Domain, Subdomain: pn.Subdomain,
					Confidence: pn.Confidence, Provenance: dktypes.ProvenanceLLMInferred,
					SourceRefs: strings.Join(pn.SourceSkills, ","),
					CreatedAt:  now, UpdatedAt: now,
				}
				// 可重入（问题 5）：node 可能已由上一轮（中途失败的那次）插入——
				// 存在则跳过 Insert，继续幂等补齐边与来源行。
				exists, err := st.nodes.GetByID(ctx, uuid)
				if err != nil {
					return fmt.Errorf("I-8 check node %s: %w", uuid, err)
				}
				if exists == nil {
					if err := st.nodes.Insert(ctx, node); err != nil {
						return fmt.Errorf("I-8 insert node %s: %w", uuid, err)
					}
				}
			} else if rr != nil {
				// No I-6 description is a normal no-op for an unchanged node.
				// Keep the branch explicit so a nil reflow result cannot panic.
				if desc, ok := rr.descriptions[uuid]; ok {
					desc = normalizePersistedSummary(desc)
					// 融入 node：description 被 I-6 重融合 → Update（uuid 钉死，R2）。
					node, err := st.nodes.GetByID(ctx, uuid)
					if err != nil {
						return fmt.Errorf("I-8 load node %s: %w", uuid, err)
					}
					if node == nil {
						rs.warnf(fmt.Sprintf("I-8：融入目标 node %s 不存在（跳过 description 更新）", uuid))
						continue
					}
					node.Description = desc
					node.UpdatedAt = now
					if err := st.nodes.Update(ctx, node); err != nil {
						return fmt.Errorf("I-8 update node %s: %w", uuid, err)
					}
				}
			}

			// ownership 层级边对全部 pending 都做幂等确认，而非仅 IsNew：
			// 首轮可能已插入 node、随后 composes 失败；下一轮重新 align 会因 UUID
			// 已存在而转为 IsNew=false，仍必须补齐 subdomain→node 才能收敛。
			node, err := st.nodes.GetByID(ctx, uuid)
			if err != nil {
				return fmt.Errorf("I-8 load ownership node %s: %w", uuid, err)
			}
			if node == nil {
				rs.warnf(fmt.Sprintf("I-8：pending node %s 不存在（跳过 ownership 边）", uuid))
				continue
			}
			if node.Domain == "" || node.Subdomain == "" {
				rs.warnf(fmt.Sprintf("I-8：pending node %s 缺 domain/subdomain（跳过 ownership 边）", uuid))
				continue
			}
			if err := insertCheckedEdge(ctx, st,
				subdomainNodeID(node.Domain, node.Subdomain), uuid, dktypes.KindComposes,
				"子域包含节点", 1.0, now, rs); err != nil {
				return err
			}
		}
		// —— 3. node_sources：新来源行批量写入（INSERT OR REPLACE 幂等）——
		if len(ao.sourceRows) > 0 {
			// schema 为兼容历史库未声明 FK；I-8 必须在应用层拒绝孤儿来源行。
			// 正常路径中所有 pending node 已在上方存在/插入，此检查也能把异常
			// alignOutcome 转成可重试错误，而不是污染 node_sources 权威账本。
			for _, row := range ao.sourceRows {
				node, err := st.nodes.GetByID(ctx, row.NodeUUID)
				if err != nil {
					return fmt.Errorf("I-8 validate node_source %s/%s: %w", row.NodeUUID, row.MemberID, err)
				}
				if node == nil {
					return fmt.Errorf("I-8 refuse orphan node_source: node %s not found (member %s)", row.NodeUUID, row.MemberID)
				}
			}
			if err := st.sources.InsertBatch(ctx, ao.sourceRows); err != nil {
				return fmt.Errorf("I-8 insert node_sources: %w", err)
			}
		}
	}

	// —— 3.5 FTS 残留清理（safety 4）：真删 node 的 rowid 已从 nodes 消失，
	// RebuildForSubdomain 的子查询找不到它们，必须显式 DeleteByRowID。
	if len(rs.deleted) > 0 {
		var rowids []int64
		for _, del := range rs.deleted {
			if del.RowID > 0 {
				rowids = append(rowids, del.RowID)
			}
		}
		if err := st.fts.DeleteByRowID(ctx, rowids); err != nil {
			return fmt.Errorf("I-8 fts delete orphans: %w", err)
		}
	}

	// —— 4. 脏子域语义边：按子域在 storage 层单事务替换 ——
	for _, key := range dirty.SortedSubdomains() {
		// map 缺 key 表示 I-7 因读取/LLM 等失败未形成权威结果；它与
		// “成功判定最终集合为空”（key 存在、slice=nil）语义不同。前者必须
		// fail-closed 保留现有边，绝不能先删后无物可插。
		if rr == nil {
			rs.warnf(fmt.Sprintf("I-8：子域 %s 无 I-7 结果（保留现有语义边）", key))
			continue
		}
		relations, completed := rr.relations[key]
		if !completed {
			rs.warnf(fmt.Sprintf("I-8：子域 %s 的 I-7 结果缺失（保留现有语义边）", key))
			continue
		}
		// Build the complete replacement set first.  The storage operation below
		// validates endpoints and performs delete/update/insert under one SQL
		// transaction, so a failure halfway through cannot publish a partial set.
		// For an identity already in the old internal set, storage treats only the
		// LLM-schema fields description/confidence as updates and preserves the
		// complete non-LLM metadata plus id/created_at from the old Edge snapshot.
		var replacement []*dktypes.Edge
		for _, rel := range relations {
			conf := rel.Confidence
			if conf <= 0 || conf > 1 {
				conf = 0.8
			}
			replacement = append(replacement, &dktypes.Edge{
				SourceID: rel.Source, TargetID: rel.Target,
				Kind: dktypes.RelationKind(rel.Kind), Description: rel.Description,
				Confidence: conf, Provenance: dktypes.ProvenanceLLMInferred,
				CreatedAt: now,
			})
		}
		for _, e := range rr.keptCross[key] {
			// keptCross 的契约是「原样保留」：不能经简化参数封装丢掉
			// properties/provenance/source_refs/bidirectional/cardinality。
			copyEdge := *e
			if copyEdge.CreatedAt.IsZero() {
				copyEdge.CreatedAt = now
			}
			replacement = append(replacement, &copyEdge)
		}
		if err := st.edges.ReplaceSemanticBySubdomain(ctx, key.Domain, key.Subdomain, replacement); err != nil {
			return fmt.Errorf("I-8 replace semantic edges %s: %w", key, err)
		}
	}

	// —— 4.5 provenance 同步（问题 9）：以 node_sources 为权威，
	// 重算受影响 node 的 source_refs，并精确同步受影响 domain 的
	// skill→domain provides（有贡献保证存在、无贡献才删除、不误删）。
	if err := syncProvenance(ctx, st, ao, rs, now); err != nil {
		return err
	}

	// —— 5. FTS：仅重建脏子域（绝不 RebuildAll，R9）——
	for _, key := range dirty.SortedSubdomains() {
		if err := st.fts.RebuildForSubdomain(ctx, key.Domain, key.Subdomain); err != nil {
			return fmt.Errorf("I-8 fts rebuild %s: %w", key, err)
		}
	}
	// C1 内容变的 node 所在子域（不脏 relation 但索引过期）。
	for key := range dirty.FTSSubdomains {
		if dirty.Subdomains[key] {
			continue // 已重建
		}
		if err := st.fts.RebuildForSubdomain(ctx, key.Domain, key.Subdomain); err != nil {
			return fmt.Errorf("I-8 fts rebuild %s: %w", key, err)
		}
	}

	// —— 6. 版本 Bump ——
	if _, err := st.version.Bump(ctx); err != nil {
		return fmt.Errorf("I-8 bump version: %w", err)
	}
	return nil
}

// normalizePendingForPersistence is the final pre-write guard for LLM-derived
// identity fields. Alignment already applies the same rules, but I-8 can also be
// called directly during retries and tests; scan the complete batch before any
// layer/node/source write so one malformed pending value cannot partially land.
func normalizePendingForPersistence(ao *alignOutcome) error {
	if ao == nil {
		return nil
	}
	for _, uuid := range sortedPendingKeys(ao) {
		pn := ao.pending[uuid]
		if pn == nil {
			return fmt.Errorf("I-8 invalid pending node %s: nil payload", uuid)
		}
		if pn.IsNew {
			name, ok := normalizePersistedName(pn.Name)
			if !ok {
				return fmt.Errorf("I-8 invalid pending node %s: unsafe name", uuid)
			}
			pn.Name = name
			pn.Summary = normalizePersistedSummary(pn.Summary)
		}
		if pn.DomainName != "" {
			name, ok := normalizePersistedName(pn.DomainName)
			if !ok {
				return fmt.Errorf("I-8 invalid pending node %s: unsafe domain display name", uuid)
			}
			pn.DomainName = name
		} else if pn.NewDomain {
			return fmt.Errorf("I-8 invalid pending node %s: missing domain display name", uuid)
		}
		if pn.SubdomainName != "" {
			name, ok := normalizePersistedName(pn.SubdomainName)
			if !ok {
				return fmt.Errorf("I-8 invalid pending node %s: unsafe subdomain display name", uuid)
			}
			pn.SubdomainName = name
		} else if pn.NewSubdomain {
			return fmt.Errorf("I-8 invalid pending node %s: missing subdomain display name", uuid)
		}
	}
	return nil
}

// materializeNewLayers 为全部 pending 幂等确认其 domain/subdomain 层节点与层级边。
// 新 node 正常物化；已有/碰撞 node 通常只命中 exists 分支，但这样也能修复
// 「首轮 node 已落、后续 composes 失败，重试变 IsNew=false」的半完成现场。
// materializeNewLayers idempotently ensures ownership layers for every pending
// node, including retry/collision plans already marked IsNew=false.
func materializeNewLayers(ctx context.Context, st *stores, ao *alignOutcome, now time.Time, rs *runState) error {
	doneDomain := make(map[string]bool)
	doneSub := make(map[SubdomainKey]bool)
	for _, uuid := range sortedPendingKeys(ao) {
		pn := ao.pending[uuid]
		if pn.Domain == "" || pn.Subdomain == "" {
			rs.warnf(fmt.Sprintf("I-8：pending node %s 缺 domain/subdomain（跳过层物化）", uuid))
			continue
		}
		// —— domain 层节点（新建 domain 或防御性缺失补齐）——
		if !doneDomain[pn.Domain] {
			exists, err := st.nodes.GetByID(ctx, domainNodeID(pn.Domain))
			if err != nil {
				return fmt.Errorf("I-8 check domain %s: %w", pn.Domain, err)
			}
			if exists == nil {
				name := firstNonEmptyStr(pn.DomainName, pn.Domain)
				var ok bool
				if name, ok = normalizePersistedName(name); !ok {
					return fmt.Errorf("I-8 invalid domain %s display name", pn.Domain)
				}
				if err := st.nodes.Insert(ctx, &dktypes.Node{
					ID: domainNodeID(pn.Domain), Label: dktypes.LabelDomain,
					Name: name, Summary: name, Domain: pn.Domain, Subdomain: "",
					SourceRefs: strings.Join(pn.SourceSkills, ","),
					Confidence: 1, Provenance: dktypes.ProvenanceLLMInferred,
					CreatedAt: now, UpdatedAt: now,
				}); err != nil {
					return fmt.Errorf("I-8 insert domain %s: %w", pn.Domain, err)
				}
			}
			// provides 边：skill --provides--> domain（skill 节点缺失时兜底补建，
			// 保证端点存在——与全量 ingest 的兜底语义一致）。
			for _, skill := range pn.SourceSkills {
				if err := ensureSkillNode(ctx, st, skill, now); err != nil {
					return err
				}
				if err := insertCheckedEdge(ctx, st, skillNodeID(skill), domainNodeID(pn.Domain),
					dktypes.KindProvides, fmt.Sprintf("skill %s 贡献了领域 %s", skill, pn.Domain), 1.0, now, rs); err != nil {
					return err
				}
			}
			doneDomain[pn.Domain] = true
		}
		// —— subdomain 层节点 ——
		key := SubdomainKey{Domain: pn.Domain, Subdomain: pn.Subdomain}
		if !doneSub[key] {
			exists, err := st.nodes.GetByID(ctx, subdomainNodeID(pn.Domain, pn.Subdomain))
			if err != nil {
				return fmt.Errorf("I-8 check subdomain %s: %w", key, err)
			}
			if exists == nil {
				name := firstNonEmptyStr(pn.SubdomainName, pn.Subdomain)
				var ok bool
				if name, ok = normalizePersistedName(name); !ok {
					return fmt.Errorf("I-8 invalid subdomain %s display name", key)
				}
				if err := st.nodes.Insert(ctx, &dktypes.Node{
					ID: subdomainNodeID(pn.Domain, pn.Subdomain), Label: dktypes.LabelSubdomain,
					Name: name, Summary: name, Domain: pn.Domain, Subdomain: pn.Subdomain,
					Confidence: 1, Provenance: dktypes.ProvenanceLLMInferred,
					CreatedAt: now, UpdatedAt: now,
				}); err != nil {
					return fmt.Errorf("I-8 insert subdomain %s: %w", key, err)
				}
			}
			// composes 边：domain --composes--> subdomain。
			if err := insertCheckedEdge(ctx, st, domainNodeID(pn.Domain),
				subdomainNodeID(pn.Domain, pn.Subdomain), dktypes.KindComposes,
				"领域包含子域", 1.0, now, rs); err != nil {
				return err
			}
			doneSub[key] = true
		}
	}
	return nil
}

// ensureSkillNode 保证 skill 节点存在（缺失时以 skill 名为摘要兜底补建，
// 与全量 ingest 的兜底语义一致——provides 边端点必须存在，R5）。
// ensureSkillNode materializes a skill node if missing (full-ingest fallback
// semantics, so provides edges never dangle).
func ensureSkillNode(ctx context.Context, st *stores, skill string, now time.Time) error {
	if skill == "" {
		return nil
	}
	exists, err := st.nodes.GetByID(ctx, skillNodeID(skill))
	if err != nil {
		return fmt.Errorf("I-8 check skill %s: %w", skill, err)
	}
	if exists != nil {
		return nil
	}
	return st.nodes.Insert(ctx, &dktypes.Node{
		ID: skillNodeID(skill), Label: dktypes.LabelSkill,
		Name: skill, Summary: skill, Domain: skill, Subdomain: "",
		Confidence: 1, Provenance: dktypes.ProvenanceLLMInferred,
		CreatedAt: now, UpdatedAt: now,
	})
}

// ——————————————————————————————————————————————————————————————————————————————
// provenance 同步（问题 9：node_sources 为权威）
// ——————————————————————————————————————————————————————————————————————————————

// syncProvenance 以 node_sources 为来源权威，精确同步受影响 node 的 source_refs
// 与受影响 domain 的 skill→domain provides（问题 9）：
//
//   - 受影响 node（touched：C1/C2 存活；pending：融入/新建）：重算 distinct skills
//     并更新 source_refs（值不变则不写——R9 最小改动、重放幂等）；
//   - 受影响 domain（上述 node 的 domain ∪ 真删 node 的 domain）：重算当前贡献
//     skills（DistinctSkillsByDomain，已删 node 自然排除）→ 有贡献的 skill 保证
//     skill 层节点 + provides 边存在（新 skill 融入已有 node 也覆盖）；无贡献的
//     现存 provides 边删除（不误删仍贡献的）；domain 节点自身 source_refs 同步。
//
// syncProvenance recomputes affected nodes' source_refs and precisely syncs
// skill→domain provides edges for affected domains, with node_sources as the
// source of truth.
func syncProvenance(ctx context.Context, st *stores, ao *alignOutcome, rs *runState, now time.Time) error {
	// 受影响 node 集合：touched（C1/C2 存活）∪ pending（融入/新建）。
	nodeSet := make(map[string]bool)
	for uuid := range rs.touched {
		nodeSet[uuid] = true
	}
	if ao != nil {
		for uuid := range ao.pending {
			nodeSet[uuid] = true
		}
	}
	domainSet := make(map[string]bool)

	// —— 1. node source_refs ——
	for uuid := range nodeSet {
		node, err := st.nodes.GetByID(ctx, uuid)
		if err != nil {
			return fmt.Errorf("I-8 sync provenance load %s: %w", uuid, err)
		}
		if node == nil {
			continue // 已删（真删路径）→ domain 仍需同步（下方记录）
		}
		domainSet[node.Domain] = true
		skills, err := st.sources.DistinctSkillsByNode(ctx, uuid)
		if err != nil {
			return err
		}
		refs := strings.Join(skills, ",")
		if node.SourceRefs != refs {
			node.SourceRefs = refs
			node.UpdatedAt = now
			if err := st.nodes.Update(ctx, node); err != nil {
				return fmt.Errorf("I-8 sync source_refs %s: %w", uuid, err)
			}
		}
	}
	// 真删 node 的 domain 也要同步（其贡献可能随删除消失）。
	for _, del := range rs.deleted {
		domainSet[del.Node.Domain] = true
	}

	// —— 2. domain provides + domain source_refs ——
	for domain := range domainSet {
		if domain == "" {
			continue
		}
		contributing, err := st.sources.DistinctSkillsByDomain(ctx, domain)
		if err != nil {
			return err
		}
		contributingSet := make(map[string]bool, len(contributing))
		for _, s := range contributing {
			contributingSet[s] = true
		}

		// domain 节点 source_refs 同步为当前贡献 skills（值不变不写）。
		domainNode, err := st.nodes.GetByID(ctx, domainNodeID(domain))
		if err != nil {
			return fmt.Errorf("I-8 sync domain %s: %w", domain, err)
		}
		domainName := domain
		if domainNode != nil {
			domainName = firstNonEmptyStr(domainNode.Name, domain)
			refs := strings.Join(contributing, ",")
			if domainNode.SourceRefs != refs {
				domainNode.SourceRefs = refs
				domainNode.UpdatedAt = now
				if err := st.nodes.Update(ctx, domainNode); err != nil {
					return fmt.Errorf("I-8 sync domain source_refs %s: %w", domain, err)
				}
			}
		}

		// 有贡献的 skill：保证层节点 + provides 边存在（幂等）。
		for _, skill := range contributing {
			if err := ensureSkillNode(ctx, st, skill, now); err != nil {
				return err
			}
			if err := insertCheckedEdge(ctx, st, skillNodeID(skill), domainNodeID(domain),
				dktypes.KindProvides, fmt.Sprintf("skill %s 贡献了领域 %s", skill, domainName),
				1.0, now, rs); err != nil {
				return err
			}
		}

		// 无贡献的现存 provides 边：删除（不误删仍贡献的）。
		incoming, err := st.edges.GetIncoming(ctx, domainNodeID(domain), 0)
		if err != nil {
			return fmt.Errorf("I-8 list provides of %s: %w", domain, err)
		}
		for _, e := range incoming {
			if e.Kind != dktypes.KindProvides {
				continue
			}
			skill := strings.TrimPrefix(e.SourceID, "skill::")
			if !contributingSet[skill] {
				if _, err := st.edges.DeleteBetween(ctx, e.SourceID, domainNodeID(domain), dktypes.KindProvides); err != nil {
					return fmt.Errorf("I-8 remove stale provides %s→%s: %w", e.SourceID, domain, err)
				}
				log.Printf("[incremental-ingest] 删除失效 provides: %s → %s（该 skill 已不再贡献）", e.SourceID, domain)
			}
		}
	}
	return nil
}

// insertCheckedEdge 是常规边入库的简化封装：构造本轮权威边后交给
// upsertCheckedEdge 做应用层外键与身份 upsert。
// 端点缺失 → 告警跳过（绝不产生悬空边，R5），不返回错误；
// 同身份边已存在 → 更新权威字段并收敛重复行（问题 7：重放幂等且不吞新值）。
func insertCheckedEdge(ctx context.Context, st *stores, sourceID, targetID string,
	kind dktypes.RelationKind, desc string, conf float64, now time.Time, rs *runState) error {
	if conf <= 0 || conf > 1 {
		conf = 0.8
	}
	return upsertCheckedEdge(ctx, st, &dktypes.Edge{
		SourceID: sourceID, TargetID: targetID, Kind: kind,
		Description: desc, Confidence: conf, Provenance: dktypes.ProvenanceLLMInferred,
		CreatedAt: now,
	}, rs)
}

// upsertCheckedEdge 保留完整 Edge 权威字段并统一执行端点校验 + 身份 upsert。
// 主要供 keptCross 原样重插；常规关系也由 insertCheckedEdge 委托到这里。
// upsertCheckedEdge preserves every authoritative edge field while enforcing
// endpoint existence and identity-idempotent storage.
func upsertCheckedEdge(ctx context.Context, st *stores, edge *dktypes.Edge, rs *runState) error {
	sourceID, targetID, kind := edge.SourceID, edge.TargetID, edge.Kind
	src, err := st.nodes.GetByID(ctx, sourceID)
	if err != nil {
		return fmt.Errorf("I-8 check edge source %s: %w", sourceID, err)
	}
	tgt, err := st.nodes.GetByID(ctx, targetID)
	if err != nil {
		return fmt.Errorf("I-8 check edge target %s: %w", targetID, err)
	}
	if src == nil || tgt == nil {
		rs.warnf(fmt.Sprintf("I-8 跳过悬空边: %s --%s--> %s（端点缺失）", sourceID, kind, targetID))
		return nil
	}
	if edge.Confidence < 0 || edge.Confidence > 1 {
		edge.Confidence = 0.8
	}
	if !edge.Provenance.IsValid() {
		edge.Provenance = dktypes.ProvenanceLLMInferred
	}
	_, err = st.edges.InsertIfAbsent(ctx, edge)
	if err != nil {
		return fmt.Errorf("I-8 upsert edge %s --%s--> %s: %w", sourceID, kind, targetID, err)
	}
	return nil
}

// ——————————————————————————————————————————————————————————————————————————————
// 层节点 ID 构造（与 ingest 包同规则：类型前缀 ID，段永不为空）
// ——————————————————————————————————————————————————————————————————————————————

// skillNodeID 构造 skill 节点 ID（与 ingest 同规则："skill::<skill>"）。
func skillNodeID(skill string) string { return "skill::" + skill }

// domainNodeID 构造 domain 节点 ID（与 ingest 同规则："domain::<slug>"）。
func domainNodeID(domainSlug string) string { return "domain::" + domainSlug }

// subdomainNodeID 构造 subdomain 节点 ID（与 ingest 同规则："subdomain::<dSlug>::<sSlug>"）。
func subdomainNodeID(domainSlug, subSlug string) string {
	return "subdomain::" + domainSlug + "::" + subSlug
}

// sortedPendingKeys 返回 pending map 的稳定序 uuid 列表（输出确定性）。
// sortedPendingKeys returns pending uuids in stable order.
func sortedPendingKeys(ao *alignOutcome) []string {
	out := make([]string, 0, len(ao.pending))
	for k := range ao.pending {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}

// logIngestStats 输出 I-8 统计（可观测）。
//
// 除管道产出（新建/融入）外，必须一并暴露旁路触及的 node 数：C1 的人工编辑是在
// 旁路阶段直接写入 KG 的，不经过 ao.pending。若只报「新建 0, 融入 0」，一次成功
// 的人工编辑与一次被静默丢弃的编辑在日志上完全相同。
//
// logIngestStats logs I-8 stats, including bypass-touched nodes (C1 edits are
// written in the bypass stage and never appear in ao.pending).
func logIngestStats(dirty *DirtySet, ao *alignOutcome, rs *runState) {
	var news, merges int
	if ao != nil {
		for _, pn := range ao.pending {
			if pn.IsNew {
				news++
			} else {
				merges++
			}
		}
	}
	var touched int
	if rs != nil {
		touched = len(rs.touched)
	}
	log.Printf("[incremental-ingest] 局部 upsert: 新建 node %d, 融入 %d, 旁路触及 node %d, 脏子域 %d, FTS 重建子域 %d",
		news, merges, touched, len(dirty.Subdomains), len(dirty.FTSSubdomains))
}
