// ============================================================
// DetailPanel — 详情面板 / Detail Panel
//
// 当用户点击图中的节点或边时，从右侧滑入显示详细属性。
// 支持：
//   - 节点详情：name, id, type, domain, subdomain, confidence,
//     provenance, description, synonyms
//   - 边详情：kind, source_id, target_id, confidence, description
//   - 关闭按钮与 Esc 键关闭
//
// Slides in from the right when the user clicks a node or edge
// in the graph. Displays detailed metadata fields with icons.
// Supports close via the X button or the Esc key.
// ============================================================

import { useEffect, useMemo } from 'react'
import { useUIStore } from '../../stores/uiStore'
import { useGraphStore } from '../../stores/graphStore'
import type { KGNode } from '../../types'
import {
  X,
  Tag,
  Link2,
  BarChart3,
  Hash,
  Shield,
  FileText,
  GitBranch,
  ArrowRight,
  Layers,
  Boxes,
  Wrench,
} from 'lucide-react'
import { motion, AnimatePresence } from 'framer-motion'

// ─── 边类型本地化标签 / Edge Kind Localized Labels ──────────────────

/**
 * 边关系类型的中文标签映射表。
 * 用于在详情面板中将英文 kind 字段展示为可读的双语文本。
 *
 * Mapping from edge kind enum value to a human-readable
 * Chinese-English label displayed in the detail panel.
 */
const EDGE_KIND_LABELS: Record<string, string> = {
  triggers: '触发 / Triggers',
  depends_on: '依赖 / Depends On',
  references: '引用 / References',
  generalizes: '泛化 / Generalizes',
  composes: '组合 / Composes',
  contradicts: '矛盾 / Contradicts',
  provides: '提供 / Provides',
}

// ─── 节点类型本地化标签 / Node Type Localized Labels ────────────────
// 方案 B 五类节点的双语显示名，用于详情面板"类型"字段。
// Bilingual display names for the 5 Plan-B node types.
const NODE_TYPE_LABELS: Record<string, string> = {
  Entity: '实体 / Entity',
  Concept: '概念 / Concept',
  Skill: '技能 / Skill',
  Domain: '领域 / Domain',
  Subdomain: '子域 / Subdomain',
}

// ─── 来源标签映射 / Provenance Label Mapping ────────────────────────

/**
 * 数据来源的中文标签映射表。
 *
 * Mapping from provenance enum value to a human-readable
 * Chinese-English label.
 */
const PROVENANCE_LABELS: Record<string, string> = {
  llm_inferred: 'LLM 推断 / LLM Inferred',
  human_curated: '人工整理 / Human Curated',
  extraction: '规则抽取 / Extraction',
}

// ─── 工具组件 / Utility Sub-Components ──────────────────────────────

/**
 * 置信度进度条与百分比文本。
 *
 * 渲染一个填充的进度条，宽度由 confidence (0-1) 决定，
 * 颜色根据置信度等级动态变化：低 (<0.4) 为警告色，中 (<0.7)
 * 为信息色，高 (>=0.7) 为成功色。
 *
 * Confidence progress bar with percentage text.
 *
 * Renders a filled bar whose width is determined by confidence (0-1).
 * Color changes by level: low (<0.4) warning, medium (<0.7) info,
 * high (>=0.7) success.
 */
function ConfidenceBar({ value }: { value: number }) {
  const pct = Math.round(value * 100)

  const color =
    pct < 40
      ? 'var(--warning)'
      : pct < 70
        ? 'var(--info)'
        : 'var(--success)'

  return (
    <div className="flex items-center gap-2">
      <div
        className="flex-1 h-1.5 rounded-full overflow-hidden"
        style={{ background: 'var(--bg-hover)' }}
      >
        <motion.div
          initial={{ width: 0 }}
          animate={{ width: `${pct}%` }}
          transition={{ duration: 0.5, ease: 'easeOut' }}
          className="h-full rounded-full"
          style={{ background: color }}
        />
      </div>
      <span
        className="text-xs font-mono shrink-0"
        style={{ color: 'var(--text-secondary)' }}
      >
        {pct}%
      </span>
    </div>
  )
}

/**
 * 元数据字段行组件。
 *
 * 渲染一个带图标的标签-值对，用于展示节点或边的单个元数据字段。
 *
 * Metadata field row. Renders an icon + label + value triplet
 * for displaying a single metadata field of a node or edge.
 */
