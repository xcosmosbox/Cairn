// Package dktypes 定义领域知识层（Domain Knowledge Layer）中所有跨包共享的核心类型、
// 枚举、数据结构及其校验方法。
//
// 本文件包含所有枚举类型定义及对应的 IsValid() 方法和遍历优先级方法。
package dktypes

// ——————————————————————————————————————————————————————————————————————————————
// Label — Node 标签枚举
// ——————————————————————————————————————————————————————————————————————————————

// Label 表示知识图谱中节点的标签类型。
// 双层架构（方案 B）下共有五类一等节点：
// skill（来源单元）→ domain（可复用领域）→ subdomain（子分支）→ entity/concept（具体知识点）。
//
// Label represents the label type of a node in the knowledge graph.
// Under the two-layer architecture (Plan B) there are five first-class node types:
// skill (source unit) → domain (reusable domain) → subdomain (branch) → entity/concept (concrete knowledge).
type Label string

const (
	// LabelEntity 表示该节点是一个具体的业务实体。
	// LabelEntity indicates the node is a concrete business entity.
	LabelEntity Label = "Entity"
	// LabelConcept 表示该节点是一个抽象的业务概念。
	// LabelConcept indicates the node is an abstract business concept.
	LabelConcept Label = "Concept"
	// LabelSkill 表示该节点是一个 skill 来源单元（来自 SKILL.md），本身不含领域知识。
	// LabelSkill indicates the node is a skill source unit (from SKILL.md); it carries no domain knowledge itself.
	LabelSkill Label = "Skill"
	// LabelDomain 表示该节点是一个可跨 skill 复用的领域（来自 reference/*.md）。
	// LabelDomain indicates the node is a domain reusable across skills (from reference/*.md).
	LabelDomain Label = "Domain"
	// LabelSubdomain 表示该节点是一个 domain 因体量过大分出的子分支。
	// LabelSubdomain indicates the node is a subdomain branched off from a domain that grew too large.
	LabelSubdomain Label = "Subdomain"
)

// IsValid 校验当前 Label 是否为合法枚举值。
// IsValid checks whether the current Label is a valid enum value.
func (l Label) IsValid() bool {
	switch l {
	case LabelEntity, LabelConcept, LabelSkill, LabelDomain, LabelSubdomain:
		return true
	default:
		return false
	}
}

// ——————————————————————————————————————————————————————————————————————————————
// RelationKind — 关系类型枚举
// ——————————————————————————————————————————————————————————————————————————————

// RelationKind 表示知识图谱中两个节点之间关系的语义类型。
// RelationKind represents the semantic type of a relationship between two nodes
// in the knowledge graph.
type RelationKind string

const (
	// KindTriggers 表示源节点"触发"目标节点。
	// KindTriggers indicates the source node triggers the target node.
	KindTriggers RelationKind = "triggers"
	// KindDependsOn 表示源节点"依赖"目标节点。
	// KindDependsOn indicates the source node depends on the target node.
	KindDependsOn RelationKind = "depends_on"
	// KindReferences 表示源节点"引用"目标节点。
	// KindReferences indicates the source node references the target node.
	KindReferences RelationKind = "references"
	// KindGeneralizes 表示源节点"泛化 / 抽象"目标节点。
	// KindGeneralizes indicates the source node generalizes (is an abstraction of) the target node.
	KindGeneralizes RelationKind = "generalizes"
	// KindComposes 表示源节点"组合"目标节点（整体-部分关系）。
	// KindComposes indicates the source node composes (is the whole of) the target node (part-of relationship).
	KindComposes RelationKind = "composes"
	// KindContradicts 表示源节点"矛盾"于目标节点。
	// KindContradicts indicates the source node contradicts the target node.
	KindContradicts RelationKind = "contradicts"
	// KindProvides 表示 skill 节点"提供"了某个 domain（skill → domain，多对多）。
	// 该 skill 的 reference 文档贡献了此 domain，是双层架构中支撑双向查询的来源边。
	// KindProvides indicates a skill node "provides" a domain (skill → domain, many-to-many).
	// The skill's reference docs contributed to this domain; it is the source edge
	// supporting bidirectional queries in the two-layer architecture.
	KindProvides RelationKind = "provides"
)

// IsValid 校验当前 RelationKind 是否为合法枚举值。
// IsValid checks whether the current RelationKind is a valid enum value.
func (k RelationKind) IsValid() bool {
	switch k {
	case KindTriggers, KindDependsOn, KindReferences, KindGeneralizes, KindComposes, KindContradicts, KindProvides:
		return true
	default:
		return false
	}
}

