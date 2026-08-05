// Package extract 的本文件实现「融合映射校准」（重构第二期 B 阶段的确定性校验部分）。
//
// 提取阶段输出的是「融合操作」：每个融合节点用 members 声明它由哪些 04 输入单元融合而成。
// 这带来两类必须校准的正确性问题（确定性、纯代码）：
//   - 非法 members 引用：members 引用了输入中并不存在的 04 id（LLM 编造 / 打错）——剔除；
//   - 覆盖度：某些 04 输入单元没有被任何融合节点的 members 覆盖（漏融合）——统计上报。
//
// 语义关系的悬空端点校验仍复用 repair.go 的 scanDangling/cleanDangling（relations 端点
// 是融合节点 id）。本文件只做「members ↔ 04 输入」这一侧的确定性校准，LLM 侧的悬空
// 修正由 repair.go 编排。
//
// This file implements deterministic fusion-mapping calibration (phase 2 part B):
// dropping members that reference non-existent 04 ids, and reporting 04 units left
// uncovered by any node's members. Relation-endpoint dangling checks stay in repair.go.
package extract

import (
	"github.com/xcosmosbox/cairn/core/dktypes"
)

// FusionReport 是融合映射校准的结果摘要（可观测，随 RepairReport 一并写盘）。
// FusionReport summarizes fusion-mapping calibration (observability).
type FusionReport struct {
	InputUnits            int      // 04 输入单元总数（按 id 去重）
	CoveredUnits          int      // 被至少一个融合节点 members 覆盖的 04 id 数
	UncoveredIDs          []string // 未被任何融合节点覆盖的 04 id（漏融合，可观测）
	InvalidMembersRemoved int      // 被剔除的非法 member 引用数（引用了不存在的 04 id）
}

// collectUnitIDs 收集 04 输入单元的 id 集合（已由 AssignIDsAndMerge 刷入）。
// collectUnitIDs collects the set of 04 input unit ids.
func collectUnitIDs(docs []*dktypes.AnnotatedDocument) map[string]bool {
	ids := make(map[string]bool)
	for _, d := range docs {
		if d == nil {
			continue
		}
		for _, it := range d.Items {
			if it.ID != "" {
				ids[it.ID] = true
			}
		}
	}
	return ids
}

// buildCovered 返回被任一融合节点 members 覆盖、且确实存在于 unitIDs 的 04 id 集合。
// buildCovered returns the set of existing 04 ids covered by any node's members.
func buildCovered(domains []Domain, unitIDs map[string]bool) map[string]bool {
	covered := make(map[string]bool)
	for di := range domains {
		d := &domains[di]
		for si := range d.Subdomains {
			sd := &d.Subdomains[si]
			for _, n := range sd.Entities {
				for _, m := range n.Members {
					if unitIDs[m] {
						covered[m] = true
					}
				}
			}
			for _, n := range sd.Concepts {
				for _, m := range n.Members {
					if unitIDs[m] {
						covered[m] = true
					}
				}
			}
		}
	}
	return covered
}

// CleanFusionMapping 就地清洗融合节点的 members：
//   - 剔除引用了不存在 04 id 的项（LLM 编造 / 拼错 slug）；
//   - 去除同一节点内重复出现的 member（防御 LLM 重复列举）。
//
// 返回被剔除的「非法 member id」去重列表（诊断用；len 即剔除总数）。
//
// CleanFusionMapping drops members referencing non-existent 04 ids and de-duplicates
// members within a node, returning the de-duplicated list of invalid member ids removed.
func CleanFusionMapping(domains []Domain, unitIDs map[string]bool) []string {
	removedSeen := make(map[string]bool)
	var removedIDs []string
	clean := func(members []string) []string {
		if len(members) == 0 {
			return members
		}
		kept := make([]string, 0, len(members))
		inNode := make(map[string]bool, len(members))
		for _, m := range members {
			if !unitIDs[m] {
				if !removedSeen[m] {
					removedSeen[m] = true
					removedIDs = append(removedIDs, m)
				}
				continue
			}
			if inNode[m] { // 同节点内重复 member → 去重（不计入非法）
				continue
			}
			inNode[m] = true
			kept = append(kept, m)
		}
		return kept
	}
	for di := range domains {
		d := &domains[di]
		for si := range d.Subdomains {
			sd := &d.Subdomains[si]
			for ni := range sd.Entities {
				sd.Entities[ni].Members = clean(sd.Entities[ni].Members)
			}
			for ni := range sd.Concepts {
				sd.Concepts[ni].Members = clean(sd.Concepts[ni].Members)
			}
		}
	}
	return removedIDs
}

