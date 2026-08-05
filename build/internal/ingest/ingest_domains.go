// Package storage 提供领域知识层的持久化存储层实现。
//
// 本文件包含「双层图」物化入库路径（Task #7）：把 internal/extract 产出的
// extract.Result（domain/subdomain/entity/concept + entity↔entity 语义关系 +
// domain/entity 的 source_skills）物化为契约（docs/design/domain-extraction-contract.md
// 第一节双层模型、方案 B）定义的一等节点图：
//
//	skill --provides--> domain --composes--> subdomain --composes--> entity/concept
//	                                                       entity <--语义关系--> entity
//
// 关键设计：这是一条【独立于既有 Ingest（annotation→storage）的新路径】，不改动
// 既有函数与测试；仅复用底层仓储（NodeRepo/EdgeRepo）与 extract 的转换方法（DRY）。
// 所有边在插入前都做「应用层外键」两端存在性校验，缺失端点跳过并计数（EdgesSkipped），
// 绝不产生悬空边——沿用 ingest.go 已修复的正确性约束。
//
// Package storage implements the persistence layer for the Cairn.
//
// This file contains the two-layer graph materialization path (Task #7). It turns
// the extract.Result produced by internal/extract into the first-class node graph
// defined by the contract (Plan B): skill --provides--> domain --composes-->
// subdomain --composes--> entity/concept, plus entity↔entity semantic relations.
//
// This is an INDEPENDENT path, separate from the existing annotation→storage Ingest;
// it does not modify existing functions or tests. It reuses the underlying repos
// (NodeRepo/EdgeRepo) and extract's conversion helpers (DRY). Every edge is checked
// with an application-level FK existence guard before insertion; edges with a missing
// endpoint are skipped and counted (EdgesSkipped), never producing dangling edges —
// the same correctness invariant fixed in ingest.go.
package ingest

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/xcosmosbox/cairn/core/dktypes"
	"github.com/xcosmosbox/cairn/core/storage"
	"github.com/xcosmosbox/cairn/build/internal/extract"
)

// ——————————————————————————————————————————————————————————————————————————————
// 双层节点 ID 方案 / Two-layer node ID scheme
// ——————————————————————————————————————————————————————————————————————————————
//
// entity/concept 使用 UUID 主键（由 extract.AssignNodeUUIDs 在所有 LLM 阶段之后分配），
// 入库时直接用 node.ID。skill/domain/subdomain 三类「层节点」使用类型前缀 ID，保证每段
// 非空且全局唯一——尤其绝不出现空段（历史悬空 bug 的根因是用空子域拼出 "domain::::entity"）。
// 层节点 ID 通过下列构造器显式拼装，段永不为空。
//
// Entities/concepts use UUID primary keys (assigned by extract.AssignNodeUUIDs after
// all LLM stages); ingest uses node.ID directly. The skill/domain/subdomain "layer
// nodes" use type-prefixed IDs that keep every segment non-empty and globally unique
// — never an empty segment (the historical dangling bug came from building
// "domain::::entity" with an empty subdomain). Layer-node IDs are assembled explicitly below.

// skillNodeID 构造 skill 节点 ID："skill::<skill>"。
// skill 名（来自 SKILL.md / source_skills）本身即稳定标识，直接作为段使用，
// 保证 provides 边两端引用同一 ID（一致性）。
// skillNodeID builds a skill node ID: "skill::<skill>".
func skillNodeID(skill string) string {
	return "skill::" + skill
}

// domainNodeID 构造 domain 节点 ID："domain::<domainSlug>"。
// domainNodeID builds a domain node ID: "domain::<domainSlug>".
func domainNodeID(domainSlug string) string {
	return "domain::" + domainSlug
}

// subdomainNodeID 构造 subdomain 节点 ID："subdomain::<domainSlug>::<subSlug>"。
// 带上父 domainSlug 前缀，保证不同 domain 下同名子域不冲突。
// subdomainNodeID builds a subdomain node ID: "subdomain::<domainSlug>::<subSlug>".
func subdomainNodeID(domainSlug, subSlug string) string {
	return "subdomain::" + domainSlug + "::" + subSlug
}

// ——————————————————————————————————————————————————————————————————————————————
// 输入 / 输出 / Input / Output
// ——————————————————————————————————————————————————————————————————————————————

