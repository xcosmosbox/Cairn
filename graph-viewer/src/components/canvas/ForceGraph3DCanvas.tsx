// ============================================================
// ForceGraph3DCanvas — 3D 图可视化组件 / 3D Graph Visualization
//
// 基于 react-force-graph-3d (three.js / WebGL) 的知识图谱 3D 渲染画布。
// 与 SigmaCanvas 遵循【完全相同的 props 契约】（graph / onNodeClick /
// onEdgeClick / onNodeHover / theme），因此可被 GraphView 无缝互换。
//
// 3D 力导向把簇/边在第三个维度上展开，缓解 2D 下「簇和边重叠严重」的痛点。
// 节点/边的颜色、尺寸直接复用 GraphEngine 已写入 graphology 属性的值
//（NODE_TYPE_STYLE 分层着色 + EDGE_KIND_COLOR 边配色），不重算、不复制（DRY）。
//
// A 3D knowledge-graph canvas built on react-force-graph-3d (three.js/WebGL).
// It follows the EXACT SAME props contract as SigmaCanvas, so GraphView can
// swap the two freely. The 3D force layout spreads clusters/edges along a third
// axis, easing the "clusters and edges overlap badly" pain of the 2D view.
// Node/edge colors and sizes reuse the values GraphEngine already wrote onto the
// graphology attributes (NODE_TYPE_STYLE layering + EDGE_KIND_COLOR), with no
// recomputation or duplication (DRY).
// ============================================================

import { useEffect, useMemo, useRef, useState, useCallback } from 'react'
import ForceGraph3D from 'react-force-graph-3d'
import type { ForceGraphMethods } from 'react-force-graph-3d'
import type Graph from 'graphology'
import type { KGNode, KGEdge, GraphTheme, EdgeKind } from '../../types'
// 复用共享适配层：graphology→ForceGraph、graphology→KGNode/KGEdge、边配色。
// 与 SigmaCanvas 同源，保证 2D/3D 的颜色与回调数据一致（DRY）。
// Reuse the shared adapter layer: graphology→ForceGraph, graphology→KGNode/KGEdge,
// edge coloring — same source as SigmaCanvas, so 2D/3D colors and callback data
// stay consistent (DRY).
import {
  graphologyToForceGraph,
  edgeKindColorCss,
  attrsToKGNode,
  attrsToKGEdge,
  type ForceGraphNode,
  type ForceGraphLink,
} from './graph-adapters'

// ─── Props / 组件属性 ────────────────────────────────────────────
// 与 SigmaCanvasProps 完全一致的契约 —— 两个渲染器可互换。
// Identical contract to SigmaCanvasProps — the two renderers are interchangeable.

interface ForceGraph3DCanvasProps {
  /** graphology Graph 实例；为 null 或空图时显示空状态。/ graphology Graph; null/empty → empty state. */
  graph: Graph | null
  /** 节点点击回调（nodeId + 完整 KGNode）。/ node click (id + full KGNode). */
  onNodeClick?: (nodeId: string, node: KGNode) => void
  /** 边点击回调（edgeId + 完整 KGEdge）。/ edge click (id + full KGEdge). */
  onEdgeClick?: (edgeId: string, edge: KGEdge) => void
  /** 节点悬停回调；离开时传 null。/ node hover; null on leave. */
  onNodeHover?: (nodeId: string | null) => void
  /** 可视化主题；用于背景色等。/ theme; used for background etc. */
  theme?: GraphTheme | null
}

// ─── ForceGraph3DCanvas 组件 / Component ─────────────────────────

/**
 * 3D 知识图谱画布组件。
 *
 * 内部把传入的 graphology Graph 用 graphologyToForceGraph 适配为
 * react-force-graph 所需的 {nodes, links} 结构，然后交给 <ForceGraph3D>
 * 渲染。所有交互事件都被翻译回与 SigmaCanvas 相同的回调签名。
 *
 * 3D knowledge-graph canvas. Internally adapts the incoming graphology Graph via
 * graphologyToForceGraph into the {nodes, links} shape react-force-graph needs,
 * then hands it to <ForceGraph3D>. All interactions are translated back into the
 * same callback signatures SigmaCanvas exposes.
 */