// TraversalPriority 返回当前关系类型在图遍历中的优先级（数值越小越优先）。
// TraversalPriority returns the traversal priority for this relation kind
// (lower values indicate higher priority during graph traversal).
func (k RelationKind) TraversalPriority() int {
	switch k {
	case KindTriggers:
		return 0
	case KindDependsOn:
		return 1
	case KindReferences:
		return 2
	default:
		return 3
	}
}

// ——————————————————————————————————————————————————————————————————————————————
// Provenance — 来源/出处枚举
// ——————————————————————————————————————————————————————————————————————————————

// Provenance 表示节点或边数据的来源方式。
// Provenance indicates how a node or edge was sourced.
type Provenance string

const (
	// ProvenanceHumanCurated 表示数据由人工整理 / 策展。
	// ProvenanceHumanCurated indicates the data was manually curated by humans.
	ProvenanceHumanCurated Provenance = "human_curated"
	// ProvenanceExtraction 表示数据由程序自动抽取。
	// ProvenanceExtraction indicates the data was automatically extracted by a program.
	ProvenanceExtraction Provenance = "extraction"
	// ProvenanceLLMInferred 表示数据由 LLM 推断得出。
	// ProvenanceLLMInferred indicates the data was inferred by an LLM.
	ProvenanceLLMInferred Provenance = "llm_inferred"
)

// IsValid 校验当前 Provenance 是否为合法枚举值。
// IsValid checks whether the current Provenance is a valid enum value.
func (p Provenance) IsValid() bool {
	switch p {
	case ProvenanceHumanCurated, ProvenanceExtraction, ProvenanceLLMInferred:
		return true
	default:
		return false
	}
}

// ——————————————————————————————————————————————————————————————————————————————
// Depth — 查询深度枚举
// ——————————————————————————————————————————————————————————————————————————————

// Depth 表示知识图谱查询的深度级别。
// Depth represents the depth level of a knowledge graph query.
type Depth string

const (
	// DepthSummary 仅返回摘要信息（最浅层查询）。
	// DepthSummary returns only summary-level information (shallowest query).
	DepthSummary Depth = "summary"
	// DepthEntity 返回单个实体的完整信息。
	// DepthEntity returns complete information for a single entity.
	DepthEntity Depth = "entity"
	// DepthNeighborhood 返回实体及其直接邻居信息。
	// DepthNeighborhood returns the entity and its immediate neighbors.
	DepthNeighborhood Depth = "neighborhood"
	// DepthSubgraph 返回实体及其可达子图（最深查询）。
	// DepthSubgraph returns the entity and its reachable subgraph (deepest query).
	DepthSubgraph Depth = "subgraph"
)

// IsValid 校验当前 Depth 是否为合法枚举值。
// IsValid checks whether the current Depth is a valid enum value.
func (d Depth) IsValid() bool {
	switch d {
	case DepthSummary, DepthEntity, DepthNeighborhood, DepthSubgraph:
		return true
	default:
		return false
	}
}

// ——————————————————————————————————————————————————————————————————————————————
// Coverage — 覆盖率枚举
// ——————————————————————————————————————————————————————————————————————————————

// Coverage 表示域的覆盖率状态。
// Coverage represents the coverage status of a domain.
type Coverage string

const (
	// CoverageFull 表示域已被完全覆盖。
	// CoverageFull indicates the domain is fully covered.
	CoverageFull Coverage = "full"
	// CoverageNone 表示域尚未被覆盖。
	// CoverageNone indicates the domain is not yet covered.
	CoverageNone Coverage = "none"
)

// ——————————————————————————————————————————————————————————————————————————————
// ResolutionStatus — 冲突解决状态枚举
// ——————————————————————————————————————————————————————————————————————————————

// ResolutionStatus 表示冲突 / 交叉引用的解决状态。
// ResolutionStatus represents the resolution status of a conflict or cross-reference.
type ResolutionStatus string

const (
	// ResolutionResolved 表示冲突已解决 / 交叉引用已处理。
	// ResolutionResolved indicates the conflict or cross-reference has been resolved.
	ResolutionResolved ResolutionStatus = "resolved"
	// ResolutionUnresolved 表示冲突未解决 / 交叉引用未处理。
	// ResolutionUnresolved indicates the conflict or cross-reference is still unresolved.
	ResolutionUnresolved ResolutionStatus = "unresolved"
	// ResolutionExternalKG 表示目标实体在当前 KG 中无法解析，需要从外部知识图谱查找。
	// ResolutionExternalKG indicates the target entity cannot be resolved in the current KG
	// and should be looked up in an external knowledge graph.
	ResolutionExternalKG ResolutionStatus = "external_kg"
)
