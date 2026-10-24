// Package extract 的本文件实现 node UUID 的「首次确定性派生」与「就地分配」
// （增量闭环第一块地基：为后续的结构化回写与稳定身份打地基）。
//
// 设计背景（决策总账 R2 / V3-a）：
//   - node 的稳定身份由 uuidv5(namespace, sorted(unique(members))) 派生，保证同一批
//     members（无论顺序、无论是否含重复）在全量首建时得到同一 uuid、可复现；
//   - uuid 仅在 node **首次创建**时由本文件的 NodeUUID 派生一次，落库后**钉死永不变**；
//     此后 members 的增删**不再**调用 NodeUUID 重新派生（避免破坏稳定身份）；
//   - 只有身份重组（merge / split）才会产生新 uuid，并写 uuid_lineage 表记录血缘。
//
// 关键铁律（R1）：UUID 绝不进入任何 LLM prompt。本文件只由代码调用，LLM 全程只见
// 临时 kebab id；AssignNodeUUIDs 把 kebab→uuid 的翻译放在「最后一个 LLM 阶段之后、
// ingest 之前」执行（U1 采纳方案 a），确保所有 prompt 阶段都不接触 uuid。
//
// 本批只定义函数，不接入 orchestrator；调用时机由后续批次在 stage 3.7（relate）之后、
// stage 4（ingest）之前接线。
//
// This file implements the "first-time deterministic derivation" and in-place assignment
// of node UUIDs (incremental-closure first block: foundation for structured write-back
// and stable identity). UUID is derived once at first creation via uuidv5 over sorted
// unique members and then pinned forever (R2 / V3-a). It NEVER enters any LLM prompt (R1);
// AssignNodeUUIDs translates kebab→uuid only after the last LLM stage (U1, solution a).
// This batch only defines the functions; wiring into the orchestrator is a later batch.
package extract

import (
	"log"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// cairnNamespace 是本项目固定的 UUIDv5 命名空间（常量，编译期确定）。
// 由 uuid.NameSpaceURL 与字符串 "domain-knowledge-layer" 派生，保证跨进程、跨机器
// 对同一 members 集合得到同一 namespace，从而得到同一 uuid（可复现性）。
//
// ⚠ 命名空间种子字符串 "domain-knowledge-layer" 是历史遗留，**永远不要改成 "cairn"**：
// UUIDv5 的输出由种子决定，改种子会让所有已有节点的 UUID 全部失效，导致已发布
// Bundle 中的 UUID 锚点与增量更新后的 UUID 不再对应，破坏稳定身份铁律（R2）。
// 项目虽已更名为 Cairn，但 UUID 命名空间是不可变的兼容性契约。
//
// cairnNamespace is the project-fixed UUIDv5 namespace (constant, compile-time determined).
// The seed string "domain-knowledge-layer" is deliberately retained for backward
// compatibility — changing it would invalidate every existing node UUID.
var cairnNamespace = uuid.NewSHA1(uuid.NameSpaceURL, []byte("domain-knowledge-layer"))

// NodeUUID 是 node 的「首次确定性生成器」：
//
//	uuidv5(cairnNamespace, ",".join(sorted(unique(non-empty(members)))))
//
// 性质：
//  1. 同一 members 集合（无论顺序、无论是否含重复或空串）→ 同一 uuid（可复现）；
//  2. 不同 members 集合 → 不同 uuid（UUIDv5 在固定 namespace 下的抗碰撞性）；
//  3. 纯函数、零副作用、零外部依赖（除 google/uuid），不触碰 LLM、不读存储。
//
// 用途约束（决策总账 R2 / V3-a）：
//   - 仅在 node **首次创建**时调用它派生初始 uuid；
//   - uuid 一经分配即钉死存库、永不变；此后 members 增删**不再**调用它重派生
//     （否则会破坏稳定身份——这是增量闭环与回写锚定的命脉）；
//   - 只有 merge / split 身份重组才产生新 uuid，并写 uuid_lineage 表（本批不涉及）。
//
// 关键铁律（R1）：UUID 绝不进入任何 LLM prompt。NodeUUID 只由代码调用，LLM 全程
// 不接触 uuid；需要让 LLM 引用已有 node 时一律用稳定别名 alias（call-scoped、不持久化）。
//
// NodeUUID is the "first-time deterministic generator" for node identity:
// uuidv5(cairnNamespace, ",".join(sorted(unique(non-empty(members))))).
// Same members (any order, with duplicates or empty strings) → same uuid; different members
// → different uuid; pure, side-effect-free. Per R2 / V3-a it is called ONLY at a node's
// first creation; the uuid is then pinned forever and never re-derived on member changes.
// Per R1, UUID NEVER enters any LLM prompt; LLM references existing nodes by alias only.
func NodeUUID(members []string) string {
	// 去空、去重，保证 (members 顺序 / 含重复 / 含空串) 不影响结果。
	// Drop empty strings and de-duplicate, so order / duplicates / empties do not affect output.
	seen := make(map[string]bool, len(members))
	uniq := make([]string, 0, len(members))
	for _, m := range members {
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		uniq = append(uniq, m)
	}
	// 排序，保证 members 集合的任何排列都得到同一 uuid。
	// Sort so any permutation of the same members set yields the same uuid.
	sort.Strings(uniq)
	return uuid.NewSHA1(cairnNamespace, []byte(strings.Join(uniq, ","))).String()
}

// AssignNodeUUIDs 在所有 LLM 阶段之后一次分配首次 UUID。先建立完整的带归属临时键映射，
// 再翻译关系，最后修改节点，避免目标子域尚未遍历或别名冲突使跨子域关系丢失。
// 跨子域候选必须由调用方先做原文验证；解析不代表语义认可。增量既有身份不经过本函数。
// AssignNodeUUIDs runs after source validation and all other LLM stages (R1).
// It binds every qualified temporary identity before mutation; existing identities stay pinned (R2).
func AssignNodeUUIDs(domains []Domain) {
	index := indexEndpoints(domains)
	identities := make(map[*Node]string)
	for _, refs := range index.global {
		for _, ref := range refs {
			identities[ref.node] = NodeUUID(ref.node.Members)
		}
	}
	for di := range domains {
		for si := range domains[di].Subdomains {
			sd := &domains[di].Subdomains[si]
			kept := sd.Relations[:0]
			for _, r := range sd.Relations {
				source, target, ss, ts := index.relation(endpointScope{domains[di].Slug, sd.Slug}, r)
				if ss != "resolved" || ts != "resolved" {
					log.Printf("[extract-uuid] 拒绝端点: %s/%s %s --%s--> %s (source=%s target=%s)", domains[di].Slug, sd.Slug, r.Source, r.Kind, r.Target, ss, ts)
					continue
				}
				r.Source, r.Target = identities[source.node], identities[target.node]
				kept = append(kept, r)
			}
			sd.Relations = kept
		}
	}
	for node, id := range identities {
		node.ID = id
	}
}
