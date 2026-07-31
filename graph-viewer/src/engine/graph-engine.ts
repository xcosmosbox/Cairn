// ============================================================
// GraphEngine — 图数据引擎 / Graph Data Engine
//
// 负责将 KGNode/KGEdge 平面数据转换为 graphology 图结构，
// 并通过 d3-force 力导向算法计算节点布局位置。
//
// Builds a graphology Graph from flat node/edge data and
// runs d3-force simulation for spatial layout.
// ============================================================

import Graph from 'graphology'
import { forceSimulation, forceLink, forceManyBody, forceCenter, forceCollide, forceX, forceY } from 'd3-force'
import type { KGNode, KGEdge, NodeLabel } from '../types'

// ─── 图节点渲染属性 / Graph Node Rendering Attributes ────────
// 挂载到每个 graphology node 的 attributes 上，供 Sigma.js 渲染器使用。

export interface GraphNodeAttributes {
  x: number // 布局 X 坐标 / layout X position
  y: number // 布局 Y 坐标 / layout Y position
  size: number // 视觉半径 / visual radius (type-based, see NODE_TYPE_STYLE)
  color: string // CSS 颜色字符串 / CSS color string
  label: string // 截断显示标签 30 字 / truncated label 30 chars
  fullLabel: string // 完整原始名称 / full original name
  nodeType: NodeLabel // 节点类型（5 类）/ node type (5 kinds)
  domain: string // 所属领域 / owning domain
  subdomain: string // 所属子域 / owning subdomain
  confidence: number // 置信度 0.0-1.0 / confidence score
  connectivity: number // 标准化连接度 0-1 / normalized connectivity
  isGhost?: boolean // 是否为幽灵节点（边引用但节点表中不存在）/ whether this is a ghost placeholder for an unresolvable edge endpoint
}

// ─── 图边渲染属性 / Graph Edge Rendering Attributes ──────────

export interface GraphEdgeAttributes {
  weight: number // 置信度权重 0-1 / confidence weight 0-1
  kind: KGEdge['kind'] // 关系类型 / relationship kind
  size: number // 视觉线宽 0.5-2.0 / visual line width
}

// ─── 工具函数 / Utility Functions ────────────────────────────

// ─── 分层节点样式 / Per-Layer Node Style ─────────────────────
// 方案 B 双层模型：按节点类型（label）分层着色 + 区分大小，直观体现层级。
// 上层节点（Skill/Domain）更大更亮、作为聚类锚点；下层（Entity/Concept）更小。
// 5 类各用一个固定基色（不再靠 domain hash），一眼区分层级；hub 亮度仍随连接度微调。
//
// Plan-B two-layer model: color + size each node by its type (label) so the
// hierarchy reads at a glance. Upper-layer nodes (Skill/Domain) are larger and
// brighter and act as cluster anchors; lower-layer (Entity/Concept) are smaller.
// Each of the 5 types has one fixed base color; hub brightness still nudged by
// connectivity. baseSize is the layout/rendering radius before connectivity boost.
interface NodeTypeStyle {
  base: [number, number, number] // 基础 RGB 色 / base RGB color
  baseSize: number // 基础视觉半径 / base visual radius
}

const NODE_TYPE_STYLE: Record<NodeLabel, NodeTypeStyle> = {
  // Skill — 来源层，暖金色，最大（层级顶端锚点）
  Skill: { base: [230, 170, 60], baseSize: 14 },
  // Domain — 领域层，青蓝色，大（次级锚点）
  Domain: { base: [80, 175, 200], baseSize: 11 },
  // Subdomain — 子域层，紫罗兰，中
  Subdomain: { base: [150, 110, 210], baseSize: 8 },
  // Entity — 实体，柔绿，小
  Entity: { base: [110, 185, 130], baseSize: 5 },
  // Concept — 概念，玫红，小（与 Entity 区分色相）
  Concept: { base: [200, 110, 140], baseSize: 5 },
}

/** djb2 字符串哈希（确定性）/ djb2 string hash (deterministic) */
function hashString(str: string): number {
  let hash = 5381
  for (let i = 0; i < str.length; i++) {
    hash = ((hash << 5) + hash) + str.charCodeAt(i) // hash * 33 + c
  }
  return Math.abs(hash)
}

/**
 * 根据节点类型（label）和连接度计算节点颜色。
 * 方案 B：颜色由类型分层决定（5 类固定基色），而非 domain hash——
 * 这样双层结构一眼可辨。连接度仅用于微调亮度：低连接度偏暗（0.55×），
 * hub 节点达到满亮度（1.0×），保留"重要节点更醒目"的原有观感。
 *
 * Computes node color from the node type (label) and connectivity.
 * Plan-B: color is driven by the type layer (5 fixed base colors) rather than a
 * domain hash, so the two-layer structure is legible at a glance. Connectivity
 * only modulates brightness (dimmer peripherals, full-brightness hubs).
 */