// DedupFusionNodes 就地合并同一 subdomain 内 id 重复的融合节点（LLM 偶尔输出重复节点，
// 如同一 subdomain 出现两个相同 id 的 "metric-definition"）。合并规则：members 取并集、
// confidence 取最高、synonyms 取并集，其余字段保留首见。分别对 entities / concepts 去重。
// 返回被合并掉的重复节点总数。
//
// DedupFusionNodes merges fusion nodes sharing the same id within a subdomain (LLM
// sometimes emits duplicates), unioning members/synonyms and taking the max confidence.
func DedupFusionNodes(domains []Domain) int {
	merged := 0
	dedup := func(nodes []Node) ([]Node, int) {
		if len(nodes) <= 1 {
			return nodes, 0
		}
		idxByID := make(map[string]int, len(nodes))
		out := make([]Node, 0, len(nodes))
		m := 0
		for _, n := range nodes {
			if j, ok := idxByID[n.ID]; ok {
				out[j].Members = unionStrings(out[j].Members, n.Members)
				out[j].Synonyms = unionStrings(out[j].Synonyms, n.Synonyms)
				if n.Confidence > out[j].Confidence {
					out[j].Confidence = n.Confidence
				}
				m++
				continue
			}
			idxByID[n.ID] = len(out)
			out = append(out, n)
		}
		return out, m
	}
	for di := range domains {
		d := &domains[di]
		for si := range d.Subdomains {
			sd := &d.Subdomains[si]
			var me, mc int
			sd.Entities, me = dedup(sd.Entities)
			sd.Concepts, mc = dedup(sd.Concepts)
			merged += me + mc
		}
	}
	return merged
}

// emptyNodeRef 定位一个 members 为空的融合节点（孤儿节点）及其所属层级，
// 供「空节点补 members」伪 session 展示定位。Node 为指向 domains 内节点的指针，
// 仅在未对该 subdomain 的 Entities/Concepts 做增删（append/删除）期间有效——
// 补 members 只改现有节点的 Members 字段，不增删节点，故安全。
//
// emptyNodeRef locates a fusion node whose members is empty, for member backfill.
type emptyNodeRef struct {
	DomainName    string
	DomainSlug    string
	SubdomainName string
	SubdomainSlug string
	Node          *Node
}

// collectEmptyMemberNodes 收集所有 members 为空的融合节点（孤儿节点）。
// 正常数据下不应存在——它们要么是 LLM 直接产出的空壳骨架节点，要么是 CleanFusionMapping
// 剔除全部非法 member 后变空。返回其定位信息供伪 session 补 members。
//
// collectEmptyMemberNodes gathers all fusion nodes with empty members (orphans).
func collectEmptyMemberNodes(domains []Domain) []emptyNodeRef {
	var out []emptyNodeRef
	for di := range domains {
		d := &domains[di]
		for si := range d.Subdomains {
			sd := &d.Subdomains[si]
			for ni := range sd.Entities {
				if len(sd.Entities[ni].Members) == 0 {
					out = append(out, emptyNodeRef{d.Name, d.Slug, sd.Name, sd.Slug, &sd.Entities[ni]})
				}
			}
			for ni := range sd.Concepts {
				if len(sd.Concepts[ni].Members) == 0 {
					out = append(out, emptyNodeRef{d.Name, d.Slug, sd.Name, sd.Slug, &sd.Concepts[ni]})
				}
			}
		}
	}
	return out
}

// ComputeCoverage 计算融合映射对 04 输入单元的覆盖情况（不修改 domains）。
// ComputeCoverage reports how many 04 units are covered by the fusion mapping.
func ComputeCoverage(domains []Domain, docs []*dktypes.AnnotatedDocument) FusionReport {
	unitIDs := collectUnitIDs(docs)
	covered := buildCovered(domains, unitIDs)

	rpt := FusionReport{InputUnits: len(unitIDs), CoveredUnits: len(covered)}
	seen := make(map[string]bool)
	for _, d := range docs {
		if d == nil {
			continue
		}
		for _, it := range d.Items {
			if it.ID == "" || covered[it.ID] || seen[it.ID] {
				continue
			}
			seen[it.ID] = true
			rpt.UncoveredIDs = append(rpt.UncoveredIDs, it.ID)
		}
	}
	return rpt
}
