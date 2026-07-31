// ============================================================
// graph-adapters — 图数据适配层 / Graph Data Adapters
//
// 单一职责：把 graphology Graph 的节点/边属性映射为上层可消费的形状，
// 供 2D (SigmaCanvas) 与 3D (ForceGraph3DCanvas) 两个渲染器复用（DRY）。
// 这里是「graphology 属性 → KGNode/KGEdge」以及「graphology → ForceGraph
// {nodes, links}」两类映射的唯一事实来源，任一渲染器都不再各写一套。
//
// Single responsibility: map graphology node/edge attributes into shapes the
// upper layers consume. Shared by both the 2D (SigmaCanvas) and 3D
// (ForceGraph3DCanvas) renderers (DRY). This module is the single source of
// truth for "graphology attrs → KGNode/KGEdge" and "graphology → ForceGraph
// {nodes, links}" — neither renderer re-implements these.
// ============================================================

import type Graph from 'graphology'
import type { KGNode, KGEdge, NodeLabel, EdgeKind } from '../../types'

// ─── 边类型配色 / Edge Kind Color Map ────────────────────────────
// 方案 B 双层模型：按 kind 区分边样式，让「结构边」（provides/composes）与
// 「语义边」（triggers/depends_on/...）在视觉上一眼可辨。返回 [r,g,b] 基色，
// 透明度由各渲染器按 weight 叠加（见 edgeKindColorCss）。
// 2D 与 3D 共用同一张表——这是边配色的唯一事实来源。
//
// Plan-B: color edges by kind so structural edges (provides/composes) are
// visually distinct from semantic edges. Returns an [r,g,b] base color; each
// renderer adds weight-based opacity (see edgeKindColorCss). Shared by 2D and
// 3D — the single source of truth for edge coloring.
export const EDGE_KIND_COLOR: Record<EdgeKind, [number, number, number]> = {
  provides: [230, 170, 60],    // 暖金 / warm gold (skill → domain)
  composes: [80, 175, 200],    // 青蓝 / teal (hierarchy)
  triggers: [220, 130, 70],    // 琥珀 / amber
  depends_on: [110, 150, 220], // 蓝 / blue
  references: [140, 140, 160], // 灰蓝 / grey-blue
  generalizes: [130, 190, 140],// 绿 / green
  contradicts: [210, 90, 110], // 红 / red
}

/**
 * 按边的 kind + weight 计算 CSS rgba 颜色字符串。
 * 与 SigmaCanvas.edgeReducer 使用完全相同的配色表与透明度公式，
 * 保证 2D/3D 边观感一致（DRY）。
 *
 * Compute a CSS rgba color string from an edge's kind + weight. Uses the exact
 * same color table and opacity formula as SigmaCanvas.edgeReducer, so 2D/3D
 * edges look consistent (DRY).
 */
export function edgeKindColorCss(kind: EdgeKind | undefined, weight: number | undefined): string {
  // 未知 kind 兜底为原有的灰蓝色 / fall back to the original grey-blue
  const rgb = (kind && EDGE_KIND_COLOR[kind]) ?? [100, 120, 160]
  const opacity = 0.25 + (weight ?? 0.5) * 0.5
  return `rgba(${rgb[0]}, ${rgb[1]}, ${rgb[2]}, ${opacity})`
}

/**
 * 从 graphology node attributes 映射回 KGNode 形状。
 * attributes 由 GraphEngine 注入，其中包含原始 KGNode 字段的镜像。
 *
 * Map graphology node attributes back to a KGNode shape. The attributes are
 * injected by GraphEngine and mirror the original KGNode fields.
 */
export function attrsToKGNode(nodeId: string, attrs: any): KGNode {
  // ── 幽灵节点：label 字段需要是合法 NodeLabel，不能是 '?' ──
  // Ghost nodes: the label field must be a valid NodeLabel, not '?'.
  // attrs.label 是显示用短标签（幽灵节点为 '?'），attrs.nodeType 才是类型枚举值。
  // attrs.label is the display short label ('?' for ghosts), attrs.nodeType holds the enum.
  const isGhost = attrs.isGhost === true
  const nodeLabel: NodeLabel = isGhost
    ? 'Entity'
    : (attrs.nodeType ?? attrs.label ?? 'Entity') as NodeLabel

  return {
    id: attrs.id ?? nodeId,
    label: nodeLabel,
    name: attrs.fullLabel ?? attrs.label ?? nodeId,
    summary: attrs.summary ?? '',
    synonyms: attrs.synonyms ?? null,
    domain: attrs.domain ?? '',
    subdomain: attrs.subdomain ?? '',
    description: attrs.description ?? null,
    properties: attrs.properties ?? null,
    tags: attrs.tags ?? null,
    related_entities: attrs.related_entities ?? null,
    visibility: attrs.visibility ?? 'public',
    confidence: attrs.confidence ?? 0.5,
    provenance: attrs.provenance ?? 'llm_inferred',
    source_refs: attrs.source_refs ?? null,
    created_at: attrs.created_at ?? new Date().toISOString(),
    updated_at: attrs.updated_at ?? new Date().toISOString(),
  }
}

