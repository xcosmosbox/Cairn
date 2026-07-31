// ============================================================
// 共享类型定义 / Shared Type Definitions
//
// 本文件定义了整个应用的数据契约。所有模块（数据加载、图引擎、
// 状态管理、UI 组件）都依赖这些类型。
//
// 数据来源：domain-knowledge-layer 产出的 SQLite 数据库，
// 包含 nodes 表和 edges 表。字段名与 SQL schema 精确对应。
// ============================================================

// ─── 节点类型枚举 / Node Label Enumeration ───────────────────
// 方案 B「一等节点」双层模型：除原有 Entity/Concept 外，
// skill/domain/subdomain 也成为图上的一等节点。
//   Skill      —— 来源单元（来自 SKILL.md），provides→domain
//   Domain     —— 可复用领域（reference/*.md），composes→subdomain
//   Subdomain  —— domain 的子分支，composes→entity/concept
//   Entity     —— 具体实体知识点
//   Concept    —— 具体概念知识点
// 层级：Skill --provides--> Domain --composes--> Subdomain --composes--> Entity/Concept
//
// Plan-B "first-class node" two-layer model: in addition to the original
// Entity/Concept, skill/domain/subdomain are now first-class graph nodes.
// 与 SQLite schema 的 CHECK(label IN (...)) 精确对应。
export type NodeLabel = 'Entity' | 'Concept' | 'Skill' | 'Domain' | 'Subdomain'

// ─── 知识图谱节点 / Knowledge Graph Node ─────────────────────
// 对应 SQLite `nodes` 表。id 依类型不同：
//   entity/concept: 三段式 "domainSlug::subSlug::entityID"
//   skill:          "skill::<skillName>"
//   domain:         "domain::<domainSlug>"
//   subdomain:      "subdomain::<domainSlug>::<subSlug>"

export interface KGNode {
  id: string // 分类型标识符 / typed ID (see NodeLabel comment for formats)
  label: NodeLabel // 节点类型 / node type
  name: string // 显示名称 / display name
  summary: string // 简要描述 / brief summary
  synonyms: string | null // 同义词 JSON 数组 / JSON array of synonyms
  domain: string // 所属领域 / owning domain
  subdomain: string // 所属子域 / owning subdomain
  description: string | null // 详细描述 / detailed description
  properties: string | null // 自定义属性 JSON 对象 / custom properties JSON
  tags: string | null // 标签 JSON 数组 / JSON array of tags
  related_entities: string | null // 关联实体 JSON / related entities JSON
  visibility: 'public' | 'private' // 可见性 / visibility
  confidence: number // 置信度 0.0-1.0 / confidence score
  provenance: 'llm_inferred' | 'human_curated' | 'extraction' // 数据来源 / data origin
  source_refs: string | null // 来源引用 JSON / source references JSON
  created_at: string // 创建时间 ISO / creation timestamp
  updated_at: string // 更新时间 ISO / update timestamp
}

// ─── 知识图谱边 / Knowledge Graph Edge ────────────────────────
// 对应 SQLite `edges` 表。source_id 和 target_id 外键引用 nodes(id)。

export interface KGEdge {
  id: number // 自增主键 / auto-increment PK
  source_id: string // 源节点 ID → KGNode.id
  target_id: string // 目标节点 ID → KGNode.id
  kind: EdgeKind // 关系类型 / relationship kind
  description: string | null // 关系描述 / relationship description
  properties: string | null // 自定义属性 JSON / custom properties JSON
  provenance: 'llm_inferred' | 'human_curated' | 'extraction'
  confidence: number // 置信度 0.0-1.0
  source_refs: string | null
  bidirectional: boolean // 是否双向 / is bidirectional
  cardinality: string | null // 基数约束 / cardinality constraint
  created_at: string
}

// 关系类型枚举 / Edge kind enumeration
// 前 6 类为 entity↔entity 语义关系（LLM 产出）+ 层级 composes；
// 'provides' 为方案 B 新增的 skill→domain 边（层节点间的来源关系）。
// 与 SQLite schema 的 CHECK(kind IN (...)) 精确对应。
export type EdgeKind =
  | 'triggers'
  | 'depends_on'
  | 'references'
  | 'generalizes'
  | 'composes'
  | 'contradicts'
  | 'provides'

// ─── 知识图谱数据库元信息 / Database Metadata ────────────────

export interface KGMeta {
  name: string // 用户自定义名称 / user-assigned name
  path: string // OPFS 中的路径 / path within OPFS
  domainCount: number // 领域数量 / number of domains
  nodeCount: number // 节点总数 / total nodes
  edgeCount: number // 边总数 / total edges
  loadedAt: string // 加载时间 ISO / loaded timestamp
}