// SkillMeta 是单个 skill 的 SKILL.md 元信息（skill 名 / 摘要 / 描述），
// 用于物化 skill 节点。它可选：source_skills 里出现、但未提供 SkillMeta 的 skill
// 仍会被物化为节点（以 skill 名兜底 Summary），保证 provides 边端点存在。
//
// SkillMeta is the SKILL.md metadata (name/summary/description) for one skill,
// used to materialize skill nodes. It is optional: any skill appearing in
// source_skills without a SkillMeta is still materialized (Summary falls back to
// the skill name), guaranteeing the provides edge endpoint exists.
type SkillMeta struct {
	// Name 是 skill 名称，必须与 source_skills 中的名称一致（用于匹配）。
	// Name is the skill name; must match the name used in source_skills.
	Name string
	// Summary 是 skill 的一句话摘要（来自 SKILL.md）。
	// Summary is a one-line summary of the skill (from SKILL.md).
	Summary string
	// Description 是 skill 的详细描述（来自 SKILL.md）。
	// Description is the detailed description of the skill (from SKILL.md).
	Description string
}

// MemberSource 描述一个 04 标注单元（member）的来源文档位置。
// 它是回写与 node_sources 表写入的输入：一个 04 id 可能来自多个文档（同名单元跨文档
// 出现），故 MemberSources 的值是 []MemberSource，每个来源写一行 node_sources。
//
// MemberSource describes the source-document location of one 04 annotated unit
// (member). A 04 id may originate from multiple documents (same-name units across
// docs), so MemberSources maps a member id to a []MemberSource; each entry writes
// one node_sources row.
type MemberSource struct {
	// Skill 是该 04 id 来源的 skill 名。
	// Skill is the skill the 04 id originated from.
	Skill string
	// FilePath 是来源文档相对仓库根的路径。
	// FilePath is the source doc path relative to the repo root.
	FilePath string
	// StartLine / EndLine 是该单元在来源文档中的原文行区间（1-based 闭区间）；
	// 0 表示 span 缺失（仅退化排序，不阻塞回写）。
	// StartLine / EndLine are the 1-based inclusive span; 0 means no span.
	StartLine int
	EndLine   int
}

// IngestDomainsOptions 定义双层物化入库的参数。
// IngestDomainsOptions defines the parameters for two-layer materialization.
type IngestDomainsOptions struct {
	// Result 是提取器（Task #6）的产出。不可为 nil。
	// Result is the extractor output (Task #6). Must not be nil.
	Result *extract.Result
	// Skills 是可选的 SKILL.md 元信息列表，用于丰富 skill 节点内容。
	// Skills is an optional list of SKILL.md metadata to enrich skill nodes.
	Skills []SkillMeta
	// MinConfidence 是 entity/concept 节点的置信度硬门控阈值（与既有 Ingest 一致）。
	// MinConfidence is the confidence gate for entity/concept nodes.
	MinConfidence float64
	// MemberSources 是 04 id → 来源文档位置列表的映射，由 orchestrator 从
	// AssignIDs 后的 []*dktypes.AnnotatedDocument 构建，供 ingest 为每个 entity/concept
	// node 的每个 member 写一行 node_sources（回写与 shared 判定的命脉）。
	// nil 时跳过 node_sources 写入（向后兼容；全量重建场景应总是传入）。
	//
	// MemberSources maps a 04 id to its source-document locations, built by the
	// orchestrator from AssignIDs output. For each entity/concept node, ingest writes
	// one node_sources row per member per source. nil skips node_sources writes.
	MemberSources map[string][]MemberSource
}