/**
 * 最小图访问契约：仅需 source(edgeId)/target(edgeId) 两个方法。
 * graphology Graph 天然满足；3D 渲染器可用轻量 shim 满足（其 link 已带
 * source/target），从而复用同一个 attrsToKGEdge。
 *
 * Minimal graph-access contract: just source(edgeId)/target(edgeId). A real
 * graphology Graph satisfies it; the 3D renderer can satisfy it with a tiny
 * shim (its links already carry source/target), reusing the same attrsToKGEdge.
 */
export interface EdgeEndpointResolver {
  source(edgeId: string): string
  target(edgeId: string): string
}

/**
 * 从 graphology edge attributes 映射回 KGEdge 形状。
 * 通过 EdgeEndpointResolver 获取两端节点 ID，故对 2D 的真实 graph 和 3D 的
 * shim 都适用。
 *
 * Map graphology edge attributes back to a KGEdge shape. Endpoint IDs come via
 * the EdgeEndpointResolver, so this works for both the real 2D graph and the
 * 3D shim.
 */
export function attrsToKGEdge(edgeId: string, graph: EdgeEndpointResolver, attrs: any): KGEdge {
  return {
    id: attrs.id ?? 0,
    source_id: graph.source(edgeId),
    target_id: graph.target(edgeId),
    kind: attrs.kind ?? 'references',
    description: attrs.description ?? null,
    properties: attrs.properties ?? null,
    provenance: attrs.provenance ?? 'llm_inferred',
    confidence: attrs.weight ?? attrs.confidence ?? 0.5,
    source_refs: attrs.source_refs ?? null,
    bidirectional: attrs.bidirectional ?? false,
    cardinality: attrs.cardinality ?? null,
    created_at: attrs.created_at ?? new Date().toISOString(),
  }
}

// ─── graphology → ForceGraph 适配 / graphology → ForceGraph ──────

/**
 * react-force-graph 节点：id + graphology 节点属性（color/size/label/
 * fullLabel/nodeType/domain/... 的镜像）。d3-force 会在其上写入 x/y/z 坐标。
 *
 * A react-force-graph node: id plus a mirror of the graphology node attributes
 * (color/size/label/fullLabel/nodeType/domain/...). d3-force writes x/y/z onto it.
 */
export interface ForceGraphNode {
  id: string
  [attr: string]: any
}

/**
 * react-force-graph 边：graphology 边 key 作 id，source/target 为端点节点 ID
 * 字符串（d3-force 运行后会被替换为节点对象引用），外加 graphology 边属性
 * （weight/kind/size）。
 *
 * A react-force-graph link: the graphology edge key as id, source/target as
 * endpoint node-ID strings (d3-force replaces them with node object refs after
 * simulation), plus the graphology edge attributes (weight/kind/size).
 */
export interface ForceGraphLink {
  id: string
  source: string
  target: string
  [attr: string]: any
}

/** ForceGraph 数据形状 / ForceGraph data shape */
export interface ForceGraphData {
  nodes: ForceGraphNode[]
  links: ForceGraphLink[]
}

/**
 * 把 graphology Graph 适配成 react-force-graph 的 {nodes, links} 结构。
 *
 * - 节点：{ id, ...attrs } —— 直接携带 GraphEngine 已计算好的 color/size 等
 *   属性，因此 3D 渲染器无需重算颜色/尺寸，天然复用 graph-engine 的
 *   NODE_TYPE_STYLE 分层着色逻辑（DRY）。
 * - 边：{ id(=graphology edge key), source, target, ...attrs } —— source/target
 *   用节点 ID 字符串，react-force-graph 依 nodeId 字段（默认 'id'）解析。
 *
 * Adapt a graphology Graph into react-force-graph's {nodes, links} shape.
 *
 * - Nodes: { id, ...attrs } — carry the color/size already computed by
 *   GraphEngine, so the 3D renderer reuses graph-engine's NODE_TYPE_STYLE
 *   layered coloring without recomputing anything (DRY).
 * - Links: { id(=graphology edge key), source, target, ...attrs } — source/target
 *   are node-ID strings, resolved by react-force-graph via the nodeId field.
 */
export function graphologyToForceGraph(graph: Graph): ForceGraphData {
  const nodes: ForceGraphNode[] = graph.mapNodes((id, attrs) => ({
    ...(attrs as object),
    id, // id 置后，防止 attrs 里潜在的 id 覆盖真实节点键 / put id last so attrs can't clobber the real node key
  }))

  const links: ForceGraphLink[] = graph.mapEdges((edgeId, attrs, source, target) => ({
    ...(attrs as object),
    id: edgeId,
    source,
    target,
  }))

  return { nodes, links }
}
