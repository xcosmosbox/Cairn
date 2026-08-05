// Package writeback 把全量构建产出的 KG 结构化回写为 markdown + sidecar，
// 形成「人可编辑、可跟踪变化」的载体（增量闭环第一块地基）。
//
// 设计要点（决策总账铁律）：
//   - R4：relation 绝不回写（纯派生物，只在 KG）；description 会回写（node 的融合正文，人可编辑）。
//   - R6：整篇替换——回写生成的内容整篇覆盖写入 .md，人类原始散文被替换（原文由 git 历史留档）。
//   - R7：md 块中 name/归属/uuid/tag/shared 标为只读展示，仅 summary/description 标"（可编辑）"；
//     镜像块整体只读。
//
// 依赖：core/dktypes、core/storage、build/internal/extract；不产生 import 环。
//
// Package writeback materializes the KG produced by a full build into structured
// markdown + sidecar, forming the human-editable, change-trackable carrier
// (incremental-closure first block). R4: relations are never written back;
// R6: whole-document replace; R7: only summary/description are editable.
package writeback

import (
	"sort"

	"github.com/xcosmosbox/cairn/build/internal/extract"
)

// ——————————————————————————————————————————————————————————————————————————————
// 共享类型 / Shared types
// ——————————————————————————————————————————————————————————————————————————————

// MemberSource 描述一个 04 标注单元（member）的来源文档位置。
// 与 ingest.MemberSource 同构；writeback 包独立定义以避免反向依赖 ingest（import 环防护）。
//
// MemberSource describes the source-document location of one 04 annotated unit.
// Isomorphic to ingest.MemberSource; defined separately to avoid an import cycle.
type MemberSource struct {
	Skill     string
	FilePath  string
	StartLine int
	EndLine   int
}

// nodeView 是单个 entity/concept 融合节点的回写视图：从 extract.Result + MemberSources
// 派生而来，携带渲染所需的全部信息（uuid / tag / name / 归属 / summary / description /
// shared / span / members / 来源文档集合）。
//
// nodeView is the write-back view of one entity/concept fusion node, carrying all
// info needed for rendering.
type nodeView struct {
	UUID          string   // node.ID（UUID，由 AssignNodeUUIDs 分配）/ node UUID
	FileSlug     string   // LLM 生成的可读 slug（用于 _shared 文件名）/ LLM-generated readable slug
	Tag           string   // "Entity" | "Concept" / node label
	Name          string   // 节点中文名 / node name
	Domain        string   // domain name（展示用，非 slug）/ domain name (display)
	Subdomain     string   // subdomain name（展示用，非 slug）/ subdomain name (display)
	DomainSlug    string   // domain slug（用于 _shared 路径）/ domain slug (for _shared path)
	SubdomainSlug string   // subdomain slug
	Summary       string   // 可编辑 / editable
	Description   string   // 可编辑 / editable
	Members       []string // 04 id 列表（只读血缘）/ 04 ids (read-only lineage)
	Shared        bool     // 来源文档 ≥2 → true / ≥2 source docs → true
	SourceFiles   []string // distinct 来源 file_path（shared 判定 + 镜像渲染用）/ distinct source paths
	Span          *span    // 排序用（来自该 node 第一个命中 member 的 SourceSpan）/ for ordering
	// Provenance 是 sidecar 的 provenance 字段来源；空串回退 "llm_inferred"
	// （全量首建语义）。增量回写携带 KG 中的权威值（如 human_curated）。
	// Provenance feeds the sidecar's provenance field; empty falls back to
	// "llm_inferred" (full-build semantics). The incremental write-back carries
	// the KG's authoritative value (e.g. human_curated).
	Provenance string
}

// span 是排序用的原文位置快照（1-based 闭区间）。
// span is a 1-based inclusive source location snapshot for ordering.
type span struct {
	StartLine int
	EndLine   int
}

// nodeSourcesForNode 是某 node 的 members 在来源文档中的展开（用于 sidecar 的 span/members
// 与 shared 判定）。等价于 MemberSources 查表后的聚合。
//
// nodeSourcesForNode is the per-node expansion of its members' sources.
type nodeSourcesForNode struct {
	UUID    string
	Members []memberSourceRef // 该 node 每个 member 的所有来源
}

// memberSourceRef 是一个 member 的来源列表（一个 member 可来自多文档）。
type memberSourceRef struct {
	MemberID string
	Sources  []MemberSource
}

