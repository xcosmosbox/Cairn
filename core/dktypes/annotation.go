// Package dktypes 定义领域知识层（Cairn）中所有跨包共享的核心类型、
// 枚举、数据结构及其校验方法。
//
// 本文件包含领域定义的结构体定义（DomainDef / SubdomainDef / EntityDef 等），
// 供 extract 和 storage 包的转换方法（ToDomainDef / ToEntityDef / ToConceptDef）使用。
package dktypes

// ——————————————————————————————————————————————————————————————————————————————
// DomainDef — 领域定义
// ——————————————————————————————————————————————————————————————————————————————

// DomainDef 是一个领域的完整定义，包含领域基本信息及所有下属子域。
// DomainDef is the complete definition of a domain, containing basic domain
// information and all subdomains under it.
type DomainDef struct {
	// Name 是领域的名称。
	// Name is the name of the domain.
	Name string `yaml:"name" json:"name"`
	// Summary 是领域的简要描述。
	// Summary is a brief summary of the domain.
	Summary string `yaml:"summary" json:"summary"`
	// Description 是领域的详细描述。
	// Description is a detailed description of the domain.
	Description string `yaml:"description" json:"description"`
	// Subdomains 是该领域下的所有子域定义列表。
	// Subdomains is a list of all subdomain definitions under this domain.
	Subdomains []SubdomainDef `yaml:"subdomains" json:"subdomains"`
}

// ——————————————————————————————————————————————————————————————————————————————
// SubdomainDef — 子域定义
// ——————————————————————————————————————————————————————————————————————————————

// SubdomainDef 是一个子域的完整定义，包含实体、概念、关系及交叉引用。
// SubdomainDef is the complete definition of a subdomain, containing entities,
// concepts, relations, and cross-references.
type SubdomainDef struct {
	// Name 是子域的名称。
	// Name is the name of the subdomain.
	Name string `yaml:"name" json:"name"`
	// Summary 是子域的简要描述。
	// Summary is a brief summary of the subdomain.
	Summary string `yaml:"summary" json:"summary"`
	// Description 是子域的详细描述。
	// Description is a detailed description of the subdomain.
	Description string `yaml:"description" json:"description"`
	// Entities 是该子域下的所有实体定义列表。
	// Entities is a list of all entity definitions under this subdomain.
	Entities []EntityDef `yaml:"entities" json:"entities"`
	// Concepts 是该子域下的所有概念定义列表。
	// Concepts is a list of all concept definitions under this subdomain.
	Concepts []ConceptDef `yaml:"concepts" json:"concepts"`
	// Relations 是该子域下的所有关系定义列表。
	// Relations is a list of all relation definitions under this subdomain.
	Relations []RelationDef `yaml:"relations" json:"relations"`
	// CrossReferences 是该子域下的所有交叉引用定义列表。
	// CrossReferences is a list of all cross-reference definitions under this subdomain.
	CrossReferences []CrossRefDef `yaml:"cross_references" json:"cross_references"`
}

// ——————————————————————————————————————————————————————————————————————————————
// EntityDef — 实体定义
// ——————————————————————————————————————————————————————————————————————————————

// EntityDef 表示一个业务实体的定义，包含实体标识、描述及来源信息。
// EntityDef represents the definition of a business entity, containing
// entity identification, description, and source information.
type EntityDef struct {
	// ID 是实体的唯一标识符。
	// ID is the unique identifier of the entity.
	ID string `yaml:"id" json:"id"`
	// Label 是实体的标签名称。
	// Label is the label name of the entity.
	Label string `yaml:"label" json:"label"`
	// Name 是实体的可读名称。
	// Name is the human-readable name of the entity.
	Name string `yaml:"name" json:"name"`
	// Summary 是实体的简要描述。
	// Summary is a brief summary of the entity.
	Summary string `yaml:"summary" json:"summary"`
	// Synonyms 是实体名称的同义词列表（可选）。
	// Synonyms is a list of synonyms for the entity name (optional).
	Synonyms []string `yaml:"synonyms,omitempty" json:"synonyms,omitempty"`
	// Description 是实体的详细描述。
	// Description is a detailed description of the entity.
	Description string `yaml:"description" json:"description"`
	// Confidence 是实体数据的置信度（0.0 ~ 1.0）。
	// Confidence is the confidence score of the entity data (0.0 to 1.0).
	Confidence float64 `yaml:"confidence" json:"confidence"`
	// SourceRefs 是实体的引用来源列表。
	// SourceRefs is a list of source references for the entity.
	SourceRefs []string `yaml:"source_refs" json:"source_refs"`
	// RelatedEntities 是关联实体的 ID 列表（可选）。
	// RelatedEntities is a list of related entity IDs (optional).
	RelatedEntities []string `yaml:"related_entities,omitempty" json:"related_entities,omitempty"`
}

