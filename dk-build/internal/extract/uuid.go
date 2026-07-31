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
	"sort"
	"strings"

	"github.com/google/uuid"
)

// dkNamespace 是本项目固定的 UUIDv5 命名空间（常量，编译期确定）。
// 由 uuid.NameSpaceURL 与字符串 "domain-knowledge-layer" 派生，保证跨进程、跨机器
// 对同一 members 集合得到同一 namespace，从而得到同一 uuid（可复现性）。
//
// dkNamespace is the project-fixed UUIDv5 namespace (constant, compile-time determined).
// Derived from uuid.NameSpaceURL and "domain-knowledge-layer" so that the same members set
// yields the same namespace (and thus the same uuid) across processes and machines.
var dkNamespace = uuid.NewSHA1(uuid.NameSpaceURL, []byte("domain-knowledge-layer"))

// NodeUUID 是 node 的「首次确定性生成器」：
//
//	uuidv5(dkNamespace, ",".join(sorted(unique(non-empty(members)))))
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
// uuidv5(dkNamespace, ",".join(sorted(unique(non-empty(members))))).
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
	return uuid.NewSHA1(dkNamespace, []byte(strings.Join(uniq, ","))).String()
}

// AssignNodeUUIDs 在「最后一个 LLM 阶段（stage 3.7 relate）之后、ingest（stage 4）之前」
// 调用，就地遍历 domains，为每个 entity/concept 融合节点派生并覆盖其 ID（原为 LLM 临时
// kebab id），并在每个 subdomain 内翻译所有 Relations 的 Source/Target 端点。
//
// 行为：
//  1. 对每个 subdomain 的每个 entity / concept，计算 u = NodeUUID(node.Members)，
//     在该 subdomain 内建立 旧ID(kebab)→uuid 映射，再用 u 覆盖 node.ID；
//  2. 遍历该 subdomain 的所有 Relations：若 Source/Target 命中上述映射则替换为 uuid，
//     未命中（如跨子域引用、或指向已不存在的临时 id）则**保留原值**——后续 ingest 的
//     端点存在性校验会把它当作悬空端点跳过（绝不产生悬空边，R5）。
//
// 就地修改语义：domains 切片及其内部 Node / Relation 均被直接改写（不返回新切片）。
// 调用方应在完成所有 LLM 阶段后调用本函数；调用后所有 prompt 阶段都已结束，uuid 进入
// 数据流不再违反 R1。
//
// 关键铁律（R2 / V3-a / R1）：
//   - NodeUUID 仅作首次确定性生成器；uuid 首次分配后钉死永不变（R2 / V3-a）。
//   - 本函数不做"幂等再分配"——重复调用会再次覆盖 ID（但因 NodeUUID 是纯函数，
//     同一 members 的输出不变，故重复调用的最终态一致）；生产中应只调一次。
//   - UUID 绝不进入任何 LLM prompt（R1）——本函数注释明确标注。
//
// AssignNodeUUIDs is called after the last LLM stage (3.7 relate) and before ingest (4).
// It walks domains in place: for each entity/concept it derives u = NodeUUID(node.Members),
// builds a kebab→uuid map per subdomain, overwrites node.ID with u, and translates the
// Source/Target of every Relation in that subdomain (unmapped endpoints are left as-is
// and will be skipped by ingest's endpoint-existence check — never producing dangling
// edges, R5). Mutates in place; should be called exactly once in production (NodeUUID is
// pure, so re-calls converge to the same final state, but the contract is "call once").
//
// R2 / V3-a / R1: NodeUUID is the first-time-only deterministic generator; a uuid, once
// assigned, is pinned forever and never re-derived on member changes. UUID NEVER enters
// any LLM prompt — this function runs only after all prompt stages are done.
func AssignNodeUUIDs(domains []Domain) {
	for di := range domains {
		for si := range domains[di].Subdomains {
			sd := &domains[di].Subdomains[si]
			// 该 subdomain 内的 旧ID(kebab)→uuid 映射，用于翻译 Relations 端点。
			// kebab→uuid map within this subdomain, used to translate relation endpoints.
			kebabToUUID := make(map[string]string)

			// entity：派生 uuid、建映射、覆盖 ID。
			// entities: derive uuid, register mapping, overwrite ID.
			for ni := range sd.Entities {
				u := NodeUUID(sd.Entities[ni].Members)
				kebabToUUID[sd.Entities[ni].ID] = u
				sd.Entities[ni].ID = u
			}
			// concept：同上。
			// concepts: same as entities.
			for ni := range sd.Concepts {
				u := NodeUUID(sd.Concepts[ni].Members)
				kebabToUUID[sd.Concepts[ni].ID] = u
				sd.Concepts[ni].ID = u
			}
			// 翻译 Relations 端点：仅替换命中映射的端点（命中才替换），
			// 未命中保留原值，交由 ingest 做端点存在性校验（缺端点则跳过，绝不悬空）。
			// Translate relation endpoints: replace only when the endpoint hits the map;
			// unmapped endpoints are left untouched for ingest's existence check (R5).
			for ri := range sd.Relations {
				if u, ok := kebabToUUID[sd.Relations[ri].Source]; ok {
					sd.Relations[ri].Source = u
				}
				if u, ok := kebabToUUID[sd.Relations[ri].Target]; ok {
					sd.Relations[ri].Target = u
				}
			}
		}
	}
}