export function ForceGraph3DCanvas({
  graph,
  onNodeClick,
  onEdgeClick,
  onNodeHover,
  theme,
}: ForceGraph3DCanvasProps) {
  // ----- Refs -----------------------------------------------------

  /** ForceGraph3D 的外层尺寸容器 / sizing container for ForceGraph3D */
  const containerRef = useRef<HTMLDivElement>(null)
  /** ForceGraph3D 实例方法句柄（用于 zoomToFit / 卸载清理）/ instance methods handle */
  const fgRef = useRef<ForceGraphMethods<ForceGraphNode, ForceGraphLink> | undefined>(undefined)
  /** 当前悬停节点，用于去重触发 onNodeHover / dedupes hover firing */
  const hoveredNodeRef = useRef<string | null>(null)
  /** 容器测量尺寸（作为 ForceGraph3D 的 width/height props）/ measured container size */
  const [size, setSize] = useState<{ width: number; height: number }>({ width: 0, height: 0 })

  // ----- graphology → ForceGraph 适配（memo 化）───────────────────
  // 仅在 graph 引用变化时重算。App 为每个 DB 用独立 graphology 实例，切库时
  // 引用会变，因此这里能正确随「多 DB 切换」刷新数据。
  // Recomputed only when the graph reference changes. App uses a fresh
  // graphology instance per DB, so this refreshes correctly on multi-DB switch.
  const graphData = useMemo(() => {
    if (!graph || graph.order === 0) return { nodes: [], links: [] }
    return graphologyToForceGraph(graph)
  }, [graph])

  const isEmpty = !graph || graph.order === 0

  // ----- 尺寸自适应 / Responsive sizing ──────────────────────────
  // react-force-graph 需显式 width/height。用 ResizeObserver 跟随容器变化，
  // 把尺寸写入 state 作为 props 传给 ForceGraph3D。
  // react-force-graph needs explicit width/height. Track the container with a
  // ResizeObserver and feed the size into ForceGraph3D via props.
  useEffect(() => {
    const el = containerRef.current
    if (!el) return

    const applySize = () => {
      const w = el.clientWidth
      const h = el.clientHeight
      setSize((prev) => (prev.width === w && prev.height === h ? prev : { width: w, height: h }))
    }
    applySize()

    const ro = new ResizeObserver(applySize)
    ro.observe(el)
    return () => ro.disconnect()
  }, [])

  // ----- 布局稳定（引擎停止）后适配视图 / Fit view once layout settles ──
  // 【性能优化】不再用固定 400ms 的 setTimeout 猜测暖场时间，而是由
  // ForceGraph3D 的 onEngineStop 精确驱动：力导向引擎收敛停止（见下方
  // cooldownTicks）后，才把整图缩放入镜。这样既避免与力导向抢渲染帧，
  // 也保证 fit 时布局已稳定、取景准确。
  // [Perf] Instead of guessing warmup time with a fixed 400ms setTimeout, fit
  // the view precisely when the force engine converges and stops (onEngineStop,
  // see cooldownTicks below). This avoids competing with the layout for frames
  // and ensures the layout is settled when we fit.
  const handleEngineStop = useCallback(() => {
    fgRef.current?.zoomToFit(600, 40)
  }, [])

  // ----- 卸载清理 three.js 资源 / Tear down three.js on unmount ───
  // react-force-graph 内部会创建 WebGLRenderer / Scene。组件卸载（如切回 2D）
  // 时暂停动画循环并释放渲染器，避免 GPU 上下文与 RAF 泄漏。
  // react-force-graph creates a WebGLRenderer/Scene internally. On unmount
  // (e.g. switching back to 2D) pause the animation loop and dispose the
  // renderer to avoid leaking the GPU context and RAF loop.
  useEffect(() => {
    return () => {
      const fg = fgRef.current
      if (!fg) return
      try {
        fg.pauseAnimation()
        // 释放 WebGL 上下文 / release the WebGL context
        const renderer = fg.renderer() as any
        renderer?.dispose?.()
        renderer?.forceContextLoss?.()
      } catch {
        // 渲染器可能已被内部清理，忽略。/ renderer may already be gone; ignore.
      }
    }
  }, [])

  // ----- 访问器 / Accessors ──────────────────────────────────────
  // 均从 ForceGraph 节点/边上「GraphEngine 已写入的属性」直接读取，不重算（DRY）。
  // All read straight from attributes GraphEngine already wrote; no recompute (DRY).

  /**
   * 节点颜色：复用 graph-engine 计算好的 color（NODE_TYPE_STYLE 分层着色 +
   * 连接度亮度），与 2D 完全一致。幽灵节点已带暗淡灰色。
   *
   * Node color: reuse the color computed by graph-engine (NODE_TYPE_STYLE
   * layering + connectivity brightness), identical to 2D. Ghost nodes already
   * carry a dimmed gray.
   */
  const nodeColor = useCallback((n: ForceGraphNode) => (n.color as string) ?? '#888', [])

  /**
   * 节点体积：react-force-graph 的 nodeVal 是「体积」，视觉半径 ∝ 立方根。
   * 复用 graph-engine 的 size（分层半径），平方一下拉开层级差异。
   *
   * Node volume: react-force-graph's nodeVal is a volume; visual radius ∝ cube
   * root. Reuse graph-engine's size (layered radius), squared to amplify the
   * per-layer size gap.
   */
  const nodeVal = useCallback((n: ForceGraphNode) => {
    const s = (n.size as number) ?? 4
    return s * s * 0.15
  }, [])

  /** 节点悬停提示：显示完整名称（graph-engine 写入的 fullLabel）。/ tooltip: full name. */
  const nodeLabel = useCallback((n: ForceGraphNode) => (n.fullLabel as string) ?? (n.label as string) ?? String(n.id), [])

  /**
   * 边颜色：按 kind + weight，复用共享 edgeKindColorCss —— 与 2D 的 edgeReducer
   * 同一函数、同一配色表、同一透明度公式（DRY）。
   *
   * Edge color: by kind + weight via the shared edgeKindColorCss — the very same
   * function/table/opacity formula as the 2D edgeReducer (DRY).
   */
  const linkColor = useCallback(
    (l: ForceGraphLink) => edgeKindColorCss(l.kind as EdgeKind | undefined, l.weight as number | undefined),
    [],
  )

  /** 边宽度：复用 graph-engine 的 size（0.5–2.0）。/ reuse graph-engine's size. */
  const linkWidth = useCallback((l: ForceGraphLink) => (l.size as number) ?? 1, [])

  /** 边悬停提示：显示关系类型。/ tooltip: relationship kind. */
  const linkLabel = useCallback((l: ForceGraphLink) => (l.kind as string) ?? '', [])

  // ----- 事件处理 / Event Handlers ───────────────────────────────

  /**
   * 节点点击 → onNodeClick。用共享 attrsToKGNode 把节点属性映射回 KGNode
   *（与 2D 相同的重建逻辑），驱动同一个 DetailPanel。
   *
   * Node click → onNodeClick. Reuse attrsToKGNode to map attributes back to a
   * KGNode (same reconstruction as 2D), driving the same DetailPanel.
   */
  const handleNodeClick = useCallback(
    (node: ForceGraphNode) => {
      const id = String(node.id)
      onNodeClick?.(id, attrsToKGNode(id, node))
    },
    [onNodeClick],
  )

  // ----- 节点点击说明 / Node-click note ─────────────────────────
  // 【问题2 真实根因】three-render-objects 把 WebGL canvas 设为
  // pointerEvents:'none'，点击由库自身在外层容器上做 raycasting 处理；且它
  // 的 pointerup 里硬编码 clickAfterDrag(false)——当 enableNodeDrag 开启时，
  // 按下节点会激活 DragControls 并让力导向升温、节点在光标下微移，本次点击被
  // 误判为拖拽而吞掉 onNodeClick（hover 走独立通道，故"hover 有、click 无"）。
  //
  // 修复：关闭 enableNodeDrag（见下方 JSX），让官方 onNodeClick 正常触发。
  // 对知识图谱浏览而言，拖动单个节点并非必需（YAGNI）——用户需要的是"点击看
  // 详情"，相机的旋转/缩放/平移仍完整保留。这样无需 hack 事件层，直接复用
  // react-force-graph 的 onNodeClick，与 2D 行为一致（DRY）。
  //
  // [Problem #2 real root cause] three-render-objects sets the WebGL canvas to
  // pointerEvents:'none' and handles clicks via raycasting on its own container;
  // its pointerup hard-codes clickAfterDrag(false). With enableNodeDrag on,
  // pressing a node engages DragControls and reheats the sim so the node
  // micro-shifts, making the click read as a drag and swallowing onNodeClick
  // (hover uses a separate channel — hence "hover works, click doesn't").
  //
  // Fix: disable enableNodeDrag (see JSX below) so the official onNodeClick
  // fires normally. Dragging individual nodes isn't needed for graph browsing
  // (YAGNI) — users want "click for details"; camera orbit/zoom/pan stay intact.
  // No event-layer hacking; we reuse react-force-graph's onNodeClick, matching 2D (DRY).

  /**
   * 边点击 → onEdgeClick。link.source/target 运行后是节点对象，用一个轻量
   * shim 满足 attrsToKGEdge 的 source(id)/target(id) 契约，复用同一映射逻辑。
   *
   * Edge click → onEdgeClick. After simulation link.source/target are node
   * objects; a tiny shim satisfies attrsToKGEdge's source(id)/target(id)
   * contract, reusing the same mapping logic.
   */
  const handleLinkClick = useCallback(
    (link: ForceGraphLink) => {
      const edgeId = String(link.id)
      // source/target 可能是字符串（未跑力导向前）或节点对象（跑后）。
      // source/target may be a string (pre-sim) or a node object (post-sim).
      const srcId = typeof link.source === 'object' ? String((link.source as any).id) : String(link.source)
      const tgtId = typeof link.target === 'object' ? String((link.target as any).id) : String(link.target)
      const resolver = { source: () => srcId, target: () => tgtId }
      onEdgeClick?.(edgeId, attrsToKGEdge(edgeId, resolver, link))
    },
    [onEdgeClick],
  )

  /**
   * 节点悬停 → onNodeHover(nodeId | null)。带去重，语义与 SigmaCanvas 的
   * enterNode/leaveNode 一致。
   *
   * Node hover → onNodeHover(nodeId | null), deduplicated; matches SigmaCanvas's
   * enterNode/leaveNode semantics.
   */
  const handleNodeHover = useCallback(
    (node: ForceGraphNode | null) => {
      const id = node ? String(node.id) : null
      if (hoveredNodeRef.current !== id) {
        hoveredNodeRef.current = id
        onNodeHover?.(id)
      }
    },
    [onNodeHover],
  )

  // ----- 渲染 / Render ───────────────────────────────────────────

  const bgColor = theme?.background ?? '#12141c'

  return (
    <div
      ref={containerRef}
      className="relative w-full h-full overflow-hidden"
      style={{ background: bgColor }}
    >
      {/* ── 3D 画布 / 3D Canvas ── */}
      {/* 【bug 修复：纯 3D 场景点击不到节点】必须等容器尺寸测量出真实值后再挂载
          ForceGraph3D。若在 size={0,0} 时挂载，ForceGraph3D 会以 undefined 宽高
          回退到默认（近全窗口）尺寸初始化其内部 WebGL canvas 与 raycaster；随后
          ResizeObserver setSize 触发的 props 更新，并不会让内部 raycaster 的坐标
          系与实际显示区域重新对齐 → 点击射线打不中节点 → onNodeClick 不触发 →
          详情页不弹出。而"2D 先点过再切 3D"时容器早已测量、size 非零，故正常——
          这正是用户复现出的差异。用 sizeReady 门控确保首次挂载即为正确尺寸，并
          传显式数值宽高（非 undefined）。
          [Bug fix: clicks miss nodes in a fresh 3D scene] Mount ForceGraph3D only
          after the container size is measured. Mounting at size={0,0} makes
          ForceGraph3D fall back to a default (near-full-window) size for its
          internal WebGL canvas and raycaster; a later size update via props does
          NOT realign the raycaster's coordinate space with the actual viewport →
          click rays miss nodes → onNodeClick never fires → the detail panel never
          opens. When 2D was clicked first, the container was already measured
          (size non-zero), so it worked — exactly the difference the user found.
          Gate on sizeReady so the first mount uses the correct size, passed as
          explicit numbers (not undefined). */}
      {!isEmpty && size.width > 0 && size.height > 0 && (
        <ForceGraph3D
          ref={fgRef as any}
          graphData={graphData}
          width={size.width}
          height={size.height}
          backgroundColor={bgColor}
          // 节点外观（复用 graph-engine 属性）/ node styling (reuses graph-engine attrs)
          nodeColor={nodeColor as any}
          nodeVal={nodeVal as any}
          nodeLabel={nodeLabel as any}
          nodeOpacity={0.9}
          nodeResolution={12}
          // 边外观（复用共享配色）/ link styling (shared coloring)
          linkColor={linkColor as any}
          linkWidth={linkWidth as any}
          linkLabel={linkLabel as any}
          linkOpacity={0.55}
          // 曲线边：呼应 2D 的 curved 边，弱化双向/平行重叠 / curved links, echoing 2D
          linkCurvature={0.15}
          // 交互 / interaction
          onNodeClick={handleNodeClick as any}
          onLinkClick={handleLinkClick as any}
          onNodeHover={handleNodeHover as any}
          // 关闭节点拖拽：它会让官方 onNodeClick 被误判为拖拽而吞掉（见上方根因说明）。
          // 相机的旋转/缩放/平移不受影响，仍可自由浏览 3D 场景。
          // Disable node drag: it makes the official onNodeClick get swallowed as a
          // drag (see root-cause note above). Camera orbit/zoom/pan are unaffected.
          enableNodeDrag={false}
          showNavInfo={false}
          // ── 性能：布局稳定后停止力导向引擎 / Perf: stop the force engine once settled ──
          // 默认 cooldownTicks=Infinity + cooldownTime=15000ms，意味着物理引擎会
          // 持续每帧重算 158 节点的力导向长达 15 秒，这是 3D 卡顿的主因。改为跑固定
          // 100 tick 即停：对本图规模（百级节点）足以收敛出稳定布局，引擎停止后不再
          // 空转重算，CPU/GPU 负载大幅下降。相机的旋转/缩放/平移仍可正常交互
          //（那是渲染循环的职责，与物理引擎是否运行无关）。onEngineStop 在收敛后
          // 触发一次 zoomToFit 取景。
          // Default cooldownTicks=Infinity + cooldownTime=15000ms keeps the physics
          // engine recomputing forces for all 158 nodes every frame for 15s — the
          // main cause of 3D lag. Cap it at 100 ticks: for a graph this size that's
          // enough to converge to a stable layout, and once stopped the engine no
          // longer spins recomputing forces, cutting CPU/GPU load sharply. Camera
          // orbit/zoom/pan still work (that's the render loop, independent of the
          // physics engine). onEngineStop fits the view once after convergence.
          warmupTicks={20}
          cooldownTicks={100}
          onEngineStop={handleEngineStop}
        />
      )}

      {/* ── 空状态覆盖层 / Empty State Overlay ── */}
      {isEmpty && (
        <div className="absolute inset-0 flex flex-col items-center justify-center gap-3 pointer-events-none">
          <span className="text-4xl opacity-30">🔬</span>
          {!graph ? (
            <>
              <p className="text-sm" style={{ color: 'var(--text-muted)' }}>
                正在加载图谱数据...
              </p>
              <p className="text-xs" style={{ color: 'var(--text-muted)', opacity: 0.6 }}>
                Loading graph data...
              </p>
            </>
          ) : (
            <>
              <p className="text-sm" style={{ color: 'var(--text-muted)' }}>
                图谱为空，请先加载数据
              </p>
              <p className="text-xs" style={{ color: 'var(--text-muted)', opacity: 0.6 }}>
                Graph is empty. Load data first.
              </p>
            </>
          )}
        </div>
      )}
    </div>
  )
}

export default ForceGraph3DCanvas