// ——————————————————————————————————————————————————————————————————————————————
// ConceptDef — 概念定义
// ——————————————————————————————————————————————————————————————————————————————

// ConceptDef 表示一个业务概念的定义，与 EntityDef 类似但结构略简。
// ConceptDef represents the definition of a business concept, similar to
// EntityDef but with a slightly simpler structure.
type ConceptDef struct {
	// ID 是概念的唯一标识符。
	// ID is the unique identifier of the concept.
	ID string `yaml:"id" json:"id"`
	// Label 是概念的标签名称。
	// Label is the label name of the concept.
	Label string `yaml:"label" json:"label"`
	// Name 是概念的可读名称。
	// Name is the human-readable name of the concept.
	Name string `yaml:"name" json:"name"`
	// Summary 是概念的简要描述。
	// Summary is a brief summary of the concept.
	Summary string `yaml:"summary" json:"summary"`
	// Synonyms 是概念名称的同义词列表（可选）。
	// Synonyms is a list of synonyms for the concept name (optional).
	Synonyms []string `yaml:"synonyms,omitempty" json:"synonyms,omitempty"`
	// Description 是概念的详细描述。
	// Description is a detailed description of the concept.
	Description string `yaml:"description" json:"description"`
	// Confidence 是概念数据的置信度（0.0 ~ 1.0）。
	// Confidence is the confidence score of the concept data (0.0 to 1.0).
	Confidence float64 `yaml:"confidence" json:"confidence"`
	// SourceRefs 是概念的引用来源列表。
	// SourceRefs is a list of source references for the concept.
	SourceRefs []string `yaml:"source_refs" json:"source_refs"`
}

// ——————————————————————————————————————————————————————————————————————————————
// RelationDef — 关系定义
// ——————————————————————————————————————————————————————————————————————————————

// RelationDef 表示两个实体或概念之间的一条关系定义。
// RelationDef represents the definition of a relationship between two entities or concepts.
type RelationDef struct {
	// Source 是关系的源实体 / 概念 ID。
	// Source is the ID of the source entity/concept of the relationship.
	Source string `yaml:"source" json:"source"`
	// Kind 是关系的语义类型。
	// Kind is the semantic type of the relationship.
	Kind string `yaml:"kind" json:"kind"`
	// Target 是关系的目标实体 / 概念 ID。
	// Target is the ID of the target entity/concept of the relationship.
	Target string `yaml:"target" json:"target"`
	// TargetDomain 是目标实体所在的域（可选，用于跨域引用）。
	// TargetDomain is the domain of the target entity (optional, for cross-domain references).
	TargetDomain string `yaml:"target_domain,omitempty" json:"target_domain,omitempty"`
	// Description 是对该关系的描述。
	// Description is a description of the relationship.
	Description string `yaml:"description" json:"description"`
	// Confidence 是该关系的置信度（0.0 ~ 1.0）。
	// Confidence is the confidence score of the relationship (0.0 to 1.0).
	Confidence float64 `yaml:"confidence" json:"confidence"`
	// Provenance 表示该关系数据的来源方式。
	// Provenance indicates how the relationship data was sourced.
	Provenance string `yaml:"provenance" json:"provenance"`
	// SourceRefs 是该关系的引用来源列表。
	// SourceRefs is a list of source references for the relationship.
	SourceRefs []string `yaml:"source_refs" json:"source_refs"`
	// Bidirectional 表示该关系是否为双向关系。
	// Bidirectional indicates whether the relationship is bidirectional.
	Bidirectional bool `yaml:"bidirectional" json:"bidirectional"`
}

// ——————————————————————————————————————————————————————————————————————————————
// CrossRefDef — 交叉引用定义
// ——————————————————————————————————————————————————————————————————————————————

// CrossRefDef 表示一条从当前子域指出的交叉引用定义，用于跨 Skill 共享知识。
// CrossRefDef represents a cross-reference definition pointing from the current
// subdomain to another, used for sharing knowledge across skills.
type CrossRefDef struct {
	// From 是交叉引用的源实体 ID。
	// From is the ID of the source entity of the cross-reference.
	From string `yaml:"from" json:"from"`
	// To 是交叉引用的目标实体 ID。
	// To is the ID of the target entity of the cross-reference.
	To string `yaml:"to" json:"to"`
	// Kind 是交叉引用的关系类型。
	// Kind is the relationship type of the cross-reference.
	Kind string `yaml:"kind" json:"kind"`
	// Reason 是该交叉引用的原因说明。
	// Reason explains the reason for this cross-reference.
	Reason string `yaml:"reason" json:"reason"`
}
