// Package patch 定义全量重整（rebalance）的结构 patch 类型、JSON 解析与校验。
// LLM 在 Louvain 先验骨架上做语义微调后，只对需要改的部分输出本包定义的 patch；
// 代码侧翻译 alias → uuid/层节点键后逐操作应用（R-5）。
//
// 设计约束（§4.2）：
//   - 纯数据 + 纯函数，不依赖 storage/incremental（可独立单测、避免环）；
//   - patch 里只有 call-scoped alias（<subdomain-slug>#<序号> / sd#<n> / dom#<n>），
//     绝无 node uuid（R1：uuid 不进 prompt，自然也不可能出现在 patch 里）；
//   - 校验失败的错误信息仅供代码侧日志；重试反馈由编排器用固定文案，
//     绝不把本包错误原文（含 LLM 提供的值）回显给 LLM（防回显泄漏，R1）。
//
// Package patch defines the rebalance structural patch: types, JSON parsing and
// validation. Pure data + pure functions; aliases only, never UUIDs (R1).
package patch

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xcosmosbox/cairn/build/internal/extract"
)

// 操作词表（§6 R-4）。LLM 只输出需要改的部分；未涉及部分零 patch（R9）。
// The operation vocabulary. The LLM emits only what needs to change.
const (
	OpMergeNodes     = "merge_nodes"     // 语义等价 node 合成一个
	OpSplitNode      = "split_node"      // 一 node 拆成多个
	OpRemigrateNode  = "remigrate_node"  // node 换归属（uuid 不变）
	OpMergeSubdomain = "merge_subdomain" // 过小子域合并
	OpSplitSubdomain = "split_subdomain" // 过大子域拆分
	OpPromote        = "promote"         // node 升格为子域代表（保守语义：新建子域+迁入）
	OpDemote         = "demote"          // 子域降格并入另一子域
	OpDropEdge       = "drop_edge"       // 删冗余语义边
	OpRename         = "rename"          // 重命名（name 非身份，uuid 不变）
)

// Op 是 patch 中的一条结构操作。字段按 op 取用（见各字段注释）。
// Op is one structural patch operation; fields are interpreted per op.
type Op struct {
	Op string `json:"op"`

	// Members 是 merge_nodes 的成分 node alias 列表（≥2）。
	Members []string `json:"members,omitempty"`
	// Name 是 merge_nodes 的可选新名 / rename 的新名。
	Name string `json:"name,omitempty"`

	// Source 是 split_node 的源 node alias / drop_edge 的源端 alias。
	Source string `json:"source,omitempty"`
	// Parts 是 split_node / split_subdomain 的拆分部分。
	Parts []Part `json:"parts,omitempty"`

	// Node 是 remigrate_node / promote 的目标 node alias。
	Node string `json:"node,omitempty"`
	// ToDomain 是 remigrate_node 的目标 domain（dom# alias 或新 domain 名；空 = 同域内迁移）。
	ToDomain string `json:"to_domain,omitempty"`
	// ToSubdomain 是 remigrate_node 的目标 subdomain（sd# alias 或新子域名）。
	ToSubdomain string `json:"to_subdomain,omitempty"`

	// Sources 是 merge_subdomain 的来源 sd# alias 列表（≥2）。
	Sources []string `json:"sources,omitempty"`
	// Target 是 merge_subdomain 的目标（sd# alias 或新子域名）/ drop_edge 的目标端 alias
	// / rename 的目标（node/sd#/dom# alias）。
	Target string `json:"target,omitempty"`

	// NewSubdomainName 是 promote 的新子域名（在 node 现有 domain 下新建）。
	NewSubdomainName string `json:"new_subdomain_name,omitempty"`
	// Subdomain 是 demote 的源子域 sd# alias。
	Subdomain string `json:"subdomain,omitempty"`
	// IntoSubdomain 是 demote 的并入目标 sd# alias。
	IntoSubdomain string `json:"into_subdomain,omitempty"`

	// Kind 是 drop_edge 的边类型（6 语义枚举之一）。
	Kind string `json:"kind,omitempty"`
}

// Part 是 split_node / split_subdomain 的一个拆分部分。
// Part is one part of a split operation.
type Part struct {
	Name string `json:"name"` // 新 node / 新子域的展示名
	// Tag 是 split_node 新 node 的类型（entity|concept）。
	Tag string `json:"tag,omitempty"`
	// MemberIDs 是 split_node 该片的 member（04 id）划分——必须是源 node
	// 原 members 的不重不漏划分。
	MemberIDs []string `json:"member_ids,omitempty"`
	// Nodes 是 split_subdomain 该片（新子域）要容纳的 node alias 列表。
	Nodes []string `json:"nodes,omitempty"`
}

