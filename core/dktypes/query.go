// Package dktypes 定义领域知识层（Cairn）中所有跨包共享的核心类型、
// 枚举、数据结构及其校验方法。
//
// 本文件包含知识图谱查询相关的请求、响应及结果数据结构。
package dktypes

// QuerySyntax distinguishes user keywords from explicit SQLite MATCH syntax.
// The zero value is ordinary text; advanced expressions are never auto-detected.
type QuerySyntax string

const (
	QuerySyntaxText QuerySyntax = "text"
	QuerySyntaxFTS5 QuerySyntax = "fts5"
)

func (s QuerySyntax) IsValid() bool {
	return s == "" || s == QuerySyntaxText || s == QuerySyntaxFTS5
}

// ——————————————————————————————————————————————————————————————————————————————
// QueryRequest — 知识图谱查询请求
// ——————————————————————————————————————————————————————————————————————————————

// QueryRequest 表示一次知识图谱查询的请求参数。
// QueryRequest represents the request parameters for a knowledge graph query.
type QueryRequest struct {
	// Query 是查询文本 / 关键词。
	// Query is the query text or keyword.
	Query string `json:"query"`
	// QuerySyntax defaults to text (literal whitespace atoms joined with AND).
	// fts5 accepts an unchanged advanced MATCH expression, including its errors.
	QuerySyntax QuerySyntax `json:"query_syntax,omitempty"`
	// Depth 指定查询的深度级别（summary / entity / neighborhood / subgraph）。
	// Depth specifies the depth level of the query.
	Depth Depth `json:"depth,omitempty"`
	// Scope 限定查询的域 / 子域范围。
	// Scope restricts the query to specific domains or subdomains.
	Scope []string `json:"scope,omitempty"`
	// MaxTokens 限制返回结果的最大 token 数量。
	// MaxTokens limits the maximum number of tokens in the response.
	MaxTokens int `json:"max_tokens,omitempty"`
	// MinConfidence 过滤掉置信度低于此阈值的节点。
	// MinConfidence filters out nodes with confidence below this threshold.
	MinConfidence float64 `json:"min_confidence,omitempty"`
	// ProvenanceFilter 仅返回指定来源类型的节点。
	// ProvenanceFilter restricts results to the specified provenance types.
	ProvenanceFilter []Provenance `json:"provenance_filter,omitempty"`
}

// ——————————————————————————————————————————————————————————————————————————————
// QueryResponse — 知识图谱查询响应
// ——————————————————————————————————————————————————————————————————————————————

// QueryResponse 表示查询的通用响应结构，results 字段类型为 any 以适配不同深度的返回。
// QueryResponse represents the generic query response structure;
// the results field uses type any to accommodate different depth levels.
type QueryResponse struct {
	// Results 是查询结果，具体类型取决于查询深度。
	// Results contains the query results; the concrete type depends on the query depth.
	Results any `json:"results"`
	// Meta 包含查询的元数据信息。
	// Meta contains metadata about the query.
	Meta QueryMeta `json:"meta"`
}

// QueryMeta 包含查询的元数据信息。
// QueryMeta contains metadata about a knowledge graph query.
type QueryMeta struct {
	// Depth 表示实际使用的查询深度。
	// Depth indicates the actual query depth used.
	Depth Depth `json:"depth"`
	// TotalMatches 是匹配到的节点总数。
	// TotalMatches is the total number of matched nodes.
	TotalMatches int `json:"total_matches"`
	// TokensUsed 是查询消耗的 token 数量。
	// TokensUsed is the number of tokens consumed by the query.
	TokensUsed int `json:"tokens_used"`
	// QueryMs 是查询执行的毫秒数。
	// QueryMs is the query execution time in milliseconds.
	QueryMs int64 `json:"query_ms"`
	// KBVersion 是知识图谱的版本标识。
	// KBVersion is the version identifier of the knowledge base.
	KBVersion string `json:"kb_version"`
	// DomainCoverage 是各域的覆盖率映射（域名 -> 覆盖率）。
	// DomainCoverage is a map from domain name to coverage status.
	DomainCoverage map[string]Coverage `json:"domain_coverage,omitempty"`
}

// ——————————————————————————————————————————————————————————————————————————————
// SummaryResult — 摘要查询结果
// ——————————————————————————————————————————————————————————————————————————————