// IngestDomainsResult 描述一次双层物化入库的结果摘要（可观测）。
// IngestDomainsResult summarizes a two-layer materialization (observability).
type IngestDomainsResult struct {
	// 各类节点物化数量 / per-type node counts.
	SkillNodes     int
	DomainNodes    int
	SubdomainNodes int
	EntityNodes    int
	ConceptNodes   int

	// 各类边物化数量 / per-type edge counts.
	ProvidesEdges int // skill --provides--> domain
	ComposesEdges int // domain/subdomain 层级组成边（自动物化）
	SemanticEdges int // entity↔entity 语义关系（LLM 产出）

	// NodesInserted / EdgesInserted 是节点 / 边入库总数。
	// NodesInserted / EdgesInserted are the total nodes / edges inserted.
	NodesInserted int
	EdgesInserted int

	// EdgesSkipped 是因端点节点不存在而被跳过的边数量（应用层外键，可观测）。
	// EdgesSkipped counts edges skipped due to a missing endpoint (app-level FK).
	EdgesSkipped int

	// NodeSourcesInserted 是写入 node_sources 表的行数（每个 node 的每个 member
	// 每个来源文档一行；member 不在 MemberSources 中则跳过该 member，不报错）。
	// 0 表示未写入（MemberSources 为 nil 或无命中）。
	//
	// NodeSourcesInserted counts rows written to node_sources (one per node member
	// per source doc). 0 when MemberSources is nil or no member matched.
	NodeSourcesInserted int
}

// ——————————————————————————————————————————————————————————————————————————————
// IngestDomains — 双层物化入库主流程
// ——————————————————————————————————————————————————————————————————————————————