// Patch 是一次重整的结构操作集合。
// Patch is the set of structural operations for one rebalance.
type Patch struct {
	Ops []Op `json:"ops"`
}

// Parse 解析 LLM 返回的 patch JSON（容忍 ```json 代码围栏）。
// Parse parses the LLM patch JSON (tolerates a code fence).
func Parse(data string) (*Patch, error) {
	text := stripFence(strings.TrimSpace(data))
	if text == "" {
		return nil, fmt.Errorf("patch 为空")
	}
	var p Patch
	if err := json.Unmarshal([]byte(text), &p); err != nil {
		return nil, fmt.Errorf("patch JSON 非法: %w", err)
	}
	return &p, nil
}

// stripFence 去除可选的 ```json 代码围栏。
// stripFence removes an optional json code fence.
func stripFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = s[idx+1:]
	}
	s = strings.TrimSuffix(strings.TrimRight(s, "\n"), "```")
	return strings.TrimSpace(s)
}

// Scope 是校验所需的 call-scoped 上下文：alias 命中判定 + 源 node 的原始
// members（split 划分校验）+ human_curated 判定（拆人工策展节点保守劝阻）。
// 全部由编排器从 KG 快照装配；本包不接触存储。
//
// Scope carries the call-scoped validation context (alias hit tests, original
// member lists for split partition checks, human_curated guards).
type Scope struct {
	// NodeAliases 是全部合法 node alias（<subdomain-slug>#<序号>）。
	NodeAliases map[string]bool
	// SubdomainAliases 是全部合法子域 alias（sd#<n>）。
	SubdomainAliases map[string]bool
	// DomainAliases 是全部合法域 alias（dom#<n>）。
	DomainAliases map[string]bool
	// MembersOf 返回某 node alias 对应 node 的原始 members（distinct 04 id，
	// 稳定排序）。未知 alias 返回 nil。
	MembersOf func(nodeAlias string) []string
	// IsHumanCurated 报告某 node alias 是否 human_curated（split 保守劝阻）。
	IsHumanCurated func(nodeAlias string) bool
}

// Validate 校验 patch 的每个操作：alias 命中、kind 枚举、merge ≥2 且不自合、
// split 划分不重不漏、promote/demote 保守约束。返回第一个违规（错误信息仅供
// 代码侧日志；重试反馈由编排器固定文案，R1）。
//
// Validate checks every op against the scope. It returns the first violation.
func (p *Patch) Validate(scope *Scope) error {
	if p == nil {
		return fmt.Errorf("patch 为 nil")
	}
	for i := range p.Ops {
		if err := p.Ops[i].validate(scope); err != nil {
			return fmt.Errorf("第 %d 个操作: %w", i+1, err)
		}
	}
	return nil
}

