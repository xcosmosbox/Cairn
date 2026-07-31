// Package observe 提供知识库版本间的血缘感知 diff 引擎（演化观测套件 E-1）。
//
// 相对 `git diff` 的核心价值（R-ev-3）：节点「消失」不是朴素删除——用 to 库
// uuid_lineage 判定为「合并进 X」（merged_into）或「拆分为 [...]」（split_into）；
// 归属（domain/subdomain）变化判为迁移（migrated），name 变化判为改名（renamed）。
// 反过来，合并幸存者 / 拆分产物出现在 to 库里也不算「新增」（它们在血缘目标的
// 全集中），避免 merge/split 被误报成一删一增。
//
// 血缘一律取自 to 库（merge/split 由重整写进运行后的库），from 可为 nil
// （v0 初次记录：全部节点/边判为 added，且不做血缘目标排除）。
//
// 铁律：纯只读（R-ev-2，仅 ListAll/ResolveSuccessors 等只读查询）、
// 确定性（R-ev-5，分类内部按 id 升序，同输入同输出）、零 LLM 零向量（R-ev-4）。
//
// Package observe implements the lineage-aware diff engine between two KG
// versions (evolution suite E-1): merge/split/migrate/rename classification
// instead of naive add/delete, deterministic ordering, read-only access.
package observe

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/xcosmosbox/domain-knowledge-layer/core/dktypes"
	"github.com/xcosmosbox/domain-knowledge-layer/core/storage"
)

// 节点变更类别 / node change categories.
const (
	ChangeAdded      = "added"
	ChangeDeleted    = "deleted"
	ChangeMergedInto = "merged_into"
	ChangeSplitInto  = "split_into"
	ChangeMigrated   = "migrated"
	ChangeRenamed    = "renamed"
	ChangeContent    = "content"
	ChangeUnchanged  = "unchanged"
)

// 边变更类别 / edge change categories.
const (
	EdgeAdded   = "added"
	EdgeDropped = "dropped"
)

// NodeChange 描述单个节点在两个版本间的命运。
//
// NodeChange describes the fate of one node between two versions.
type NodeChange struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Change string `json:"change"` // added|deleted|merged_into|split_into|migrated|renamed|content|unchanged
	Detail any    `json:"detail,omitempty"`
}