function getNodeColor(label: NodeLabel, connectivity: number): string {
  const style = NODE_TYPE_STYLE[label] ?? NODE_TYPE_STYLE.Entity
  const base = style.base
  const factor = 0.55 + connectivity * 0.45 // 0.55..1.0
  const r = Math.round(base[0] * factor)
  const g = Math.round(base[1] * factor)
  const b = Math.round(base[2] * factor)
  return `rgb(${r},${g},${b})`
}

/**
 * 根据节点类型和连接度计算视觉半径。
 * 层级基础尺寸（NODE_TYPE_STYLE.baseSize）体现"上层大、下层小"，
 * 再叠加连接度加成（最多 +4），使同层内的 hub 略大。
 *
 * Computes visual radius from node type + connectivity. The per-layer base size
 * encodes "upper layers larger", plus a connectivity bonus (up to +4) so hubs
 * within a layer stand out slightly.
 */
function getNodeSize(label: NodeLabel, connectivity: number): number {
  const style = NODE_TYPE_STYLE[label] ?? NODE_TYPE_STYLE.Entity
  return style.baseSize + connectivity * 4
}

/**
 * 截断标签文本，超长以 "…" (U+2026) 替代。
 * Truncates label text, replacing overflow with Unicode ellipsis.
 */
function truncateLabel(str: string, max: number): string {
  return str.length > max ? str.substring(0, max - 1) + '…' : str
}

// ============================================================
// GraphEngine 类 / GraphEngine Class
// ============================================================

export class GraphEngine {
  private graph: Graph<GraphNodeAttributes, GraphEdgeAttributes> | null = null
  private simulation: ReturnType<typeof forceSimulation> | null = null