function MetaField({
  icon: Icon,
  label,
  value,
  mono,
}: {
  icon: React.ElementType<{ size?: number | string; style?: React.CSSProperties }>
  label: string
  value: string
  mono?: boolean
}) {
  return (
    <div className="flex items-start gap-2 py-1.5">
      <Icon
        size={14}
        style={{ color: 'var(--text-muted)', flexShrink: 0, marginTop: 1 }}
      />
      <div className="min-w-0 flex-1">
        <div
          className="text-[10px] uppercase tracking-wide mb-0.5"
          style={{ color: 'var(--text-muted)' }}
        >
          {label}
        </div>
        <p
          className={`text-sm leading-snug break-words ${mono ? 'font-mono text-xs' : ''}`}
          style={{ color: mono ? 'var(--text-secondary)' : 'var(--text-primary)' }}
        >
          {value}
        </p>
      </div>
    </div>
  )
}

// ─── 组件 / Component ──────────────────────────────────────────────

/**
 * 详情面板组件。
 *
 * 从右侧滑入的面板，展示当前选中节点或边的详细属性。
 * 通过 UIStore 获取选中状态，响应 Esc 键关闭。
 * 使用 AnimatePresence 确保退出动画在 DOM 移除前完整播放。
 *
 * Detail panel component.
 *
 * A panel that slides in from the right showing detailed properties
 * of the currently selected node or edge. Selection state is read
 * from the UIStore; dismisses on Esc. Uses AnimatePresence to ensure
 * exit animations complete before DOM removal.
 */
