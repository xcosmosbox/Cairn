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
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/xcosmosbox/cairn/core/dktypes"
	"sort"
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

// DedupFusionNodes 仅合并语义字段与成员集合相同的对象；相同临时别名不是等价证据。
// 无歧义的旧别名引用跟随对象移动，真正的歧义留待所属关系范围修复。
// DedupFusionNodes merges exact semantic duplicates and conserves proven endpoint identity.
func DedupFusionNodes(domains []Domain) int {
	merged := 0
	before := indexEndpoints(domains)
	type canonicalTarget struct {
		scope endpointScope
		id    string
	}
	canonicalByScope := map[endpointScope]map[string]canonicalTarget{}
	mergeMetadata := func(target *Node, n Node) {
		target.Members = unionStrings(target.Members, n.Members)
		target.Synonyms = unionStrings(target.Synonyms, n.Synonyms)
		target.SourceSkills = unionStrings(target.SourceSkills, n.SourceSkills)
		if n.Confidence > target.Confidence {
			target.Confidence = n.Confidence
		}
	}
	var currentScope endpointScope
	dedup := func(nodes []Node) ([]Node, int) {

		// 相同临时 id 不代表相同对象；只有语义字段和成员集合相同才可合并。
		// Equal aliases alone never establish semantic equivalence.
		aliases := map[string]map[string]bool{}
		for _, n := range nodes {
			key := nodeSemanticKey(n)
			if aliases[n.ID] == nil {
				aliases[n.ID] = map[string]bool{}
			}
			aliases[n.ID][key] = true
		}
		signatureCounts := map[string]int{}
		for _, n := range nodes {
			signatureCounts[nodeSemanticKey(n)]++
		}
		idxBySignature := make(map[string]int, len(nodes))
		out := make([]Node, 0, len(nodes))
		m := 0
		for _, n := range nodes {
			members := unionStrings(nil, n.Members)
			sort.Strings(members)
			wire, _ := json.Marshal([]any{n.Label, n.Name, n.Summary, n.Description, members})
			signature := string(wire)
			if j, ok := idxBySignature[signature]; ok {
				mergeMetadata(&out[j], n)
				canonicalByScope[currentScope][signature] = canonicalTarget{currentScope, out[j].ID}
				m++
				continue
			}
			if signatureCounts[signature] > 1 {
				qualified, _ := json.Marshal([]string{currentScope.domain, currentScope.subdomain, signature})
				n.ID = fmt.Sprintf("fusion-%x", sha256.Sum256(qualified))
			} else if len(aliases[n.ID]) > 1 {
				n.ID = fmt.Sprintf("%s-distinct-%x", n.ID, sha256.Sum256(wire))
			}
			idxBySignature[signature] = len(out)
			canonicalByScope[currentScope][signature] = canonicalTarget{currentScope, n.ID}
			out = append(out, n)
		}
		return out, m
	}
	for di := range domains {
		d := &domains[di]
		for si := range d.Subdomains {
			sd := &d.Subdomains[si]
			currentScope = endpointScope{d.Slug, sd.Slug}
			canonicalByScope[currentScope] = map[string]canonicalTarget{}
			var me, mc int
			sd.Entities, me = dedup(sd.Entities)
			sd.Concepts, mc = dedup(sd.Concepts)
			merged += me + mc
		}
	}
	// 同域完全相同的候选必须在融合前只保留一次，否则独立改写会变成同 UUID 的正文冲突。
	// Keep the first existing traversal classification, not a newly inferred or sorted taxonomy.
	for di := range domains {
		seen := map[string]endpointRef{}
		dedupAcrossSubdomains := func(nodes []Node, expectedLabel string) []Node {
			out := make([]Node, 0, len(nodes))
			for _, n := range nodes {
				// 只扩展 parser 已认证的分类；导出 helper 的手工坏标签沿用旧局部行为。
				// Preserve malformed manual inputs without inferring their container identity.
				if n.Label != expectedLabel {
					out = append(out, n)
					continue
				}
				key := nodeSemanticKey(n)
				if canonical, ok := seen[key]; ok {
					mergeMetadata(canonical.node, n)
					canonicalByScope[currentScope][key] = canonicalTarget{canonical.scope, canonical.node.ID}
					merged++
					continue
				}
				out = append(out, n)
				seen[key] = endpointRef{currentScope, &out[len(out)-1]}
			}
			return out
		}
		for si := range domains[di].Subdomains {
			sd := &domains[di].Subdomains[si]
			currentScope = endpointScope{domains[di].Slug, sd.Slug}
			sd.Entities = dedupAcrossSubdomains(sd.Entities, "Entity")
			sd.Concepts = dedupAcrossSubdomains(sd.Concepts, "Concept")
		}
	}
	// 先前无歧义的旧引用必须跟随去重后的对象；真正歧义不能猜目标。
	// Preserve proven alias redirects without inventing relation semantics.
	after := indexEndpoints(domains)
	unresolved := func(scope endpointScope, alias string) string {
		wire, _ := json.Marshal([]string{scope.domain, scope.subdomain, alias})
		candidate := fmt.Sprintf("unresolved-fusion-%x", sha256.Sum256(wire))
		for len(after.global[candidate]) > 0 {
			candidate += "-missing"
		}
		return candidate
	}
	redirect := func(scope endpointScope, alias string) string {
		refs := before.local[scope][alias]
		if len(refs) == 0 && len(before.global[alias]) == 0 {
			var framed []string
			if json.Unmarshal([]byte(alias), &framed) == nil && len(framed) == 3 {
				refs = before.local[endpointScope{framed[0], framed[1]}][framed[2]]
			}
		}
		if len(refs) == 0 {
			refs = before.global[alias]
		}
		var target canonicalTarget
		for _, ref := range refs {
			canonical := canonicalByScope[ref.scope][nodeSemanticKey(*ref.node)]
			if canonical.id == "" || (target.id != "" && target != canonical) {
				return unresolved(scope, alias)
			}
			target = canonical
		}
		if target.id != "" {
			candidate := target.id
			// 新的 ID 仍须绑定新归属，不能被旧子域中恰好同名的另一个对象截走。
			// Frame the surviving scope together with its ID, including formerly framed references.
			if target.scope != scope {
				candidate = qualifiedRelationAlias(endpointRef{target.scope, &Node{ID: target.id}})
			}
			// frame 自身也可能是另一个节点的原始 ID；必须按既有解析优先级复核归属。
			// A literal identity can shadow the new frame; never redirect to that different object.
			resolved, status := after.resolve(scope, candidate)
			if status != "resolved" || resolved.scope != target.scope || resolved.node.ID != target.id {
				return unresolved(scope, alias)
			}
			return candidate
		}
		return alias
	}
	for di := range domains {
		for si := range domains[di].Subdomains {
			sd := &domains[di].Subdomains[si]
			scope := endpointScope{domains[di].Slug, sd.Slug}
			for ri := range sd.Relations {
				sd.Relations[ri].Source = redirect(scope, sd.Relations[ri].Source)
				sd.Relations[ri].Target = redirect(scope, sd.Relations[ri].Target)
			}
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

// nodeSemanticKey 不以别名、置信度或来源顺序猜测对象等价。
// nodeSemanticKey compares retained semantic material and the canonical member set.
func nodeSemanticKey(n Node) string {
	members := unionStrings(nil, n.Members)
	sort.Strings(members)
	wire, _ := json.Marshal([]any{n.Label, n.Name, n.Summary, n.Description, members})
	return string(wire)
}