  /**
   * 从 KGNode[] / KGEdge[] 构建图并运行 d3-force 力导向布局。
   *
   * Builds a graphology Graph from the given nodes and edges,
   * runs d3-force simulation for layout, and returns the positioned graph.
   *
   * @param nodes  - 知识图谱节点列表 / list of KG nodes
   * @param edges  - 知识图谱边列表 / list of KG edges
   * @param options - 布局画布尺寸（默认 800×600）/ canvas size (default 800×600)
   * @returns 构建完成的 graphology Graph 实例 / the built graphology instance
   */
  buildGraph(
    nodes: KGNode[],
    edges: KGEdge[],
    options?: { width?: number; height?: number },
  ): Graph<GraphNodeAttributes, GraphEdgeAttributes> {
    // 销毁旧实例 / destroy previous instance
    this.destroy()

    const graph = new Graph<GraphNodeAttributes, GraphEdgeAttributes>()
    this.graph = graph

    // ─── 1. 计算每个节点的边数（用于连接度归一化）────────────────
    // Compute per-node edge count for connectivity normalization
    const edgeCounts = new Map<string, number>()
    for (const edge of edges) {
      edgeCounts.set(edge.source_id, (edgeCounts.get(edge.source_id) || 0) + 1)
      edgeCounts.set(edge.target_id, (edgeCounts.get(edge.target_id) || 0) + 1)
    }
    const maxEdges = Math.max(1, ...edgeCounts.values())

    // ─── 2. 添加节点 / Add nodes ───────────────────────────────
    const w = options?.width ?? 800
    const h = options?.height ?? 600

    for (const node of nodes) {
      const connectivity = (edgeCounts.get(node.id) || 0) / maxEdges

      graph.addNode(node.id, {
        // 随机初始位置（避免所有节点起点重叠导致 d3-force 陷入局部最优）
        // Random initial positions to avoid local optima in force simulation
        x: (Math.random() - 0.5) * w * 0.3,
        y: (Math.random() - 0.5) * h * 0.3,
        size: getNodeSize(node.label, connectivity), // 分层尺寸 / type-based size
        color: getNodeColor(node.label, connectivity), // 分层着色 / type-based color
        label: truncateLabel(node.name, 30),
        fullLabel: node.name,
        nodeType: node.label,
        domain: node.domain,
        subdomain: node.subdomain,
        confidence: node.confidence,
        connectivity,
      })
    }

    // ─── 3. 为缺失边端点创建幽灵节点 / Create Ghost Nodes for
    //     missing edge endpoints ────────────────────────────────
    // 当边引用的 source_id 或 target_id 不在节点表中时，
    // 创建一个视觉上可区分的幽灵占位节点，使边能够被渲染出来。
    // 这遵循"数据诚实"原则：不静默丢弃数据，让用户看到全貌。
    //
    // When an edge references a source_id or target_id not present
    // in the nodes table, create a visually distinct ghost placeholder
    // so the edge can still be rendered. This follows the principle
    // of data honesty: don't silently hide data — let users see it.
    //
    // 幽灵节点追踪集合（避免重复创建同一 ID）
    // Ghost node tracking set (avoid duplicate creation)
    const ghostNodeIds = new Set<string>()

    for (const edge of edges) {
      // ── 为缺失的源节点创建幽灵 / Create ghost for missing source ──
      if (!graph.hasNode(edge.source_id) && !ghostNodeIds.has(edge.source_id)) {
        // 从三节 ID 中解析出 domain 和 subdomain（容错：部分 ID 格式不标准）
        // Parse domain and subdomain from the triple-segment ID
        // (tolerant: some IDs may have non-standard formatting)
        const sourceParts = edge.source_id.split('::')
        const sourceDomain = sourceParts[0] || 'unknown'
        const sourceSubdomain = sourceParts[1] || 'unresolved'

        graph.addNode(edge.source_id, {
          x: (Math.random() - 0.5) * w * 0.3,
          y: (Math.random() - 0.5) * h * 0.3,
          size: 2,                              // 比真实节点 (3-11) 更小 / smaller than real nodes
          color: 'rgba(120,120,120,0.4)',       // 暗淡灰色 / dimmed gray
          label: '?',                           // 幽灵指示符 / ghost indicator
          fullLabel: `[未解析] ${edge.source_id.split('::').pop()}`,
                                                // 显示 ID 尾部便于识别 / show ID tail for identification
          nodeType: 'Entity',
          domain: sourceDomain,
          subdomain: sourceSubdomain,
          confidence: 0,                        // 幽灵节点无置信度 / ghost nodes have no confidence
          connectivity: 0,                      // 初始连接度 / initial connectivity
          isGhost: true,                        // ← 标记为幽灵节点 / mark as ghost
        })
        ghostNodeIds.add(edge.source_id)
      }

      // ── 为缺失的目标节点创建幽灵 / Create ghost for missing target ──
      if (!graph.hasNode(edge.target_id) && !ghostNodeIds.has(edge.target_id)) {
        const targetParts = edge.target_id.split('::')
        const targetDomain = targetParts[0] || 'unknown'
        const targetSubdomain = targetParts[1] || 'unresolved'

        graph.addNode(edge.target_id, {
          x: (Math.random() - 0.5) * w * 0.3,
          y: (Math.random() - 0.5) * h * 0.3,
          size: 2,
          color: 'rgba(120,120,120,0.4)',
          label: '?',
          fullLabel: `[未解析] ${edge.target_id.split('::').pop()}`,
          nodeType: 'Entity',
          domain: targetDomain,
          subdomain: targetSubdomain,
          confidence: 0,
          connectivity: 0,
          isGhost: true,
        })
        ghostNodeIds.add(edge.target_id)
      }
    }

    // ─── 4. 添加边 / Add edges ─────────────────────────────────
    // 跳过重复的无向边（幽灵节点已补全，边不会再因缺失端点被跳过）
    // Skip duplicate undirected edges (ghost nodes fill in missing
    // endpoints, so edges will no longer be silently dropped)
    const addedUndirected = new Set<string>()

    for (const edge of edges) {
      if (!graph.hasNode(edge.source_id) || !graph.hasNode(edge.target_id)) {
        continue
      }
      // 对于无向边，检查 src→tgt 或 tgt→src 是否已存在
      // For undirected edges, check if src→tgt or tgt→src already exists
      const undirectedKey = [edge.source_id, edge.target_id].sort().join('||')
      if (addedUndirected.has(undirectedKey)) continue
      addedUndirected.add(undirectedKey)

      // 避免添加已存在的有向边 / avoid adding duplicate directed edges
      if (graph.hasEdge(edge.source_id, edge.target_id)) continue
      if (graph.hasEdge(edge.target_id, edge.source_id)) continue

      graph.addEdge(edge.source_id, edge.target_id, {
        weight: edge.confidence,
        kind: edge.kind,
        size: 0.5 + edge.confidence * 1.5, // 0.5..2.0
      })
    }

    // ─── 5. 运行 d3-force 力导向布局 / Run force simulation ───
    // 注意：这里刻意复用 graphology 的属性对象引用（而非拷贝），使 d3-force
    // 写入的 x/y 坐标直接落回图节点属性。同时给每个对象注入其节点 ID —— 因为
    // 下方 forceLink 的 links 用字符串 ID 作 source/target，d3-force 需按
    // 节点的 `id` 字段匹配（见 forceLink(...).id(d => d.id)）。缺此字段时
    // d3 会退回默认的 index 访问器，导致 "node not found: <id>" 错误。
    //
    // Reuse graphology's attribute object references (not copies) so d3-force's
    // x/y writes land back on the graph nodes. Also inject each node's ID: the
    // forceLink below uses string IDs as link source/target, so d3-force must
    // match nodes by their `id` field (see forceLink(...).id(d => d.id)).
    // Without it, d3 falls back to the index accessor and throws "node not found".
    const simNodes = graph.nodes().map(id => {
      const attrs = graph.getNodeAttributes(id)
      ;(attrs as any).id = id
      return attrs
    })

    // ─── 5a. 领域聚类锚点 / Per-domain clustering anchors ─────────
    // 让「同一 domain 下的节点」自然聚成一团。做法：为每个 domain 计算一个
    // 确定性锚点（在画布中心外圈按 domain 名 hash 分布到一个圆环上），再对
    // 每个节点施加朝其 domain 锚点的弱 forceX/forceY。这与 composes/provides
    // 结构边的 link 力叠加——结构边把层级拉在一起，锚点把领域推开成独立团簇，
    // 二者共同产生"按领域分区、层级向心"的双层聚类布局。
    //
    // Make nodes sharing a domain cluster together. For each domain we compute a
    // deterministic anchor (placed on a ring around the canvas center via a hash
    // of the domain name), then apply a weak forceX/forceY pulling each node toward
    // its domain's anchor. Combined with the composes/provides link forces, this
    // yields a two-layer, domain-partitioned clustered layout.
    const domains = Array.from(new Set(simNodes.map((n: any) => n.domain || ''))).filter(Boolean)
    const domainAnchors = new Map<string, { x: number; y: number }>()
    const ringRadius = Math.min(w, h) * 0.32 // 团簇环半径 / cluster ring radius
    domains.forEach((dom) => {
      // 用 hash 决定角度（确定性、稳定），使同名 domain 每次落在同一方位。
      // Hash → angle (deterministic) so a given domain always lands in the same sector.
      const angle = (hashString(dom) % 360) * (Math.PI / 180)
      domainAnchors.set(dom, {
        x: w / 2 + Math.cos(angle) * ringRadius,
        y: h / 2 + Math.sin(angle) * ringRadius,
      })
    })
    // 单一 domain（或全空）时不做聚类推开——避免把唯一团簇拽向偏心锚点。
    // With a single domain (or none), skip clustering to keep it centered.
    const enableClustering = domains.length > 1
    const anchorX = (d: any) => (domainAnchors.get(d.domain || '')?.x ?? w / 2)
    const anchorY = (d: any) => (domainAnchors.get(d.domain || '')?.y ?? h / 2)

    const sim = forceSimulation(simNodes as any)
      .force('link', forceLink(
        graph.edges().map(e => ({
          source: graph.source(e),
          target: graph.target(e),
          weight: graph.getEdgeAttributes(e).weight,
        })),
      ).id((d: any) => d.id).distance(80).strength(0.3))
      .force('charge', forceManyBody().strength(-300).distanceMax(400))
      .force('center', forceCenter(w / 2, h / 2))
      .force('collision', forceCollide<{ x: number; y: number; size: number }>()
        .radius(d => (d.size || 4) + 4).strength(1))
      // 领域向心力（弱强度，仅在多 domain 时启用）/ domain-cluster gravity
      .force('clusterX', enableClustering ? forceX<any>(anchorX).strength(0.12) : null)
      .force('clusterY', enableClustering ? forceY<any>(anchorY).strength(0.12) : null)
      .stop()

    // 同步运行 ticks（离线计算布局，无需动画帧）
    // Run ticks synchronously (offline layout, no animation frames needed)
    const numTicks = Math.max(100, Math.min(300, nodes.length))
    for (let i = 0; i < numTicks; i++) {
      sim.tick()
    }

    this.simulation = sim
    return graph
  }

  /**
   * 获取当前 graphology 实例。
   * Returns the current graphology instance, or null if not yet built.
   */
  getGraph(): Graph<GraphNodeAttributes, GraphEdgeAttributes> | null {
    return this.graph
  }

  /**
   * 销毁图与力导向模拟，释放资源。
   * Destroys the graph and force simulation to free resources.
   */
  destroy(): void {
    if (this.simulation) {
      this.simulation.stop()
      this.simulation = null
    }
    if (this.graph) {
      this.graph.clear()
      this.graph = null
    }
  }
}