// UnmarshalJSON 按 Change 把 Detail 还原为具体类型（默认反序列化只会得到
// map[string]any，Markdown()/dk show 需要类型化 Detail）。
//
// UnmarshalJSON restores the concrete Detail type implied by Change so a
// JSON round-trip keeps DiffResult fully renderable.
func (n *NodeChange) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID     string          `json:"id"`
		Name   string          `json:"name"`
		Change string          `json:"change"`
		Detail json.RawMessage `json:"detail"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	n.ID, n.Name, n.Change = raw.ID, raw.Name, raw.Change
	if len(raw.Detail) == 0 {
		return nil
	}
	switch raw.Change {
	case ChangeMergedInto:
		var d MergedIntoDetail
		if err := json.Unmarshal(raw.Detail, &d); err == nil {
			n.Detail = d
		}
	case ChangeSplitInto:
		var d SplitIntoDetail
		if err := json.Unmarshal(raw.Detail, &d); err == nil {
			n.Detail = d
		}
	case ChangeMigrated:
		var d MigratedDetail
		if err := json.Unmarshal(raw.Detail, &d); err == nil {
			n.Detail = d
		}
	case ChangeRenamed:
		var d RenamedDetail
		if err := json.Unmarshal(raw.Detail, &d); err == nil {
			n.Detail = d
		}
	case ChangeContent:
		var d ContentDetail
		if err := json.Unmarshal(raw.Detail, &d); err == nil {
			n.Detail = d
		}
	}
	return nil
}

// EdgeChange 描述一条边（按 source_id+target_id+kind 三元组对齐）的增删。
// SourceName/TargetName 仅为人读展示（事件流 / Markdown），不参与对齐。
//
// EdgeChange describes one edge addition/removal, aligned by the
// (source_id, target_id, kind) triple; names are display-only.
type EdgeChange struct {
	Source     string `json:"source"`
	Target     string `json:"target"`
	Kind       string `json:"kind"`
	Change     string `json:"change"` // added|dropped
	SourceName string `json:"source_name,omitempty"`
	TargetName string `json:"target_name,omitempty"`
}

// MergedIntoDetail 是 ChangeMergedInto 的 Detail 载荷。
type MergedIntoDetail struct {
	Into     string `json:"into"`
	IntoName string `json:"into_name"`
}

// SplitIntoDetail 是 ChangeSplitInto 的 Detail 载荷。
type SplitIntoDetail struct {
	Into      []string `json:"into"`
	IntoNames []string `json:"into_names"`
}

// MigratedDetail 是 ChangeMigrated 的 Detail 载荷。
type MigratedDetail struct {
	FromDomain    string `json:"from_domain"`
	ToDomain      string `json:"to_domain"`
	FromSubdomain string `json:"from_subdomain"`
	ToSubdomain   string `json:"to_subdomain"`
}

// RenamedDetail 是 ChangeRenamed 的 Detail 载荷。
type RenamedDetail struct {
	OldName string `json:"old_name"`
	NewName string `json:"new_name"`
}

// ContentDetail 是 ChangeContent 的 Detail 载荷（哪些内容字段变了）。
type ContentDetail struct {
	Fields []string `json:"fields"`
}

// DiffResult 是一次版本间 diff 的完整结果。
// Counts 固定含全部计数键（缺省为 0），供 changeset 汇总列直接取用：
// added/deleted/merged/split/migrated/renamed/content/unchanged/edge_added/edge_dropped。
//
// DiffResult is the full result of one inter-version diff. Counts always
// contains every key (zero-filled) for direct changeset column mapping.
type DiffResult struct {
	Nodes  []NodeChange   `json:"nodes"`
	Edges  []EdgeChange   `json:"edges"`
	Counts map[string]int `json:"counts"`
}

// countKeys 是 Counts 的固定键集（与 DiffResult 文档一致）。
var countKeys = []string{
	"added", "deleted", "merged", "split", "migrated",
	"renamed", "content", "unchanged", "edge_added", "edge_dropped",
}

// categoryRank 定义 Nodes 切片的分类排序（分类内部按 id 升序，R-ev-5）。
var categoryRank = map[string]int{
	ChangeMergedInto: 0,
	ChangeSplitInto:  1,
	ChangeMigrated:   2,
	ChangeRenamed:    3,
	ChangeContent:    4,
	ChangeAdded:      5,
	ChangeDeleted:    6,
	ChangeUnchanged:  7,
}

// Diff 比对 from → to 两个 KG 版本，产出血缘感知的变更分类。
// from 可为 nil（v0 初次记录：to 的全部节点/边判为 added）。
// 只读访问两个库；分类与排序确定性，同输入同输出。
//
// Diff compares two KG versions from → to with lineage-aware classification.
// from may be nil (initial record: everything in to counts as added).
func Diff(ctx context.Context, from, to *storage.DB) (*DiffResult, error) {
	if to == nil {
		return nil, fmt.Errorf("observe.Diff: to 不可为 nil / to must not be nil")
	}

	// ── 装载 to 库（运行后终态；血缘也取自它）────────────────
	bList, err := storage.NewNodeRepo(to).ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("observe.Diff: 读取 to 节点失败: %w", err)
	}
	bMap := make(map[string]*dktypes.Node, len(bList))
	for _, n := range bList {
		if n != nil {
			bMap[n.ID] = n
		}
	}
	bEdges, err := storage.NewEdgeRepo(to).ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("observe.Diff: 读取 to 边失败: %w", err)
	}

	// ── 装载 from 库（可为 nil）──────────────────────────────
	aMap := map[string]*dktypes.Node{}
	var aEdges []*dktypes.Edge
	if from != nil {
		aList, err := storage.NewNodeRepo(from).ListAll(ctx)
		if err != nil {
			return nil, fmt.Errorf("observe.Diff: 读取 from 节点失败: %w", err)
		}
		for _, n := range aList {
			if n != nil {
				aMap[n.ID] = n
			}
		}
		aEdges, err = storage.NewEdgeRepo(from).ListAll(ctx)
		if err != nil {
			return nil, fmt.Errorf("observe.Diff: 读取 from 边失败: %w", err)
		}
	}

	// ── 血缘目标全集：merge 幸存者 / split 产物不算「新增」────
	// from==nil（v0 初次）时不做排除——全部判 added（R-ev-3 语义只对版本间生效）。
	lineageTargets := map[string]bool{}
	linTo := storage.NewUUIDLineageRepo(to)
	if from != nil {
		rows, err := linTo.ListAll(ctx)
		if err != nil {
			return nil, fmt.Errorf("observe.Diff: 读取 to 血缘失败: %w", err)
		}
		for _, l := range rows {
			lineageTargets[l.NewUUID] = true
		}
	}

	// 人读名称表（跨两个版本，供边事件展示）。
	names := map[string]string{}
	for id, n := range bMap {
		names[id] = n.Name
	}
	for id, n := range aMap {
		if _, ok := names[id]; !ok {
			names[id] = n.Name
		}
	}

	res := &DiffResult{Counts: map[string]int{}, Nodes: []NodeChange{}, Edges: []EdgeChange{}}
	for _, k := range countKeys {
		res.Counts[k] = 0
	}

	// ── 节点分类：from 侧出发（存活判归属/改名/内容；消失查血缘）──
	for _, id := range sortedKeys(aMap) {
		a := aMap[id]
		if b, ok := bMap[id]; ok {
			res.Nodes = append(res.Nodes, classifySurvivor(a, b))
			continue
		}
		res.Nodes = append(res.Nodes, classifyGone(ctx, linTo, bMap, names, a))
	}
	// to 侧新增（排除血缘目标）。
	for _, id := range sortedKeys(bMap) {
		if _, ok := aMap[id]; ok {
			continue
		}
		if lineageTargets[id] {
			continue
		}
		res.Nodes = append(res.Nodes, NodeChange{ID: id, Name: bMap[id].Name, Change: ChangeAdded})
	}

	// ── 边分类：三元组对齐 ───────────────────────────────────
	aEdgeSet := edgeSetOf(aEdges)
	bEdgeSet := edgeSetOf(bEdges)
	for _, k := range sortedEdgeKeys(bEdgeSet) {
		if !aEdgeSet[k] {
			res.Edges = append(res.Edges, EdgeChange{
				Source: k.source, Target: k.target, Kind: k.kind, Change: EdgeAdded,
				SourceName: names[k.source], TargetName: names[k.target],
			})
		}
	}
	for _, k := range sortedEdgeKeys(aEdgeSet) {
		if !bEdgeSet[k] {
			res.Edges = append(res.Edges, EdgeChange{
				Source: k.source, Target: k.target, Kind: k.kind, Change: EdgeDropped,
				SourceName: names[k.source], TargetName: names[k.target],
			})
		}
	}

	// ── 汇总计数 + 分类内 id 升序稳定排序 ────────────────────
	for _, nc := range res.Nodes {
		res.Counts[countKeyOf(nc.Change)]++
	}
	for _, ec := range res.Edges {
		res.Counts[countKeyOf("edge_"+ec.Change)]++
	}
	sort.Slice(res.Nodes, func(i, j int) bool {
		ri, rj := categoryRank[res.Nodes[i].Change], categoryRank[res.Nodes[j].Change]
		if ri != rj {
			return ri < rj
		}
		return res.Nodes[i].ID < res.Nodes[j].ID
	})
	sort.Slice(res.Edges, func(i, j int) bool {
		ci, cj := res.Edges[i].Change, res.Edges[j].Change
		if ci != cj {
			return ci < cj // added 先于 dropped（与规格枚举序一致）
		}
		ei, ej := res.Edges[i], res.Edges[j]
		if ei.Source != ej.Source {
			return ei.Source < ej.Source
		}
		if ei.Target != ej.Target {
			return ei.Target < ej.Target
		}
		return ei.Kind < ej.Kind
	})
	return res, nil
}

// classifySurvivor 分类「两个版本都存在」的节点：
// 归属变 → migrated；name 变 → renamed；summary/description/confidence 变 → content；否则 unchanged。
func classifySurvivor(a, b *dktypes.Node) NodeChange {
	if a.Domain != b.Domain || a.Subdomain != b.Subdomain {
		return NodeChange{ID: a.ID, Name: b.Name, Change: ChangeMigrated, Detail: MigratedDetail{
			FromDomain: a.Domain, ToDomain: b.Domain,
			FromSubdomain: a.Subdomain, ToSubdomain: b.Subdomain,
		}}
	}
	if a.Name != b.Name {
		return NodeChange{ID: a.ID, Name: b.Name, Change: ChangeRenamed,
			Detail: RenamedDetail{OldName: a.Name, NewName: b.Name}}
	}
	var fields []string
	if a.Summary != b.Summary {
		fields = append(fields, "summary")
	}
	if a.Description != b.Description {
		fields = append(fields, "description")
	}
	if a.Confidence != b.Confidence {
		fields = append(fields, "confidence")
	}
	if len(fields) > 0 {
		return NodeChange{ID: a.ID, Name: b.Name, Change: ChangeContent, Detail: ContentDetail{Fields: fields}}
	}
	return NodeChange{ID: a.ID, Name: b.Name, Change: ChangeUnchanged}
}

// classifyGone 分类「from 有、to 无」的节点：查 to 库血缘链的终端后继，
// 唯一存活 → merged_into；多个存活 → split_into；无存活 → deleted（R-ev-3 的灵魂）。
func classifyGone(ctx context.Context, linTo *storage.UUIDLineageRepo, bMap map[string]*dktypes.Node, names map[string]string, a *dktypes.Node) NodeChange {
	succ, err := linTo.ResolveSuccessors(ctx, a.ID)
	if err != nil || succ == nil {
		succ = []string{a.ID} // 无血缘记录 → 自身即终端（与 ResolveSuccessors 语义一致）
	}
	var live []string
	for _, u := range succ {
		if u == a.ID {
			continue
		}
		if _, ok := bMap[u]; ok {
			live = append(live, u)
		}
	}
	switch {
	case len(live) == 1:
		return NodeChange{ID: a.ID, Name: a.Name, Change: ChangeMergedInto,
			Detail: MergedIntoDetail{Into: live[0], IntoName: names[live[0]]}}
	case len(live) > 1:
		intoNames := make([]string, 0, len(live))
		for _, u := range live {
			intoNames = append(intoNames, names[u])
		}
		return NodeChange{ID: a.ID, Name: a.Name, Change: ChangeSplitInto,
			Detail: SplitIntoDetail{Into: live, IntoNames: intoNames}}
	default:
		return NodeChange{ID: a.ID, Name: a.Name, Change: ChangeDeleted}
	}
}

// countKeyOf 把变更类别映射到 Counts 键（merged_into→merged，edge_dropped→edge_dropped…）。
func countKeyOf(change string) string {
	switch change {
	case ChangeMergedInto:
		return "merged"
	case ChangeSplitInto:
		return "split"
	default:
		return change
	}
}

// edgeKey 是边对齐三元组。
type edgeKey struct{ source, target, kind string }

func edgeSetOf(edges []*dktypes.Edge) map[edgeKey]bool {
	set := make(map[edgeKey]bool, len(edges))
	for _, e := range edges {
		if e != nil {
			set[edgeKey{e.SourceID, e.TargetID, string(e.Kind)}] = true
		}
	}
	return set
}

func sortedKeys(m map[string]*dktypes.Node) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedEdgeKeys(set map[edgeKey]bool) []edgeKey {
	out := make([]edgeKey, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].source != out[j].source {
			return out[i].source < out[j].source
		}
		if out[i].target != out[j].target {
			return out[i].target < out[j].target
		}
		return out[i].kind < out[j].kind
	})
	return out
}

// JSON 序列化 DiffResult（确定性：切片已稳定排序，map 键按字典序输出）。
//
// JSON serializes the result deterministically.
func (d *DiffResult) JSON() ([]byte, error) {
	return json.Marshal(d)
}

// Markdown 生成人读摘要（终端摘要与 changeset 事件流共用）。
// 分类顺序与 categoryRank 一致；unchanged 只报计数不逐行列举。
//
// Markdown renders a human-readable summary of the diff.
func (d *DiffResult) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "节点: +%d -%d 合并 %d 拆分 %d 迁移 %d 改名 %d 内容 %d（不变 %d）| 边: +%d -%d\n",
		d.Counts["added"], d.Counts["deleted"], d.Counts["merged"], d.Counts["split"],
		d.Counts["migrated"], d.Counts["renamed"], d.Counts["content"], d.Counts["unchanged"],
		d.Counts["edge_added"], d.Counts["edge_dropped"])

	sections := []struct {
		change string
		title  string
	}{
		{ChangeMergedInto, "🟡 合并 / Merged"},
		{ChangeSplitInto, "🟡 拆分 / Split"},
		{ChangeMigrated, "🔵 迁移 / Migrated"},
		{ChangeRenamed, "🔵 改名 / Renamed"},
		{ChangeContent, "🟣 内容变更 / Content"},
		{ChangeAdded, "🟢 新增 / Added"},
		{ChangeDeleted, "🔴 删除 / Deleted"},
	}
	for _, sec := range sections {
		var lines []string
		for _, nc := range d.Nodes {
			if nc.Change == sec.change {
				lines = append(lines, nodeLine(nc))
			}
		}
		if len(lines) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n%s (%d)\n", sec.title, len(lines))
		for _, l := range lines {
			b.WriteString("  " + l + "\n")
		}
	}

	if d.Counts["edge_added"]+d.Counts["edge_dropped"] > 0 {
		fmt.Fprintf(&b, "\n边变更 / Edges (+%d -%d)\n", d.Counts["edge_added"], d.Counts["edge_dropped"])
		for _, ec := range d.Edges {
			mark := "🟢 +"
			if ec.Change == EdgeDropped {
				mark = "🔴 -"
			}
			fmt.Fprintf(&b, "  %s %s --%s--> %s\n", mark, displayName(ec.SourceName, ec.Source), ec.Kind, displayName(ec.TargetName, ec.Target))
		}
	}
	return b.String()
}

// nodeLine 渲染单条节点事件。
func nodeLine(nc NodeChange) string {
	switch d := nc.Detail.(type) {
	case MergedIntoDetail:
		return fmt.Sprintf("%s (%s) → 合并进 %s (%s)", nc.Name, nc.ID, d.IntoName, d.Into)
	case SplitIntoDetail:
		parts := make([]string, 0, len(d.Into))
		for i, u := range d.Into {
			parts = append(parts, fmt.Sprintf("%s (%s)", d.IntoNames[i], u))
		}
		return fmt.Sprintf("%s (%s) → 拆分为 %s", nc.Name, nc.ID, strings.Join(parts, ", "))
	case MigratedDetail:
		return fmt.Sprintf("%s (%s): %s/%s → %s/%s", nc.Name, nc.ID,
			d.FromDomain, d.FromSubdomain, d.ToDomain, d.ToSubdomain)
	case RenamedDetail:
		return fmt.Sprintf("%s → %s (%s)", d.OldName, d.NewName, nc.ID)
	case ContentDetail:
		return fmt.Sprintf("%s (%s): %s", nc.Name, nc.ID, strings.Join(d.Fields, ", "))
	default:
		return fmt.Sprintf("%s (%s)", nc.Name, nc.ID)
	}
}

// displayName 优先展示名称，缺省退回 uuid。
func displayName(name, id string) string {
	if name != "" {
		return name
	}
	return id
}