// SummaryResult 表示 depth=summary 时的单个节点查询结果。
// SummaryResult represents a single node query result when depth=summary.
type SummaryResult struct {
	// EntityID 是节点的唯一标识符。
	// EntityID is the unique identifier of the entity.
	EntityID string `json:"entity_id"`
	// Name 是节点的可读名称。
	// Name is the human-readable name of the node.
	Name string `json:"name"`
	// Summary 是节点的简短摘要。
	// Summary is a short summary of the node.
	Summary string `json:"summary"`
	// Type 表示节点类型（Entity 或 Concept）。
	// Type indicates the node type (Entity or Concept).
	Type Label `json:"type"`
	// Domain 是节点所属的业务域。
	// Domain is the business domain of the node.
	Domain string `json:"domain"`
	// Subdomain 是节点所属的业务子域。
	// Subdomain is the business subdomain of the node.
	Subdomain string `json:"subdomain"`
	// Relevance 是节点与查询的相关性评分。
	// Relevance is the relevance score of the node to the query.
	Relevance float64 `json:"relevance"`
	// RelatedCount 是与该节点直接关联的其他节点数量。
	// RelatedCount is the number of nodes directly related to this node.
	RelatedCount int `json:"related_count"`
	// Synonyms 是节点的同义词列表。
	// Synonyms is a list of synonyms for the node.
	Synonyms []string `json:"synonyms,omitempty"`
}

// ——————————————————————————————————————————————————————————————————————————————
// EntityResult — 实体查询结果
// ——————————————————————————————————————————————————————————————————————————————

// EntityResult 表示 depth=entity 时的单个节点完整查询结果。
// EntityResult represents a complete single node query result when depth=entity.
type EntityResult struct {
	// EntityID 是节点的唯一标识符。
	// EntityID is the unique identifier of the entity.
	EntityID string `json:"entity_id"`
	// Name 是节点的可读名称。
	// Name is the human-readable name of the node.
	Name string `json:"name"`
	// Summary 是节点的简短摘要。
	// Summary is a short summary of the node.
	Summary string `json:"summary"`
	// Type 表示节点类型（Entity 或 Concept）。
	// Type indicates the node type (Entity or Concept).
	Type Label `json:"type"`
	// Domain 是节点所属的业务域。
	// Domain is the business domain of the node.
	Domain string `json:"domain"`
	// Subdomain 是节点所属的业务子域。
	// Subdomain is the business subdomain of the node.
	Subdomain string `json:"subdomain"`
	// Content 是节点的完整内容描述。
	// Content is the full content description of the node.
	Content string `json:"content"`
	// Synonyms 是节点的同义词列表。
	// Synonyms is a list of synonyms for the node.
	Synonyms []string `json:"synonyms,omitempty"`
	// Relations 是从该节点出发的所有关系列表。
	// Relations is a list of all relationships originating from this node.
	Relations []RelationOnEdge `json:"relations"`
	// Confidence 是节点数据的置信度。
	// Confidence is the confidence score of the node data.
	Confidence float64 `json:"confidence"`
	// Provenance 表示节点数据的来源方式。
	// Provenance indicates how the node data was sourced.
	Provenance Provenance `json:"provenance"`
	// SourceRefs 是节点的来源引用列表。
	// SourceRefs is a list of source references for the node.
	SourceRefs []string `json:"source_refs,omitempty"`
}

// RelationOnEdge 表示从当前节点出发的一条关系及其目标节点的概要信息。
// RelationOnEdge represents a relationship originating from the current node
// together with summary information about the target node.
type RelationOnEdge struct {
	// Kind 表示关系的语义类型。
	// Kind indicates the semantic type of the relationship.
	Kind RelationKind `json:"kind"`
	// TargetName 是目标节点的可读名称。
	// TargetName is the human-readable name of the target node.
	TargetName string `json:"target_name"`
	// TargetID 是目标节点的唯一标识符。
	// TargetID is the unique identifier of the target node.
	TargetID string `json:"target_id"`
	// Description 是对这条关系的描述。
	// Description is a description of the relationship.
	Description string `json:"description,omitempty"`
}

// ——————————————————————————————————————————————————————————————————————————————
// SubgraphResponse — 子图查询结果
// ——————————————————————————————————————————————————————————————————————————————

// SubgraphResponse 表示 depth=subgraph 时的查询结果，包含多个子图组件。
// SubgraphResponse represents the query result when depth=subgraph,
// containing multiple subgraph components.
type SubgraphResponse struct {
	// Components 是子图中的所有组件（节点、关系、冲突等）。
	// Components contains all components in the subgraph (nodes, relations, conflicts, etc.).
	Components []SubgraphComponent `json:"components"`
}