// ─── 图引擎加载结果 / Graph Load Result ──────────────────────

export interface GraphLoadResult {
  nodes: KGNode[]
  edges: KGEdge[]
  meta: Pick<KGMeta, 'domainCount' | 'nodeCount' | 'edgeCount'>
  /**
   * 复杂度哨兵历史时序（W-B，可选）。
   * 来自 kg_manifest 的 sentinel.history 键；老库无该键时为 undefined。
   * 向后兼容：既有消费者不读此字段，行为不变。
   *
   * Sentinel history time series (W-B, optional) from kg_manifest's
   * sentinel.history key; undefined for older DBs without the key.
   */
  sentinelHistory?: SentinelSample[]
}

// ─── 复杂度哨兵采样 / Complexity Sentinel Sample ─────────────
// 与 service SentinelSnapshot 的 JSON 字段一一对应（sentinel.history 元素）。
// Mirrors the service SentinelSnapshot JSON shape (one sentinel.history entry).

export interface SentinelSample {
  ts: string // 采样时间 ISO / sampling timestamp
  q: number // 现状模块度 / current modularity Q
  singleton_ratio: number // 单例子域占比 / singleton subdomain ratio
  edge_node_ratio: number // 语义边/节点比 / semantic edge-to-node ratio
  cumulative_ratio: number // 累计改动占比 / cumulative change ratio
  breach_reasons?: string[] // 越界原因（空=未越阈值）/ breach reasons (empty = healthy)
}

// ─── 可视化主题配置 / Graph Theme Configuration ──────────────
// RGB 三元组表示 0-255 范围的颜色分量。

export type RGB = [number, number, number]

export interface GraphTheme {
  id: string
  name: string
  background: string // 画布背景色 / canvas background
  nodeMin: RGB // 低连接度节点颜色 / peripheral node color
  nodeMax: RGB // 高连接度节点（hub）颜色 / hub node color
  palette: RGB[] // 领域调色板 / domain color palette
  edgeMin: RGB // 弱关系边颜色 / weak edge color
  edgeMax: RGB // 强关系边颜色 / strong edge color
  labelColor: string // 聚类标签文字色 / cluster label text color
  labelBg: string // 聚类标签背景色 / cluster label background
  labelBorder: string // 聚类标签边框色 / cluster label border
  nodeLabelColor: string // 节点标签文字色 / node label text color
}

// ─── 演化模式类型 / Evolution Mode Types (E-5) ───────────────
// 与 cairn-evolve 的 GET /api/manifest 返回结构一一对应
// （core/evolve 的 ChangesetRow / MetricRow JSON 字段）。

/** 节点变更类别（血缘感知，与 core/observe 的分类常量一致） */
export type NodeChangeKind =
  | 'added'
  | 'deleted'
  | 'merged_into'
  | 'split_into'
  | 'migrated'
  | 'renamed'
  | 'content'
  | 'unchanged'

/** diff_json 中的节点事件 */
export interface NodeChangeJSON {
  id: string
  name: string
  change: NodeChangeKind
  detail?:
    | { into: string; into_name: string } // merged_into
    | { into: string[]; into_names: string[] } // split_into
    | { from_domain: string; to_domain: string; from_subdomain: string; to_subdomain: string } // migrated
    | { old_name: string; new_name: string } // renamed
    | { fields: string[] } // content
}

/** diff_json 中的边事件 */
export interface EdgeChangeJSON {
  source: string
  target: string
  kind: string
  change: 'added' | 'dropped'
  source_name?: string
  target_name?: string
}

/** core/observe DiffResult 的 JSON 形态 */
export interface DiffResultJSON {
  nodes: NodeChangeJSON[]
  edges: EdgeChangeJSON[]
  counts: Record<string, number>
}

/** 一条 changeset（时间轴上的一个点 = 知识库的一次「提交」） */
export interface EvolutionChangeset {
  seq: number
  ts: string
  tool: string // ingest | incremental | rebalance
  kb_version: string
  parent_version?: string
  snapshot_file: string
  summary: string
  trigger_reason?: string
  diff?: DiffResultJSON
  docs_affected?: string[]
  n_added: number
  n_deleted: number
  n_merged: number
  n_split: number
  n_migrated: number
  n_renamed: number
  n_content: number
  n_edge_added: number
  n_edge_dropped: number
}

/** 一条指标采样（规模 + 质量曲线数据点） */
export interface EvolutionMetric {
  seq: number
  ts: string
  kb_version: string
  modularity_q: number
  singleton_ratio: number
  edge_node_ratio: number
  node_count: number
  edge_count: number
  cumulative_ratio: number
}

/** GET /api/manifest 的载荷 */
export interface EvolutionManifest {
  changesets: EvolutionChangeset[]
  metrics: EvolutionMetric[]
}