// IngestDomains 把 extract.Result 物化为方案 B 的双层一等节点图。
//
// 严格的「先节点、后边」顺序，保证任何边插入时两端节点均已入库（正确性前提）：
//  1. 物化 skill 节点（来自 source_skills 并集 + 可选 SKILL.md 元信息）
//  2. 物化 domain 节点（source_refs 回填 domain.source_skills 逗号拼接）
//  3. 物化 subdomain 节点
//  4. 物化 entity/concept 节点（置信度门控；source_refs 回填 entity.source_skills）
//  5. 物化 provides 边：skill --provides--> domain（多对多，端点存在性校验）
//  6. 物化 composes 层级边：domain->subdomain、subdomain->entity/concept（自动生成）
//  7. 物化 entity↔entity 语义边（LLM 产出，kind 限 6 枚举；端点存在性校验）
//  8. 重建 FTS 索引 + bump kb_version
//
// 所有边插入前复用「应用层外键」两端存在性校验（见 ingest.go 注释），缺失端点跳过并
// 计入 EdgesSkipped，绝不产生悬空边。provenance 一律 llm_inferred（契约决策 4，不新增枚举）。
//
// IngestDomains materializes an extract.Result into the Plan-B two-layer graph.
// It strictly inserts nodes before edges so every edge's endpoints already exist,
// reusing the application-level FK existence check for all edges (missing endpoint
// → skipped, counted in EdgesSkipped, never a dangling edge). Provenance is always
// llm_inferred (contract decision 4).
func IngestDomains(ctx context.Context, db *storage.DB, opts IngestDomainsOptions) (*IngestDomainsResult, error) {
	if opts.Result == nil {
		return nil, fmt.Errorf("IngestDomains: result is nil")
	}

	nodeRepo := storage.NewNodeRepo(db)
	edgeRepo := storage.NewEdgeRepo(db)
	nsRepo := storage.NewNodeSourceRepo(db)
	ftsIdx := storage.NewFTSIndex(db)
	verRepo := storage.NewKBVersionRepo(db)

	result := &IngestDomainsResult{}
	now := time.Now().UTC()

	// inserted 跟踪已入库节点 ID，避免 LLM 产出重复 ID 触发主键冲突（防御）。
	// 同时它就是后续边端点存在性校验的「本地视图」——但为绝对正确，边校验仍走
	// NodeRepo.GetByID 落库查询（与 ingest.go 一致），本地集合仅用于节点去重。
	// inserted tracks already-persisted node IDs to avoid PK conflicts on duplicate
	// LLM ids. Edge checks still go through NodeRepo.GetByID for correctness.
	inserted := make(map[string]bool)

	// insertNode 是节点入库的统一封装：去重 + 计数 + 时间戳。
	// insertNode is the unified node-insert helper: dedup + count + timestamps.
	insertNode := func(n *dktypes.Node) (bool, error) {
		if inserted[n.ID] {
			return false, nil // 已入库同 ID → 跳过（防御去重）/ dedup
		}
		n.CreatedAt = now
		n.UpdatedAt = now
		n.Provenance = dktypes.ProvenanceLLMInferred
		if err := nodeRepo.Insert(ctx, n); err != nil {
			return false, err
		}
		inserted[n.ID] = true
		return true, nil
	}

	// insertEdge 是边入库的统一封装：应用层外键（两端存在性）→ 计数。
	// 返回 (inserted bool)：false 表示端点缺失被跳过（已计入 EdgesSkipped）。
	// insertEdge is the unified edge-insert helper enforcing the app-level FK.
	insertEdge := func(sourceID, targetID string, kind dktypes.RelationKind, desc string, conf float64, srcRefs string) (bool, error) {
		// 端点存在性校验（应用层外键）：两端必须都是已入库真实节点。
		// Existence check (application-level FK): both endpoints must be real nodes.
		srcNode, err := nodeRepo.GetByID(ctx, sourceID)
		if err != nil {
			return false, fmt.Errorf("check edge source %s: %w", sourceID, err)
		}
		tgtNode, err := nodeRepo.GetByID(ctx, targetID)
		if err != nil {
			return false, fmt.Errorf("check edge target %s: %w", targetID, err)
		}
		if srcNode == nil || tgtNode == nil {
			// 端点缺失 → 跳过悬空边，计数 + 日志以保证可观测（绝不入库悬空边）。
			// Missing endpoint → skip dangling edge, count + log for observability.
			missing := "source"
			if srcNode != nil {
				missing = "target"
			}
			log.Printf("[ingest] 跳过悬空边: %s --%s--> %s (缺失 %s 端点)", sourceID, kind, targetID, missing)
			result.EdgesSkipped++
			return false, nil
		}
		edge := &dktypes.Edge{
			SourceID:   sourceID,
			TargetID:   targetID,
			Kind:       kind,
			Description: desc,
			Confidence: conf,
			Provenance: dktypes.ProvenanceLLMInferred,
			SourceRefs: srcRefs,
			CreatedAt:  now,
		}
		if err := edgeRepo.Insert(ctx, edge); err != nil {
			return false, fmt.Errorf("insert edge %s->%s: %w", sourceID, targetID, err)
		}
		result.EdgesInserted++
		return true, nil
	}

	// nodeSourceRows 收集所有 node_sources 行，最后统一批量写入。
	// 收集而非逐行写，避免在嵌套循环里频繁开事务（性能 + 简洁）。
	// nodeSourceRows accumulates node_sources rows for a single batch insert at the end.
	var nodeSourceRows []storage.NodeSource

	// appendNodeSources 把一个 entity/concept node 的 members 展开为 node_sources 行：
	// 对 node 的每个 member 查 MemberSources map，对每条来源写一行。
	// member 不在 map 中则跳过该 member（不报错——防御 repair/supplement 新建节点
	// 引用的边界情况，R5 同源精神：宁可少写也不误报）。
	//
	// appendNodeSources expands a node's members into node_sources rows: for each
	// member it looks up MemberSources and writes one row per source. A member
	// absent from the map is skipped silently (defensive: repair/supplement may
	// reference edge-case members).
	appendNodeSources := func(nodeID string, members []string, dSlug, sSlug string) {
		if len(opts.MemberSources) == 0 {
			return // 无映射 → 不写（向后兼容）
		}
		for _, m := range members {
			if m == "" {
				continue
			}
			sources, ok := opts.MemberSources[m]
			if !ok {
				continue // member 不在 map 中：跳过，不报错
			}
			for _, src := range sources {
				nodeSourceRows = append(nodeSourceRows, storage.NodeSource{
					NodeUUID:  nodeID,
					MemberID:  m,
					Skill:     src.Skill,
					FilePath:  src.FilePath,
					StartLine: src.StartLine,
					EndLine:   src.EndLine,
				})
			}
		}
	}

	// ————————————————————————————————————————————————————————————————————————
	// 步骤 1：物化 skill 节点 / Step 1: materialize skill nodes
	// ————————————————————————————————————————————————————————————————————————
	//
	// skill 集合 = 所有 domain.source_skills 的并集（保证 provides 边端点存在）
	//            ∪ 显式传入的 SKILL.md 元信息（丰富节点内容）。
	// 先建 skill 节点，provides 边（步骤 5）才有合法的源端点。
	skillMetaByName := make(map[string]SkillMeta, len(opts.Skills))
	for _, sm := range opts.Skills {
		if sm.Name != "" {
			skillMetaByName[sm.Name] = sm
		}
	}
	skillOrder := collectSkillNames(opts.Result, opts.Skills)
	for _, skill := range skillOrder {
		meta := skillMetaByName[skill] // 零值兜底 / zero value if absent
		summary := meta.Summary
		if strings.TrimSpace(summary) == "" {
			// Summary 列 NOT NULL：无 SKILL.md 摘要时以 skill 名兜底。
			// Summary is NOT NULL: fall back to the skill name when no SKILL.md summary.
			summary = skill
		}
		name := meta.Name
		if name == "" {
			name = skill
		}
		node := &dktypes.Node{
			ID:          skillNodeID(skill),
			Label:       dktypes.LabelSkill,
			Name:        name,
			Summary:     summary,
			Description: meta.Description,
			// skill 节点自成一个「域」，便于看板按 skill 分组（Task #9）。
			// A skill node forms its own "domain" for board grouping (Task #9).
			Domain:    skill,
			Subdomain: "",
		}
		ok, err := insertNode(node)
		if err != nil {
			return nil, fmt.Errorf("insert skill node %s: %w", node.ID, err)
		}
		if ok {
			result.SkillNodes++
			result.NodesInserted++
		}
	}

	// ————————————————————————————————————————————————————————————————————————
	// 步骤 2-7：逐 domain 物化 domain/subdomain/entity/concept 节点与三类边。
	// Steps 2-7: per-domain materialization of nodes and the three edge kinds.
	// ————————————————————————————————————————————————————————————————————————
	for di := range opts.Result.Domains {
		d := &opts.Result.Domains[di]
		dSlug := d.Slug
		if dSlug == "" {
			// 防御：Slug 理应由 extract.parseAndValidate 填充；缺失则跳过该 domain。
			// Defensive: Slug should be set by extract; skip the domain if absent.
			continue
		}
		domainID := domainNodeID(dSlug)

		// 步骤 2：domain 节点。source_refs 回填 domain.source_skills（逗号拼接），
		// 支撑「domain → skills」反向查询（契约第一节双向查询）。
		// Step 2: domain node; source_refs backfilled from domain.source_skills.
		domainSrcRefs := strings.Join(d.SourceSkills, ",")
		dd := d.ToDomainDef() // 复用 extract 的转换方法（DRY）/ reuse extract conversion
		domainNode := &dktypes.Node{
			ID:          domainID,
			Label:       dktypes.LabelDomain,
			Name:        dd.Name,
			Summary:     firstNonEmpty(dd.Summary, dd.Name),
			Description: buildDomainDescription(d),
			Domain:      dSlug,
			Subdomain:   "",
			SourceRefs:  domainSrcRefs,
		}
		if ok, err := insertNode(domainNode); err != nil {
			return nil, fmt.Errorf("insert domain node %s: %w", domainID, err)
		} else if ok {
			result.DomainNodes++
			result.NodesInserted++
		}

		// 步骤 5（provides，端点已就绪：skill 步骤 1 已建、domain 刚建）：
		// skill --provides--> domain，多对多，来自 domain.source_skills。
		// Step 5 (provides): skill --provides--> domain (many-to-many).
		for _, skill := range d.SourceSkills {
			ok, err := insertEdge(skillNodeID(skill), domainID, dktypes.KindProvides,
				fmt.Sprintf("skill %s 贡献了领域 %s", skill, dd.Name), 1.0, skill)
			if err != nil {
				return nil, err
			}
			if ok {
				result.ProvidesEdges++
			}
		}

		// 步骤 3/4/6/7：逐 subdomain。
		for si := range d.Subdomains {
			sd := &d.Subdomains[si]
			sSlug := sd.Slug
			if sSlug == "" {
				continue // 防御：无 slug 的子域跳过 / skip subdomain without slug
			}
			subID := subdomainNodeID(dSlug, sSlug)

			// 步骤 3：subdomain 节点。
			// Step 3: subdomain node.
			subNode := &dktypes.Node{
				ID:          subID,
				Label:       dktypes.LabelSubdomain,
				Name:        sd.Name,
				Summary:     firstNonEmpty(sd.Summary, sd.Name),
				Description: sd.Description,
				Domain:      dSlug,
				Subdomain:   sSlug,
			}
			if ok, err := insertNode(subNode); err != nil {
				return nil, fmt.Errorf("insert subdomain node %s: %w", subID, err)
			} else if ok {
				result.SubdomainNodes++
				result.NodesInserted++
			}

			// 步骤 6a：domain --composes--> subdomain（层级边，自动物化，端点已就绪）。
			// Step 6a: domain --composes--> subdomain (auto hierarchical edge).
			if ok, err := insertEdge(domainID, subID, dktypes.KindComposes,
				"领域包含子域", 1.0, ""); err != nil {
				return nil, err
			} else if ok {
				result.ComposesEdges++
			}

			// 步骤 4：entity/concept 节点（置信度门控；source_refs 回填 source_skills）。
			// 先全部入库，其后再连 composes / 语义边，保证端点存在。
			// Step 4: entity/concept nodes (confidence-gated; source_refs backfilled).
			for _, ent := range sd.Entities {
				if ent.Confidence < opts.MinConfidence {
					continue
				}
				entID := ent.ID // UUID 主键（由 extract.AssignNodeUUIDs 分配）
				ed := ent.ToEntityDef()                    // 复用 extract 转换（DRY）
			node := &dktypes.Node{
				ID:          entID,
				Label:       dktypes.LabelEntity,
				Name:        ed.Name,
				Summary:     firstNonEmpty(ed.Summary, ed.Name),
				Description: ed.Description,
				Synonyms:    strings.Join(ent.Synonyms, " "),
				Domain:      dSlug,
				Subdomain:   sSlug,
				Confidence:  ent.Confidence,
				SourceRefs:  strings.Join(ent.SourceSkills, ","),
				FileSlug:    ent.FileSlug,
			}
			if ok, err := insertNode(node); err != nil {
				return nil, fmt.Errorf("insert entity node %s: %w", entID, err)
			} else if ok {
				result.EntityNodes++
				result.NodesInserted++
				// 为该 entity 的每个 member 写 node_sources（回写与 shared 判定的命脉）。
				// Write node_sources rows for this entity's members (write-back lifeline).
				appendNodeSources(entID, ent.Members, dSlug, sSlug)
				// 步骤 6b：subdomain --composes--> entity（层级边）。
				if ok2, err := insertEdge(subID, entID, dktypes.KindComposes,
					"子域包含实体", 1.0, ""); err != nil {
					return nil, err
				} else if ok2 {
					result.ComposesEdges++
				}
			}
			}
			for _, con := range sd.Concepts {
				if con.Confidence < opts.MinConfidence {
					continue
				}
				conID := con.ID // UUID 主键（由 extract.AssignNodeUUIDs 分配）
				cd := con.ToConceptDef() // 复用 extract 转换（DRY）
			node := &dktypes.Node{
				ID:          conID,
				Label:       dktypes.LabelConcept,
				Name:        cd.Name,
				Summary:     firstNonEmpty(cd.Summary, cd.Name),
				Description: cd.Description,
				Synonyms:    strings.Join(con.Synonyms, " "),
				Domain:      dSlug,
				Subdomain:   sSlug,
				Confidence:  con.Confidence,
				SourceRefs:  strings.Join(con.SourceSkills, ","),
				FileSlug:    con.FileSlug,
			}
			if ok, err := insertNode(node); err != nil {
				return nil, fmt.Errorf("insert concept node %s: %w", conID, err)
			} else if ok {
				result.ConceptNodes++
				result.NodesInserted++
				// 为该 concept 的每个 member 写 node_sources（回写与 shared 判定的命脉）。
				// Write node_sources rows for this concept's members (write-back lifeline).
				appendNodeSources(conID, con.Members, dSlug, sSlug)
				// 步骤 6b：subdomain --composes--> concept（层级边）。
				if ok2, err := insertEdge(subID, conID, dktypes.KindComposes,
					"子域包含概念", 1.0, ""); err != nil {
					return nil, err
				} else if ok2 {
					result.ComposesEdges++
				}
			}
			}

			// 步骤 7：entity↔entity 语义边（LLM 产出）。source/target 已由 AssignNodeUUIDs
			// 翻译为 uuid（未命中翻译的端点保留原值，由 insertEdge 端点存在性校验跳过——R5）。
			// 两端存在性校验（应用层外键），缺失则跳过并计数——被门控过滤掉的实体、或指向
			// 不存在实体的关系，都在此安全跳过，绝不产生悬空边。
			// Step 7: entity↔entity semantic edges; endpoints are already uuids (assigned by
			// AssignNodeUUIDs; unmapped endpoints left as-is, skipped by insertEdge — R5).
			for _, rel := range sd.Relations {
				srcID := rel.Source
				tgtID := rel.Target
				ok, err := insertEdge(srcID, tgtID, dktypes.RelationKind(rel.Kind),
					rel.Description, rel.Confidence, "")
				if err != nil {
					return nil, err
				}
				if ok {
					result.SemanticEdges++
				}
			}
		}
	}

	// ————————————————————————————————————————————————————————————————————————
	// 步骤 7.5：批量写入 node_sources（回写与 shared 判定的命脉）。
	// 每行 = 一个 node 的一个 member 的一处来源文档；member 不在 MemberSources 中
	// 的已在 appendNodeSources 内静默跳过。失败不阻断主入库（KG 已正确物化），
	// 仅记录错误与计数——node_sources 是回写产物输入，非 KG 正确性的一部分。
	// Step 7.5: batch-insert node_sources rows (write-back lifeline). A member
	// absent from MemberSources was already skipped silently. Failure does not
	// abort ingest (the KG is already materialized correctly).
	// ————————————————————————————————————————————————————————————————————————
	if len(nodeSourceRows) > 0 {
		if err := nsRepo.InsertBatch(ctx, nodeSourceRows); err != nil {
			log.Printf("[ingest] 写 node_sources 失败（不阻断入库）: %v", err)
		} else {
			result.NodeSourcesInserted = len(nodeSourceRows)
		}
	}

	// ————————————————————————————————————————————————————————————————————————
	// 步骤 8：重建全量 FTS 索引 + bump kb_version。
	// Step 8: rebuild the full FTS index + bump kb_version.
	// ————————————————————————————————————————————————————————————————————————
	if err := ftsIdx.RebuildAll(ctx); err != nil {
		return nil, fmt.Errorf("rebuild fts5: %w", err)
	}
	if _, err := verRepo.Bump(ctx); err != nil {
		return nil, fmt.Errorf("bump kb_version: %w", err)
	}

	return result, nil
}