export function DetailPanel() {
  // ── Store 选择器 / Store Selectors ────────────────────────────

  /** 当前选中的节点 / currently selected node */
  const selectedNode = useUIStore((s) => s.selectedNode)

  /** 当前选中的边 / currently selected edge */
  const selectedEdge = useUIStore((s) => s.selectedEdge)

  /** 全图节点/边（用于双向查询）/ all nodes+edges (for bidirectional lookup) */
  const allNodes = useGraphStore((s) => s.nodes)
  const allEdges = useGraphStore((s) => s.edges)

  /** 详情面板是否打开 / whether the detail panel is open */
  const detailPanelOpen = useUIStore((s) => s.detailPanelOpen)

  /** 选中节点（传 null 取消选中）/ select or deselect a node */
  const selectNode = useUIStore((s) => s.selectNode)

  /** 选中边（传 null 取消选中）/ select or deselect an edge */
  const selectEdge = useUIStore((s) => s.selectEdge)

  // ── 取回权威完整节点 / Resolve the authoritative full node ──────
  // 【问题4 根因】图中点击得到的 selectedNode 来自 graphology attributes，
  // 而 GraphEngine.buildGraph 只往图里存了少量渲染字段（label/size/color/
  // domain/subdomain/confidence 等），裁剪掉了 summary/description/synonyms/
  // source_refs 等领域知识内容。于是详情面板拿不到这些字段。
  // 修复：用 selectedNode.id 从 graphStore 的全量 nodes（db-loader 以 SELECT *
  // 读入，字段完整）取回权威节点，取不到再回退 selectedNode。这与下方
  // relatedLinks 已有的 nodeById 思路一致（DRY），且不需要让图去存所有字段，
  // 保持 2D/3D 图数据精简（关注点分离：图只管渲染，面板从 store 取完整数据）。
  //
  // [Problem #4 root cause] The clicked selectedNode comes from graphology
  // attributes, but GraphEngine.buildGraph stores only a few render fields onto
  // the graph, dropping summary/description/synonyms/source_refs. So the panel
  // never sees them. Fix: resolve the authoritative full node from graphStore's
  // complete nodes (db-loader reads them via SELECT *) by id, falling back to
  // selectedNode. Same nodeById idea already used by relatedLinks below (DRY),
  // and it avoids bloating the 2D/3D graph data with every field (separation of
  // concerns: the graph just renders; the panel pulls full data from the store).
  const node: KGNode | null = useMemo(() => {
    if (!selectedNode) return null
    const full = allNodes.find((n) => n.id === selectedNode.id)
    return full ?? selectedNode
  }, [selectedNode, allNodes])

  // ── Esc 键监听 / Esc Key Listener ────────────────────────────

  useEffect(() => {
    /**
     * 监听键盘 Esc 键以关闭详情面板。
     * 仅在详情面板处于打开状态时附加监听器。
     *
     * Listen for the Esc key to dismiss the detail panel.
     * Only attaches the listener when the panel is open.
     */
    if (!detailPanelOpen) return

    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        selectNode(null)
        selectEdge(null)
      }
    }

    window.addEventListener('keydown', handleKeyDown)
    return () => window.removeEventListener('keydown', handleKeyDown)
  }, [detailPanelOpen, selectNode, selectEdge])

  // ── 解析同义词 / Parse Synonyms ──────────────────────────────

  /**
   * 尝试将 synonyms JSON 字符串解析为字符串数组，
   * 用于以标签形式展示。解析失败时返回空数组。
   *
   * Attempt to parse the synonyms JSON string into a string array
   * for display as tags. Returns an empty array on parse failure.
   */
  const synonymsList: string[] = useMemo(() => {
    if (!node?.synonyms) return []
    try {
      const parsed = JSON.parse(node.synonyms)
      return Array.isArray(parsed) ? parsed : []
    } catch {
      return []
    }
  }, [node?.synonyms])

  // ── 双向查询：skill ⇄ domain ──────────────────────────────────
  // 方案 B 双层模型的核心交互：
  //   · 点击 Skill  → 列出它 provides 的所有 Domain（正向）
  //   · 点击 Domain → 列出它的来源 Skill（反向）
  // 数据来源双管齐下、去重合并，保证鲁棒：
  //   1) provides 边（skill::X --provides--> domain::Y）——权威结构边
  //   2) Domain.source_refs（逗号分隔的来源 skill 名，形如 "a,b,c"）——
  //      与 skill 节点 id（"skill::<name>"）或 name 匹配
  // 返回 { title, items[] }，items 为可点击跳转的关联节点；
  // 非 skill/domain 节点返回 null（不渲染该区块）。
  //
  // Bidirectional query — the core interaction of the Plan-B two-layer model:
  //   · Click a Skill  → list all Domains it provides (forward)
  //   · Click a Domain → list its source Skills (reverse)
  // Both directions merge two sources (deduped) for robustness:
  //   1) provides edges (authoritative structural edges)
  //   2) Domain.source_refs (comma-separated skill names)
  // Returns { title, items[] } of clickable related nodes, or null for
  // node types that are neither Skill nor Domain.
  const relatedLinks = useMemo((): {
    title: string
    hint: string
    items: KGNode[]
  } | null => {
    if (!selectedNode) return null
    const nodeById = new Map(allNodes.map((n) => [n.id, n]))
    // 点击的节点来自 graph attributes（字段稀疏，无 source_refs），
    // 用 id 从 graphStore 全量数据取回权威节点，确保 source_refs 等字段完整。
    // Clicked nodes carry sparse graph attributes (no source_refs); resolve the
    // authoritative full node from graphStore by id so source_refs is populated.
    const node = nodeById.get(selectedNode.id) ?? selectedNode

    // ── 正向：Skill → provides → Domain ──
    if (node.label === 'Skill') {
      const domains = new Map<string, KGNode>()
      // (1) provides 边 / provides edges
      for (const e of allEdges) {
        if (e.kind === 'provides' && e.source_id === node.id) {
          const d = nodeById.get(e.target_id)
          if (d) domains.set(d.id, d)
        }
      }
      // (2) 反查 source_refs：domain 的来源列表包含本 skill 名 / reverse via source_refs
      const skillName = node.name
      for (const n of allNodes) {
        if (n.label !== 'Domain' || !n.source_refs) continue
        const refs = n.source_refs.split(',').map((s) => s.trim())
        if (refs.includes(skillName)) domains.set(n.id, n)
      }
      return {
        title: '提供的领域 / Provides Domains',
        hint: '此技能作为来源支撑以下领域',
        items: Array.from(domains.values()),
      }
    }

    // ── 反向：Domain → source Skills ──
    if (node.label === 'Domain') {
      const skills = new Map<string, KGNode>()
      // (1) source_refs 逗号分隔的来源 skill 名 → 匹配 skill 节点 / parse source_refs
      if (node.source_refs) {
        for (const rawName of node.source_refs.split(',')) {
          const name = rawName.trim()
          if (!name) continue
          // 优先按 id 约定 "skill::<name>" 匹配，回退按 name 匹配
          // Prefer id convention "skill::<name>", fall back to name match.
          const byId = nodeById.get(`skill::${name}`)
          const hit = byId ?? allNodes.find((n) => n.label === 'Skill' && n.name === name)
          if (hit) skills.set(hit.id, hit)
        }
      }
      // (2) provides 边反查（target 为本 domain 的 source skill）/ reverse via provides edges
      for (const e of allEdges) {
        if (e.kind === 'provides' && e.target_id === node.id) {
          const s = nodeById.get(e.source_id)
          if (s) skills.set(s.id, s)
        }
      }
      return {
        title: '来源技能 / Source Skills',
        hint: '以下技能贡献了此领域的知识',
        items: Array.from(skills.values()),
      }
    }

    return null
  }, [selectedNode, allNodes, allEdges])

  // ── 渲染 / Render ────────────────────────────────────────────

  return (
    <AnimatePresence>
      {detailPanelOpen && (selectedNode || selectedEdge) && (
        <motion.aside
          key="detail-panel"
          initial={{ x: 380 }}
          animate={{ x: 0 }}
          exit={{ x: 380 }}
          transition={{ type: 'spring', stiffness: 400, damping: 40 }}
          className="flex-shrink-0 border-l overflow-y-auto h-full flex flex-col relative"
          style={{
            width: 380,
            background: 'var(--bg-main)',
            borderColor: 'var(--border-default)',
            // zIndex 保险：确保详情面板始终层叠在图画布之上。ForceGraph3D 的
            // canvas 是 position:absolute，若无显式层级，详情面板可能被其覆盖
            // （配合 main 的 overflow-hidden 形成双重保障）。
            // zIndex safeguard: keep the panel stacked above the graph canvas.
            // ForceGraph3D's canvas is position:absolute; without an explicit
            // z-index the panel could be covered (paired with main's overflow-hidden).
            zIndex: 20,
          }}
        >
          {/* ── 面板标题栏 / Panel Header ──────────────────────── */}
          <div
            className="px-4 py-3 border-b flex items-center justify-between sticky top-0 z-10 shrink-0"
            style={{
              background: 'var(--bg-main)',
              borderColor: 'var(--border-default)',
            }}
          >
            <h2
              className="text-sm font-semibold select-none"
              style={{ color: 'var(--text-primary)' }}
            >
              {selectedNode ? '节点详情 / Node Detail' : '边详情 / Edge Detail'}
            </h2>

            {/* 关闭按钮 / Close Button */}
            <button
              onClick={() => {
                selectNode(null)
                selectEdge(null)
              }}
              className="p-1 rounded transition-colors hover:opacity-80"
              style={{ color: 'var(--text-muted)' }}
              title="关闭 / Close (Esc)"
            >
              <X size={18} />
            </button>
          </div>

          {/* ── 详情内容 / Detail Content ──────────────────────── */}
          <div className="p-4 space-y-4">
            {/* ================================================ */}
            {/* 节点详情 / Node Detail                           */}
            {/* ================================================ */}
            {/* 使用取回的权威完整节点 node（含 summary/description/synonyms/ */}
            {/* source_refs），而非稀疏的 selectedNode。node 在 selectedNode 非空时 */}
            {/* 必非空（见上方 useMemo），故此 gate 同时用于 TS 非空收窄。 */}
            {/* Render from the resolved full node (has summary/description/etc.), */}
            {/* not the sparse selectedNode. node is non-null whenever selectedNode */}
            {/* is (see memo above), so this gate also narrows the type for TS. */}
            {node && (
              <>
                {/* ── 幽灵节点警告横幅 / Ghost Node Warning Banner ── */}
                {/* 检测标准：name 以 '[未解析]' 开头（由 GraphEngine.buildGraph 设置）*/}
                {/* Detection: name starts with '[未解析]' (set by GraphEngine.buildGraph) */}
                {node.name.startsWith('[未解析]') && (
                  <div
                    className="rounded-lg p-3 border"
                    style={{
                      background: 'rgba(180, 120, 50, 0.08)',
                      borderColor: 'rgba(200, 140, 60, 0.3)',
                    }}
                  >
                    {/* 徽章 / Badge */}
                    <div className="flex items-center gap-2 mb-2">
                      <span
                        className="px-2 py-0.5 rounded text-[11px] font-semibold"
                        style={{
                          background: 'rgba(200, 140, 60, 0.2)',
                          color: '#d4a050',
                        }}
                      >
                        {`[未解析引用]`}
                      </span>
                      <span
                        className="text-[10px]"
                        style={{ color: 'var(--text-muted)' }}
                      >
                        Unresolved Reference
                      </span>
                    </div>
                    {/* 说明文本 / Explanation */}
                    <p
                      className="text-xs leading-relaxed"
                      style={{ color: 'var(--text-secondary)' }}
                    >
                      {`此节点 ID 被图中的边所引用，但在节点表（nodes table）中不存在对应的记录。它被渲染为"幽灵节点"占位符以便边仍然可见。`}
                    </p>
                    <p
                      className="text-[11px] leading-relaxed mt-1.5 opacity-60"
                      style={{ color: 'var(--text-secondary)' }}
                    >
                      This node ID is referenced by edges in the graph but has no
                      corresponding record in the nodes table. It is rendered as a
                      &ldquo;ghost node&rdquo; placeholder so the edges remain visible.
                    </p>
                    {/* 显示被引用的完整 ID / Show the full referenced ID */}
                    <div className="mt-2">
                      <span
                        className="text-[10px] uppercase tracking-wide"
                        style={{ color: 'var(--text-muted)' }}
                      >
                        被引用的 ID / Referenced ID
                      </span>
                      <p
                        className="text-[11px] font-mono break-all leading-relaxed mt-0.5"
                        style={{ color: 'var(--text-secondary)' }}
                      >
                        {node.id}
                      </p>
                    </div>
                  </div>
                )}

                {/* 名称 / Name — 大号主标题 */}
                <div>
                  <h3
                    className="text-lg font-semibold leading-tight"
                    style={{ color: 'var(--text-primary)' }}
                  >
                    {node.name}
                  </h3>
                </div>

                {/* ID — 等宽小字 */}
                <div>
                  <p
                    className="text-[11px] font-mono break-all leading-relaxed opacity-60"
                    style={{ color: 'var(--text-secondary)' }}
                  >
                    {node.id}
                  </p>
                </div>

                {/* 分隔线 / Divider */}
                <hr
                  className="border-0 h-px"
                  style={{ background: 'var(--border-default)' }}
                />

                {/* 元数据网格 / Metadata Grid */}
                <div className="divide-y" style={{ borderColor: 'var(--border-default)' }}>
                  {/* 节点类型 / Node Type */}
                  {/* 方案 B 五类节点均需正确显示（不再只区分 Entity/Concept）。 */}
                  {/* All 5 Plan-B node types must display correctly (not just Entity/Concept). */}
                  <MetaField
                    icon={Tag}
                    label="类型 / Type"
                    value={NODE_TYPE_LABELS[node.label] ?? node.label}
                  />

                  {/* 所属领域 / Domain */}
                  <MetaField
                    icon={Hash}
                    label="领域 / Domain"
                    value={node.domain}
                  />

                  {/* 所属子域 / Subdomain */}
                  <MetaField
                    icon={Layers}
                    label="子域 / Subdomain"
                    value={node.subdomain}
                  />

                  {/* 数据来源 / Provenance */}
                  <MetaField
                    icon={Shield}
                    label="来源 / Provenance"
                    value={
                      PROVENANCE_LABELS[node.provenance] ??
                      node.provenance
                    }
                  />
                </div>

                {/* 置信度 / Confidence — 独立区块（含进度条） */}
                {/* 【问题3 修复】仅当 confidence > 0 时显示。数据事实：Entity/Concept */}
                {/* 由 LLM 赋予置信度(如 0.9)，而 Skill/Domain/Subdomain 是 ingest 物化的 */}
                {/* 结构节点，confidence=0.0。对结构节点显示"置信度 0%"无意义且误导，故 */}
                {/* 隐藏该区块（结构节点的可信度不来自概率评分，而来自其结构地位）。 */}
                {/* [Problem #3 fix] Show only when confidence > 0. Fact: Entity/Concept */}
                {/* get an LLM confidence (e.g. 0.9), while Skill/Domain/Subdomain are */}
                {/* ingest-materialized structural nodes with confidence=0.0. Showing */}
                {/* "0%" for them is meaningless and misleading, so hide the block. */}
                {node.confidence > 0 && (
                  <div className="py-1.5">
                    <div className="flex items-center gap-2 mb-1.5">
                      <BarChart3
                        size={14}
                        style={{ color: 'var(--text-muted)', flexShrink: 0 }}
                      />
                      <span
                        className="text-[10px] uppercase tracking-wide"
                        style={{ color: 'var(--text-muted)' }}
                      >
                        置信度 / Confidence
                      </span>
                    </div>
                    <div className="pl-6">
                      <ConfidenceBar value={node.confidence} />
                    </div>
                  </div>
                )}

                {/* 同义词标签 / Synonyms Tags */}
                {synonymsList.length > 0 && (
                  <div className="py-1.5">
                    <div className="flex items-center gap-2 mb-2">
                      <GitBranch
                        size={14}
                        style={{ color: 'var(--text-muted)', flexShrink: 0 }}
                      />
                      <span
                        className="text-[10px] uppercase tracking-wide"
                        style={{ color: 'var(--text-muted)' }}
                      >
                        同义词 / Synonyms
                      </span>
                    </div>
                    <div className="pl-6 flex flex-wrap gap-1.5">
                      {synonymsList.map((syn, i) => (
                        <span
                          key={i}
                          className="px-2 py-0.5 rounded text-[11px]"
                          style={{
                            background: 'var(--bg-card)',
                            color: 'var(--text-secondary)',
                            border: `1px solid var(--border-default)`,
                          }}
                        >
                          {syn}
                        </span>
                      ))}
                    </div>
                  </div>
                )}

                {/* 概要 / Summary */}
                {/* 【问题4】summary 是领域知识的一句话概括（domain 节点尤其有价值）。 */}
                {/* 原面板未渲染此字段；取回完整 node 后补充展示。仅在非空时显示。 */}
                {/* [Problem #4] summary is a one-line gist of the domain knowledge */}
                {/* (especially valuable for Domain nodes). The panel didn't render it */}
                {/* before; now shown from the resolved full node, only when non-empty. */}
                {node.summary && (
                  <div className="py-1.5">
                    <div className="flex items-center gap-2 mb-1.5">
                      <FileText
                        size={14}
                        style={{ color: 'var(--text-muted)', flexShrink: 0 }}
                      />
                      <span
                        className="text-[10px] uppercase tracking-wide"
                        style={{ color: 'var(--text-muted)' }}
                      >
                        概要 / Summary
                      </span>
                    </div>
                    <div className="pl-6">
                      <p
                        className="text-xs leading-relaxed"
                        style={{ color: 'var(--text-secondary)' }}
                      >
                        {node.summary}
                      </p>
                    </div>
                  </div>
                )}

                {/* 描述 / Description */}
                {node.description && (
                  <div className="py-1.5">
                    <div className="flex items-center gap-2 mb-1.5">
                      <FileText
                        size={14}
                        style={{ color: 'var(--text-muted)', flexShrink: 0 }}
                      />
                      <span
                        className="text-[10px] uppercase tracking-wide"
                        style={{ color: 'var(--text-muted)' }}
                      >
                        描述 / Description
                      </span>
                    </div>
                    <div className="pl-6">
                      <p
                        className="text-xs leading-relaxed"
                        style={{ color: 'var(--text-secondary)' }}
                      >
                        {node.description}
                      </p>
                    </div>
                  </div>
                )}

                {/* ── 双向查询：skill⇄domain 关联跳转 / Bidirectional links ── */}
                {/* 仅当选中节点是 Skill 或 Domain 且有关联时渲染。 */}
                {/* Only rendered for Skill/Domain nodes that have related links. */}
                {relatedLinks && relatedLinks.items.length > 0 && (
                  <div className="py-1.5">
                    <div className="flex items-center gap-2 mb-1.5">
                      {/* Skill→Domain 用 Boxes 图标，Domain→Skill 用 Wrench 图标 */}
                      {node.label === 'Skill' ? (
                        <Boxes size={14} style={{ color: 'var(--text-muted)', flexShrink: 0 }} />
                      ) : (
                        <Wrench size={14} style={{ color: 'var(--text-muted)', flexShrink: 0 }} />
                      )}
                      <span
                        className="text-[10px] uppercase tracking-wide"
                        style={{ color: 'var(--text-muted)' }}
                      >
                        {relatedLinks.title}
                      </span>
                      <span
                        className="ml-auto text-[10px] font-mono"
                        style={{ color: 'var(--text-muted)' }}
                      >
                        {relatedLinks.items.length}
                      </span>
                    </div>
                    <p className="pl-6 text-[11px] mb-2 opacity-70" style={{ color: 'var(--text-secondary)' }}>
                      {relatedLinks.hint}
                    </p>
                    {/* 可点击的关联节点列表：点击即在详情面板中跳转到该节点 */}
                    {/* Clickable related-node list: click to navigate the panel to that node. */}
                    <div className="pl-6 flex flex-col gap-1">
                      {relatedLinks.items.map((item) => (
                        <button
                          key={item.id}
                          onClick={() => selectNode(item)}
                          className="flex items-center gap-2 px-2 py-1.5 rounded text-left transition-colors hover:opacity-80"
                          style={{
                            background: 'var(--bg-card)',
                            border: '1px solid var(--border-default)',
                          }}
                          title={item.id}
                        >
                          <ArrowRight size={12} style={{ color: 'var(--text-muted)', flexShrink: 0 }} />
                          <span
                            className="text-xs truncate"
                            style={{ color: 'var(--text-primary)' }}
                          >
                            {item.name}
                          </span>
                          <span
                            className="ml-auto text-[9px] uppercase tracking-wide shrink-0"
                            style={{ color: 'var(--text-muted)' }}
                          >
                            {item.label}
                          </span>
                        </button>
                      ))}
                    </div>
                  </div>
                )}
              </>
            )}

            {/* ================================================ */}
            {/* 边详情 / Edge Detail                             */}
            {/* ================================================ */}
            {selectedEdge && (
              <>
                {/* 关系类型 / Edge Kind — 大号标题 */}
                <div>
                  <h3
                    className="text-lg font-semibold leading-tight"
                    style={{ color: 'var(--text-primary)' }}
                  >
                    {EDGE_KIND_LABELS[selectedEdge.kind] ?? selectedEdge.kind}
                  </h3>
                </div>

                {/* 分隔线 / Divider */}
                <hr
                  className="border-0 h-px"
                  style={{ background: 'var(--border-default)' }}
                />

                <div className="divide-y" style={{ borderColor: 'var(--border-default)' }}>
                  {/* 关系类型原始值 / Kind (raw value) */}
                  <MetaField
                    icon={Link2}
                    label="关系类型 / Kind"
                    value={selectedEdge.kind}
                    mono
                  />

                  {/* 源节点 ID / Source Node ID */}
                  <MetaField
                    icon={ArrowRight}
                    label="源节点 / Source ID"
                    value={selectedEdge.source_id}
                    mono
                  />

                  {/* 目标节点 ID / Target Node ID */}
                  <MetaField
                    icon={ArrowRight}
                    label="目标节点 / Target ID"
                    value={selectedEdge.target_id}
                    mono
                  />

                  {/* 数据来源 / Provenance */}
                  <MetaField
                    icon={Shield}
                    label="来源 / Provenance"
                    value={
                      PROVENANCE_LABELS[selectedEdge.provenance] ??
                      selectedEdge.provenance
                    }
                  />
                </div>

                {/* 置信度 / Confidence */}
                <div className="py-1.5">
                  <div className="flex items-center gap-2 mb-1.5">
                    <BarChart3
                      size={14}
                      style={{ color: 'var(--text-muted)', flexShrink: 0 }}
                    />
                    <span
                      className="text-[10px] uppercase tracking-wide"
                      style={{ color: 'var(--text-muted)' }}
                    >
                      置信度 / Confidence
                    </span>
                  </div>
                  <div className="pl-6">
                    <ConfidenceBar value={selectedEdge.confidence} />
                  </div>
                </div>

                {/* 边描述 / Edge Description */}
                {selectedEdge.description && (
                  <div className="py-1.5">
                    <div className="flex items-center gap-2 mb-1.5">
                      <FileText
                        size={14}
                        style={{ color: 'var(--text-muted)', flexShrink: 0 }}
                      />
                      <span
                        className="text-[10px] uppercase tracking-wide"
                        style={{ color: 'var(--text-muted)' }}
                      >
                        描述 / Description
                      </span>
                    </div>
                    <div className="pl-6">
                      <p
                        className="text-xs leading-relaxed"
                        style={{ color: 'var(--text-secondary)' }}
                      >
                        {selectedEdge.description}
                      </p>
                    </div>
                  </div>
                )}
              </>
            )}
          </div>
        </motion.aside>
      )}
    </AnimatePresence>
  )
}

export default DetailPanel