// Report 是一次回写的结果摘要（可观测）。
// Report summarizes one write-back run (observability).
type Report struct {
	// DocsWritten 是被整篇覆盖写入的 .md 文档数（不含 _shared primary）。
	// DocsWritten counts .md docs overwritten (excluding _shared primaries).
	DocsWritten int
	// NodesWritten 是渲染为完整块的 node 数（非 shared node 在来源文档的完整块）。
	// NodesWritten counts nodes rendered as full editable blocks (non-shared).
	NodesWritten int
	// SharedNodes 是 shared node 数（来源文档 ≥2）。
	// SharedNodes counts shared nodes (≥2 source docs).
	SharedNodes int
	// MirrorBlocks 是渲染的只读镜像块数（每个 shared node 在每个来源文档一个镜像块）。
	// MirrorBlocks counts read-only mirror blocks rendered.
	MirrorBlocks int
	// PrimaryFiles 是写入 _shared/<domain-slug>/<uuid>.md 的 primary 文件数。
	// PrimaryFiles counts _shared primary files written.
	PrimaryFiles int
	// SidecarsWritten 是写入的 sidecar 文件数（.md.kg.yaml，含 _shared primary）。
	// SidecarsWritten counts sidecar files written (incl. _shared primaries).
	SidecarsWritten int
}

// buildNodeViews 从 extract.Result + MemberSources 派生全部 entity/concept 节点的回写视图。
// 顺序：按 domain → subdomain → entity/concept 的原始顺序（后续渲染时再按 span 排序）。
//
// buildNodeViews derives write-back views for all entity/concept nodes.
func buildNodeViews(res *extract.Result, memberSources map[string][]MemberSource) []nodeView {
	if res == nil {
		return nil
	}
	var views []nodeView
	for di := range res.Domains {
		d := &res.Domains[di]
		for si := range d.Subdomains {
			sd := &d.Subdomains[si]
			for ni := range sd.Entities {
				views = append(views, makeNodeView(&sd.Entities[ni], "Entity",
					d.Name, d.Slug, sd.Name, sd.Slug, memberSources))
			}
			for ni := range sd.Concepts {
				views = append(views, makeNodeView(&sd.Concepts[ni], "Concept",
					d.Name, d.Slug, sd.Name, sd.Slug, memberSources))
			}
		}
	}
	return views
}

// makeNodeView 构造单个 node 的回写视图，含 shared 判定与来源文档聚合。
// makeNodeView builds one node's write-back view, including shared detection.
func makeNodeView(n *extract.Node, tag, domainName, domainSlug, subdomainName, subdomainSlug string,
	memberSources map[string][]MemberSource) nodeView {
	v := nodeView{
		UUID:          n.ID,
		FileSlug:      n.FileSlug,
		Tag:           tag,
		Name:          n.Name,
		Domain:        domainName,
		Subdomain:     subdomainName,
		DomainSlug:    domainSlug,
		SubdomainSlug: subdomainSlug,
		Summary:       n.Summary,
		Description:   n.Description,
		Members:       append([]string(nil), n.Members...),
	}
	// Members 排序，与 incremental 校验侧 buildNodeUpdate 的 sort.Strings 对齐：
	// 否则 cairn-ingest 全量回写的 sidecar Members（extract 原序）与 incremental 权威视图
	// （排序后）不一致 → SidecarMetadataMatches 误判 C3 → 首次增量全文档还原、覆盖人编辑。
	// Sort members to match buildNodeUpdate on the incremental-verify side; otherwise the
	// full-build sidecar (extract order) disagrees with the authoritative view (sorted)
	// and every first incremental run falsely classifies all docs as C3.
	sort.Strings(v.Members)

	// 聚合该 node 所有 members 的来源文档（distinct file_path），用于 shared 判定与镜像渲染。
	// Aggregate distinct source file_paths across all members for shared detection.
	fileSet := make(map[string]bool)
	var firstSpan *span
	for _, m := range n.Members {
		if m == "" {
			continue
		}
		sources, ok := memberSources[m]
		if !ok {
			continue
		}
		for _, src := range sources {
			if src.FilePath != "" {
				fileSet[src.FilePath] = true
			}
			// 取第一个有行的 source 作为排序 span（回写块排序用）。
			// Take the first source with lines as the ordering span.
			if firstSpan == nil && src.StartLine > 0 {
				firstSpan = &span{StartLine: src.StartLine, EndLine: src.EndLine}
			}
		}
	}
	for fp := range fileSet {
		v.SourceFiles = append(v.SourceFiles, fp)
	}
	// 稳定序：来源文档排序，保证输出确定。
	// Stable order for determinism.
	sortStrings(v.SourceFiles)
	v.Shared = len(v.SourceFiles) >= 2
	v.Span = firstSpan
	return v
}

// sortStrings 是一个稳定的字符串排序包装（避免在每个文件都 import sort）。
// sortStrings is a stable wrapper around sort.Strings.
func sortStrings(s []string) {
	// 局部排序 / local sort
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
