// Package incremental 的本文件定义脏集（I-5 脏集收敛的数据结构）与脏传播规则。
//
// 脏传播规则（决策总账 R9 脏集封闭 / 设计定稿 I-5）：
//   - 改/新增 member（管道 P 融入、新建）→ 脏其 node 的 description（I-6 重融合）
//     并脏其 subdomain 的 relation（I-7 重算——成员变 → 语义变 → 拓扑可能变）；
//   - 块内编辑（旁路 R，C1）→ 只脏 node 的「内容」（回写 + FTS 更新），
//     不脏 subdomain relation（V1 定稿：信任编辑者，不因内容编辑触发整子域 relation 重算）；
//   - 删 node（旁路 D 引用计数归零）→ 脏其 subdomain（relation 拓扑变了）；
//   - node 进出 subdomain（新建 node 挂入）→ 脏其 subdomain；
//   - 不涉及的一律不动（R9：未涉及部分零改动）。
//
// This file defines the dirty-set data structure (I-5 convergence) and the
// dirty-propagation rules (R9): member changes dirty the node's description and
// its subdomain's relations; C1 content edits dirty only the node's content
// (rewrite + FTS), never the subdomain's relations; node deletion dirty its
// subdomain. Anything untouched stays byte-identical.
package incremental

import (
	"fmt"
	"sort"
)

// SubdomainKey 唯一标识一个 subdomain（domain slug + subdomain slug，即 nodes 表
// 归属列的值）。用作脏子域集合的键。
// SubdomainKey uniquely identifies a subdomain by its ownership-column slugs.
type SubdomainKey struct {
	Domain    string
	Subdomain string
}

// String 返回可打印形式（日志/告警用）。
func (k SubdomainKey) String() string {
	return fmt.Sprintf("%s/%s", k.Domain, k.Subdomain)
}

// DirtySet 是 I-5 脏集收敛的产物：本轮增量需要动的全部对象，之外一律零改动（R9）。
//
// DirtySet is the converged dirty set (I-5): every object this incremental run
// touches; everything outside it stays byte-identical (R9).
type DirtySet struct {
	// NodesContent 是「内容脏」的 node（C1 块内编辑）：只需 I-9 重渲染其块/镜像
	// 并重建其 FTS，不做 description 重融合、不脏 relation（V1 定稿）。
	// NodesContent are content-dirty nodes (C1 edits): rewrite + FTS only.
	NodesContent map[string]bool
	// NodesFused 是「成员脏」的 node（管道 P 融入新 member）：I-6 需重融合
	// description（human_curated 跳过，R8）。
	// NodesFused are member-dirty nodes (P merged new members): I-6 re-fuses
	// their descriptions (human_curated skipped, R8).
	NodesFused map[string]bool
	// Subdomains 是「relation 脏」的 subdomain：I-7 重算其语义边。
	// Subdomains are relation-dirty subdomains: I-7 recomputes their edges.
	Subdomains map[SubdomainKey]bool
	// FTSSubdomains 是需要重建 FTS 的 subdomain（含 C1 内容变的 node 所在子域——
	// 内容变了索引就过期，但这不是 relation 脏，两个集合独立）。
	// FTSSubdomains need FTS rebuilds (includes C1 nodes' subdomains — content
	// changes expire the index without dirtying relations).
	FTSSubdomains map[SubdomainKey]bool
}

// NewDirtySet 构造空脏集。
// NewDirtySet constructs an empty dirty set.
func NewDirtySet() *DirtySet {
	return &DirtySet{
		NodesContent:  make(map[string]bool),
		NodesFused:    make(map[string]bool),
		Subdomains:    make(map[SubdomainKey]bool),
		FTSSubdomains: make(map[SubdomainKey]bool),
	}
}

// DirtyNodeContent 标记「内容脏」（C1）：重写 + FTS，不脏 relation。
// DirtyNodeContent marks a node content-dirty (C1): rewrite + FTS, no relation dirty.
func (d *DirtySet) DirtyNodeContent(uuid string, key SubdomainKey) {
	d.NodesContent[uuid] = true
	d.FTSSubdomains[key] = true // 内容变 → 索引过期；仅 FTS，不动 relation / FTS only
}

// DirtyNodeFused 标记「成员脏」（融入/新建）：I-6 重融合 + I-7 relation 重算。
// 脏传播：改/新增 member → 脏 node（description）→ 脏 subdomain（relation）。
// DirtyNodeFused marks a node member-dirty (merge/create): I-6 re-fuse +
// I-7 relation recompute (member change → node dirty → subdomain dirty).
func (d *DirtySet) DirtyNodeFused(uuid string, key SubdomainKey) {
	d.NodesFused[uuid] = true
	d.Subdomains[key] = true
	d.FTSSubdomains[key] = true
}

// DirtySubdomain 标记「relation 脏」（删 node / node 进出 / 新子域）。
// DirtySubdomain marks a subdomain relation-dirty (node deleted/added/created).
func (d *DirtySet) DirtySubdomain(key SubdomainKey) {
	d.Subdomains[key] = true
	d.FTSSubdomains[key] = true
}

// SortedFusedNodes 返回成员脏 node 的稳定序列表（测试与日志确定性）。
// SortedFusedNodes returns member-dirty node uuids in stable order.
func (d *DirtySet) SortedFusedNodes() []string {
	out := make([]string, 0, len(d.NodesFused))
	for u := range d.NodesFused {
		out = append(out, u)
	}
	sort.Strings(out)
	return out
}

// SortedSubdomains 返回 relation 脏子域的稳定序列表。
// SortedSubdomains returns relation-dirty subdomains in stable order.
func (d *DirtySet) SortedSubdomains() []SubdomainKey {
	out := make([]SubdomainKey, 0, len(d.Subdomains))
	for k := range d.Subdomains {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Domain != out[j].Domain {
			return out[i].Domain < out[j].Domain
		}
		return out[i].Subdomain < out[j].Subdomain
	})
	return out
}

// Empty 报告脏集是否为空（无任何对象需要动）。
// Empty reports whether nothing needs to change.
func (d *DirtySet) Empty() bool {
	return len(d.NodesContent) == 0 && len(d.NodesFused) == 0 &&
		len(d.Subdomains) == 0 && len(d.FTSSubdomains) == 0
}