// validate 校验单个操作。
// validate checks one operation.
func (op *Op) validate(scope *Scope) error {
	switch op.Op {
	case OpMergeNodes:
		if len(op.Members) < 2 {
			return fmt.Errorf("merge_nodes 至少需要 2 个成分")
		}
		seen := make(map[string]bool, len(op.Members))
		for _, m := range op.Members {
			if !scope.nodeHit(m) {
				return fmt.Errorf("merge_nodes 成员 alias 未命中")
			}
			if seen[m] {
				return fmt.Errorf("merge_nodes 存在重复成员（自合）")
			}
			seen[m] = true
		}
		return nil

	case OpSplitNode:
		if !scope.nodeHit(op.Source) {
			return fmt.Errorf("split_node 源 alias 未命中")
		}
		if scope.IsHumanCurated != nil && scope.IsHumanCurated(op.Source) {
			return fmt.Errorf("split_node 不建议拆分 human_curated 节点（改用其它操作）")
		}
		if len(op.Parts) < 2 {
			return fmt.Errorf("split_node 至少需要 2 个部分")
		}
		// 划分校验：各片 member_ids 的并集 == 源 node 原 members（不重不漏）。
		orig := map[string]bool{}
		if scope.MembersOf != nil {
			for _, m := range scope.MembersOf(op.Source) {
				orig[m] = true
			}
		}
		assigned := make(map[string]bool)
		for _, part := range op.Parts {
			if strings.TrimSpace(part.Name) == "" {
				return fmt.Errorf("split_node 部分缺 name")
			}
			if part.Tag != "entity" && part.Tag != "concept" {
				return fmt.Errorf("split_node 部分 tag 非法（必须 entity/concept）")
			}
			if len(part.MemberIDs) == 0 {
				return fmt.Errorf("split_node 部分缺 member_ids")
			}
			for _, m := range part.MemberIDs {
				if !orig[m] {
					return fmt.Errorf("split_node member 不属于源 node（划分越界）")
				}
				if assigned[m] {
					return fmt.Errorf("split_node member 被重复分配（划分重叠）")
				}
				assigned[m] = true
			}
		}
		if len(assigned) != len(orig) {
			return fmt.Errorf("split_node 划分不重不漏校验失败（有 member 未分配）")
		}
		return nil

	case OpRemigrateNode:
		if !scope.nodeHit(op.Node) {
			return fmt.Errorf("remigrate_node 目标 alias 未命中")
		}
		if strings.TrimSpace(op.ToSubdomain) == "" {
			return fmt.Errorf("remigrate_node 缺 to_subdomain")
		}
		if strings.HasPrefix(op.ToSubdomain, "sd#") && !scope.subHit(op.ToSubdomain) {
			return fmt.Errorf("remigrate_node to_subdomain alias 未命中")
		}
		if op.ToDomain != "" && strings.HasPrefix(op.ToDomain, "dom#") && !scope.domHit(op.ToDomain) {
			return fmt.Errorf("remigrate_node to_domain alias 未命中")
		}
		return nil

	case OpMergeSubdomain:
		if len(op.Sources) < 2 {
			return fmt.Errorf("merge_subdomain 至少需要 2 个来源子域")
		}
		seen := make(map[string]bool, len(op.Sources))
		for _, s := range op.Sources {
			if !scope.subHit(s) {
				return fmt.Errorf("merge_subdomain 来源 alias 未命中")
			}
			if seen[s] {
				return fmt.Errorf("merge_subdomain 来源重复")
			}
			seen[s] = true
		}
		if strings.TrimSpace(op.Target) == "" {
			return fmt.Errorf("merge_subdomain 缺 target")
		}
		if strings.HasPrefix(op.Target, "sd#") && !scope.subHit(op.Target) {
			return fmt.Errorf("merge_subdomain target alias 未命中")
		}
		return nil

	case OpSplitSubdomain:
		if !scope.subHit(op.Source) {
			return fmt.Errorf("split_subdomain 源 alias 未命中")
		}
		if len(op.Parts) < 2 {
			return fmt.Errorf("split_subdomain 至少需要 2 个部分")
		}
		assigned := make(map[string]bool)
		for _, part := range op.Parts {
			if strings.TrimSpace(part.Name) == "" {
				return fmt.Errorf("split_subdomain 部分缺 name")
			}
			if len(part.Nodes) == 0 {
				return fmt.Errorf("split_subdomain 部分缺 nodes")
			}
			for _, a := range part.Nodes {
				if !scope.nodeHit(a) {
					return fmt.Errorf("split_subdomain 部分 node alias 未命中")
				}
				if assigned[a] {
					return fmt.Errorf("split_subdomain node 被重复分配")
				}
				assigned[a] = true
			}
		}
		return nil

	case OpPromote:
		if !scope.nodeHit(op.Node) {
			return fmt.Errorf("promote 目标 alias 未命中")
		}
		if strings.TrimSpace(op.NewSubdomainName) == "" {
			return fmt.Errorf("promote 缺 new_subdomain_name")
		}
		return nil

	case OpDemote:
		if !scope.subHit(op.Subdomain) {
			return fmt.Errorf("demote 源子域 alias 未命中")
		}
		if !scope.subHit(op.IntoSubdomain) {
			return fmt.Errorf("demote 目标子域 alias 未命中")
		}
		if op.Subdomain == op.IntoSubdomain {
			return fmt.Errorf("demote 源与目标相同（无意义）")
		}
		return nil

	case OpDropEdge:
		if !scope.nodeHit(op.Source) || !scope.nodeHit(op.Target) {
			return fmt.Errorf("drop_edge 端点 alias 未命中")
		}
		if op.Source == op.Target {
			return fmt.Errorf("drop_edge 自环（无意义）")
		}
		if !extract.IsValidRelationKind(op.Kind) {
			return fmt.Errorf("drop_edge kind 非法（必须 6 语义枚举之一）")
		}
		return nil

	case OpRename:
		if strings.TrimSpace(op.Target) == "" {
			return fmt.Errorf("rename 缺 target")
		}
		if !scope.nodeHit(op.Target) && !scope.subHit(op.Target) && !scope.domHit(op.Target) {
			return fmt.Errorf("rename 目标 alias 未命中")
		}
		if strings.TrimSpace(op.Name) == "" {
			return fmt.Errorf("rename 缺新名")
		}
		return nil

	default:
		return fmt.Errorf("未知操作 %q", op.Op)
	}
}

// nodeHit / subHit / domHit 是 alias 命中判定的 nil 安全封装。
func (s *Scope) nodeHit(alias string) bool {
	return s != nil && s.NodeAliases[alias]
}

func (s *Scope) subHit(alias string) bool {
	return s != nil && s.SubdomainAliases[alias]
}

func (s *Scope) domHit(alias string) bool {
	return s != nil && s.DomainAliases[alias]
}