// SubgraphComponent 表示子图中的一个组件，可能是实体节点、关系或冲突信息。
// SubgraphComponent represents a component in the subgraph, which may be
// an entity node, a relationship, or conflict information.
type SubgraphComponent struct {
	// Type 是组件的类型标识（如 "entity", "concept", "relation", "conflict"）。
	// Type is the component type identifier (e.g., "entity", "concept", "relation", "conflict").
	Type string `json:"type"`
	// EntityID 是实体节点的唯一标识符（当类型为 entity/concept 时）。
	// EntityID is the unique identifier of the entity node (when type is entity/concept).
	EntityID string `json:"entity_id,omitempty"`
	// Name 是实体名称（当类型为 entity/concept 时）。
	// Name is the entity name (when type is entity/concept).
	Name string `json:"name,omitempty"`
	// Content 是实体的详细内容（当类型为 entity/concept 时）。
	// Content is the detailed content of the entity (when type is entity/concept).
	Content string `json:"content,omitempty"`
	// Domain 是实体所属的业务域（当类型为 entity/concept 时）。
	// Domain is the business domain of the entity (when type is entity/concept).
	Domain string `json:"domain,omitempty"`
	// Subdomain 是实体所属的业务子域（当类型为 entity/concept 时）。
	// Subdomain is the business subdomain of the entity (when type is entity/concept).
	Subdomain string `json:"subdomain,omitempty"`
	// Confidence 是实体或关系的置信度。
	// Confidence is the confidence score of the entity or relationship.
	Confidence float64 `json:"confidence,omitempty"`
	// Provenance 表示数据的来源方式。
	// Provenance indicates how the data was sourced.
	Provenance Provenance `json:"provenance,omitempty"`
	// Path 是从中心节点到该组件的路径（当类型为 relation/conflict 时）。
	// Path is the path from the center node to this component (when type is relation/conflict).
	Path []string `json:"path,omitempty"`
	// RelationDesc 是对关系的描述（当类型为 relation 时）。
	// RelationDesc is a description of the relationship (when type is relation).
	RelationDesc string `json:"description,omitempty"`
	// HopCount 是从中心节点到该组件的跳数（当类型为 relation/conflict 时）。
	// HopCount is the number of hops from the center node to this component
	// (when type is relation/conflict).
	HopCount int `json:"hop_count,omitempty"`
	// Entity 是冲突涉及的实体名称（当类型为 conflict 时）。
	// Entity is the name of the entity involved in the conflict (when type is conflict).
	Entity string `json:"entity,omitempty"`
	// ConflictingDefs 是冲突的不同定义列表（当类型为 conflict 时）。
	// ConflictingDefs is a list of conflicting definitions (when type is conflict).
	ConflictingDefs []ConflictingDef `json:"conflicting_definitions,omitempty"`
	// ResolutionStatus 是冲突的解决状态（当类型为 conflict 时）。
	// ResolutionStatus is the resolution status of the conflict (when type is conflict).
	ResolutionStatus ResolutionStatus `json:"resolution_status,omitempty"`
	// ResolutionNote 是冲突解决的备注说明（当类型为 conflict 时）。
	// ResolutionNote is a note describing how the conflict was resolved (when type is conflict).
	ResolutionNote string `json:"resolution_note,omitempty"`
}

// ConflictingDef 表示同一概念的一个冲突性定义。
// ConflictingDef represents a conflicting definition of the same concept.
type ConflictingDef struct {
	// Value 是定义的文本值。
	// Value is the text value of the definition.
	Value string `json:"value"`
	// Source 是该定义的来源。
	// Source is the origin of this definition.
	Source string `json:"source"`
	// Context 是该定义的上下文信息。
	// Context is the contextual information for this definition.
	Context string `json:"context"`
}

// ——————————————————————————————————————————————————————————————————————————————
// ListDomainsResponse — 域列表查询结果
// ——————————————————————————————————————————————————————————————————————————————

// ListDomainsResponse 表示列出所有域的查询结果。
// ListDomainsResponse represents the query result for listing all domains.
type ListDomainsResponse struct {
	// Domains 是域信息列表。
	// Domains is a list of domain information.
	Domains []DomainInfo `json:"domains"`
}

// DomainInfo 表示一个域的简要信息。
// DomainInfo represents summary information about a domain.
type DomainInfo struct {
	// Name 是域的名称。
	// Name is the name of the domain.
	Name string `json:"name"`
	// Summary 是域的简要描述。
	// Summary is a brief summary of the domain.
	Summary string `json:"summary"`
	// SubdomainCount 是该域下的子域数量。
	// SubdomainCount is the number of subdomains within this domain.
	SubdomainCount int `json:"subdomain_count"`
	// Coverage 表示该域的覆盖率状态。
	// Coverage indicates the coverage status of this domain.
	Coverage Coverage `json:"coverage"`
}