// ——————————————————————————————————————————————————————————————————————————————
// 内部辅助 / Internal helpers
// ——————————————————————————————————————————————————————————————————————————————

// collectSkillNames 收集需要物化为节点的 skill 名（稳定顺序、去重）：
// 先取显式 SKILL.md 元信息顺序，再补齐 domain.source_skills 中出现但未提供元信息的 skill。
// 这样既保证 provides 边端点齐备，又让带元信息的 skill 优先。
//
// collectSkillNames returns the de-duplicated, stable-ordered set of skill names to
// materialize: explicit SkillMeta first, then any skill seen in source_skills.
func collectSkillNames(res *extract.Result, metas []SkillMeta) []string {
	seen := make(map[string]bool)
	var order []string
	add := func(s string) {
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		order = append(order, s)
	}
	for _, m := range metas {
		add(m.Name)
	}
	for i := range res.Domains {
		for _, s := range res.Domains[i].SourceSkills {
			add(s)
		}
	}
	return order
}

// buildDomainDescription 组合 domain 的 description 与 reusability_rationale。
// 契约第三节：reusability_rationale「落库时可存入 domain 节点的 description」。
// 二者都在时以分隔行拼接，保留复用性自证以供审计。
//
// buildDomainDescription combines the domain description with its reusability
// rationale (contract §3: the rationale may be stored in the domain node's description).
func buildDomainDescription(d *extract.Domain) string {
	desc := strings.TrimSpace(d.Description)
	rationale := strings.TrimSpace(d.ReusabilityRationale)
	switch {
	case rationale == "":
		return desc
	case desc == "":
		return "复用性理由 / Reusability: " + rationale
	default:
		return desc + "\n\n复用性理由 / Reusability: " + rationale
	}
}

// firstNonEmpty 返回首个非空字符串（用于 NOT NULL 列的兜底）。
// firstNonEmpty returns the first non-empty string (fallback for NOT NULL columns).
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
