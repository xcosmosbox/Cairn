// ============================================================
// SigmaCanvas — 核心图可视化组件 / Core Graph Visualization
//
// 基于 Sigma.js v3 (WebGL) 封装的知识图谱渲染画布。
// 负责将 graphology Graph 实例渲染为交互式力导向布局图，
// 并向上层暴露节点/边点击、悬停等事件回调。
//
// Wraps Sigma.js v3 (WebGL) to render a graphology Graph as an
// interactive force-directed visualization. Exposes node/edge
// click and hover callbacks to the parent layer.
// ============================================================

import { useEffect, useRef, useCallback } from 'react'
import Sigma from 'sigma'
// 自定义渲染程序 / Custom rendering programs
//   - EdgeCurveProgram：曲线边，比直线更好地区分双向 / 平行关系，视觉更柔和。
//   - NodeBorderProgram：带描边的节点，用于把幽灵占位节点渲染成空心环。
// 需在下方 new Sigma(...) 的 edgeProgramClasses / nodeProgramClasses 注册后，
// 才能在 defaultEdgeType / node type 中引用 'curved' / 'border'。
// EdgeCurveProgram (curved edges) distinguishes parallel/bidirectional links
// better than straight lines; NodeBorderProgram renders ghost placeholders as
// hollow rings. Both must be registered in edgeProgramClasses / nodeProgramClasses
// below before 'curved' / 'border' can be referenced.
import EdgeCurveProgram from '@sigma/edge-curve'
import { NodeBorderProgram } from '@sigma/node-border'
import type Graph from 'graphology'
import { ZoomIn, ZoomOut, Maximize2 } from 'lucide-react'
import type { KGNode, KGEdge, GraphTheme, EdgeKind } from '../../types'
// 演化模式 diff 着色（E-5）：从 zustand store 直接读当前选中的 changeset 颜色索引，
// nodeReducer / edgeReducer 每次调用 getState() 取最新值，无需 prop 透传。
// Evolution diff coloring (E-5): read the selected changeset's color index
// directly from the zustand store in the reducers — no prop threading needed.
import { useEvolutionStore, DIFF_COLORS } from '../../stores/evolutionStore'
// 复用共享适配层：边配色、graphology→KGNode/KGEdge 映射的唯一事实来源。
// 2D 与 3D 渲染器共用同一份，避免重复定义（DRY）。
// Reuse the shared adapter layer: single source of truth for edge coloring and
// graphology→KGNode/KGEdge mapping, shared by the 2D and 3D renderers (DRY).
import { edgeKindColorCss, attrsToKGNode, attrsToKGEdge } from './graph-adapters'

// ─── Props / 组件属性 ────────────────────────────────────────────

/**
 * SigmaCanvas 组件属性。
 *
 * Props for the SigmaCanvas component.
 */
interface SigmaCanvasProps {
  /**
   * graphology Graph 实例。外层（如 GraphEngine）负责构建并传入。
   * 为 null 或 order === 0 时显示加载/空状态。
   *
   * The graphology Graph instance. The outer layer (e.g. GraphEngine)
   * builds and passes it in. Shows a loading/empty state when null or
   * when the graph has zero nodes.
   */
  graph: Graph | null

  /**
   * 节点点击回调。参数为节点 ID 与完整的 KGNode 数据。
   *
   * Node click callback. Receives the node ID and full KGNode data.
   */
  onNodeClick?: (nodeId: string, node: KGNode) => void

  /**
   * 边点击回调。参数为边 ID 与完整的 KGEdge 数据。
   *
   * Edge click callback. Receives the edge ID and full KGEdge data.
   */
  onEdgeClick?: (edgeId: string, edge: KGEdge) => void

  /**
   * 节点悬停回调。参数为当前悬停的节点 ID；
   * 鼠标离开时传入 null。
   *
   * Node hover callback. Receives the currently hovered node ID,
   * or null when the cursor leaves a node.
   */
  onNodeHover?: (nodeId: string | null) => void

  /**
   * 可视化主题配置。用于动态调整节点/边颜色与画布背景。
   * 为 null 时使用默认暗色主题。
   *
   * Visualization theme configuration. Used to dynamically adjust
   * node/edge colors and canvas background. Falls back to a default
   * dark theme when null.
   */
  theme?: GraphTheme | null
}

// ─── 辅助函数 / Helpers ──────────────────────────────────────────
// 边配色（EDGE_KIND_COLOR / edgeKindColorCss）与 graphology→KGNode/KGEdge 映射
// （attrsToKGNode / attrsToKGEdge）已上移到 ./graph-adapters，供 2D/3D 共用（DRY）。
// Edge coloring and graphology→KGNode/KGEdge mapping now live in ./graph-adapters,
// shared by the 2D/3D renderers (DRY).

// ─── SigmaCanvas 组件 / Component ────────────────────────────────

/**
 * 核心图可视化画布组件。
 *
 * 使用 Sigma.js v3 (WebGL) 渲染 graphology 图实例。
 * 提供缩放工具栏、适应视图按钮，并通过事件回调连接上层状态。
 *
 * Core graph visualization canvas component.
 *
 * Renders a graphology Graph instance using Sigma.js v3 (WebGL).
 * Includes zoom controls, a fit-to-view button, and connects to
 * parent state via event callbacks.
 */
export function SigmaCanvas({
  graph,
  onNodeClick,
  onEdgeClick,
  onNodeHover,
  theme,
}: SigmaCanvasProps) {
  // ----- Refs -----------------------------------------------------

  /** Sigma 渲染器的 DOM 挂载容器 / DOM container for the Sigma renderer */
  const containerRef = useRef<HTMLDivElement>(null)

  /** Sigma 实例引用 / Sigma instance reference */
  const sigmaRef = useRef<Sigma | null>(null)

  /** 用于追踪当前悬停节点以去重 / Tracks current hovered node for dedup */
  const hoveredNodeRef = useRef<string | null>(null)

  // ----- 初始化 / 销毁 Sigma 实例 ────────────────────────────────
  // Initialize and tear down the Sigma instance whenever `graph` changes

  useEffect(() => {
    const container = containerRef.current
    if (!container || !graph || graph.order === 0) {
      // 清理旧实例（当 graph 变为 null 时）/ Clean up old instance when graph becomes null
      if (sigmaRef.current) {
        sigmaRef.current.kill()
        sigmaRef.current = null
      }
      return
    }

    // 销毁上一个 Sigma 实例，避免内存泄漏与重复渲染
    // Kill the previous Sigma instance to avoid memory leaks and duplicate rendering
    if (sigmaRef.current) {
      sigmaRef.current.kill()
      sigmaRef.current = null
    }

    // 解析主题色 / Resolve theme colors with defaults
    const bgColor = theme?.background ?? '#12141c'
    const labelColorVal = theme?.nodeLabelColor ?? '#a0a8b8'

    /**
     * 创建 Sigma v3 渲染器实例。
     *
     * Create a Sigma v3 renderer instance.
     */
    const sigma = new Sigma(graph, container, {
      // 标签渲染 / Label rendering
      renderLabels: true,
      labelFont: 'system-ui, -apple-system, sans-serif',
      labelSize: 12,
      labelColor: { color: labelColorVal },

      // 默认边外观 / Default edge appearance
      // 使用 'curved' 曲线边（由下方 edgeProgramClasses 注册的 EdgeCurveProgram
      // 提供）。曲线比直线更能区分双向 / 平行关系，视觉更柔和美观。
      // Use 'curved' edges (provided by EdgeCurveProgram, registered in
      // edgeProgramClasses below). Curves distinguish bidirectional/parallel
      // links better than straight lines and look softer.
      defaultEdgeColor: 'rgba(80, 100, 140, 0.3)',
      defaultEdgeType: 'curved',
      defaultNodeColor: '#555',

      // 注册自定义渲染程序 / Register custom rendering programs
      //   - 'curved'：曲线边程序，被 defaultEdgeType 引用。
      //   - 'border'：描边节点程序，被 nodeReducer 用于渲染幽灵占位节点为空心环。
      // 未注册就引用对应 type 会让 Sigma 抛
      // "could not find a suitable program for edge/node type"。
      // Registering these lets defaultEdgeType='curved' and node type='border'
      // resolve to real programs; without registration Sigma throws
      // "could not find a suitable program".
      edgeProgramClasses: {
        curved: EdgeCurveProgram,
      },
      nodeProgramClasses: {
        border: NodeBorderProgram,
      },

      // 相机限制 / Camera constraints
      minCameraRatio: 0.02,
      maxCameraRatio: 10,
      stagePadding: 40,

      // 禁用内置悬停效果，由我们手动控制 / Disable built-in hover, we control it manually
      defaultDrawNodeHover: () => {},

      /**
       * 节点归约器：根据 GraphNodeAttributes 动态设置每个节点的
       * 大小和颜色。size 和 color 由 GraphEngine 在构建图时写入。
       *
       * Node reducer: dynamically sets each node's size and color
       * from GraphNodeAttributes. These fields are written by
       * GraphEngine during graph construction.
       */
      nodeReducer: (node, attrs) => {
        const data = attrs as any
        // ── 演化 diff 着色叠加（E-5）/ evolution diff color overlay ──
        // 优先取 diff 颜色，其次取 GraphEngine 原色；幽灵节点不着色。
        // getState() 每次调用取最新值（闭包只捕获函数引用，不缓存值）。
        // Read the latest diff colors on every reducer call (closure captures
        // the function ref, not a value snapshot).
        const ev = useEvolutionStore.getState()
        let overlayColor: string | undefined
        if (ev.enabled && ev.overlay && !data.isGhost) {
          overlayColor = ev.nodeColors[data.id]
        }
        // ── 幽灵节点：视觉上区分于真实节点 ──────────────────
        // Ghost nodes: visually distinct from real nodes.
        if (data.isGhost) {
          return {
            ...attrs,
            size: data.size || 2,
            color: 'rgba(100, 100, 100, 0.35)',
            label: data.label || '?',
            type: 'border',
          }
        }
        return {
          ...attrs,
          size: data.size ?? 4,
          color: overlayColor ?? data.color ?? '#888',
          label: data.label ?? '',
        }
      },

      /**
       * 边归约器：根据 GraphEdgeAttributes 动态设置每条边的颜色与粗细。
       * 颜色按关系类型（kind）分色（见 EDGE_KIND_COLOR），透明度由 weight
       * 控制（高权重 = 更不透明）。这样 provides/composes 结构边与语义边一眼可辨。
       *
       * Edge reducer: sets each edge's color and thickness from
       * GraphEdgeAttributes. Color is chosen by relationship kind
       * (see EDGE_KIND_COLOR); opacity is driven by weight (higher = more opaque),
       * so structural (provides/composes) and semantic edges are visually distinct.
       */
      edgeReducer: (edge, attrs) => {
        const data = attrs as any
        const kind = data.kind as EdgeKind
        // 演化 diff：新增边用绿色高亮 / evolution: highlight added edges in green
        const ev = useEvolutionStore.getState()
        if (ev.enabled && ev.overlay) {
          const srcId = data.source_id ?? ''
          const tgtId = data.target_id ?? ''
          if (ev.addedEdgeKeys[`${srcId} ${tgtId} ${kind}`]) {
            return {
              ...attrs,
              color: DIFF_COLORS.added,
              size: (data.size ?? 1) * 1.6,
            }
          }
        }
        // 颜色/透明度由共享适配层统一计算（DRY，与 3D 一致）。
        // Color/opacity computed by the shared adapter (DRY, consistent with 3D).
        return {
          ...attrs,
          color: edgeKindColorCss(kind, data.weight),
          size: data.size ?? 1,
        }
      },
    })

    sigmaRef.current = sigma

    // 动画适配视图，让所有节点可见 / Animated fit to show all nodes
    sigma.getCamera().animatedReset({ duration: 500 })

    // 清理函数：组件卸载或 graph 变化时销毁实例
    // Cleanup: kill instance on unmount or when graph changes
    return () => {
      sigma.kill()
      sigmaRef.current = null
    }
  }, [graph, theme])

  // ----- 事件绑定 / Event Bindings ───────────────────────────────
  // 绑定 Sigma 的交互事件，将底层事件映射为上层回调。
  // Bind Sigma interaction events, mapping low-level events to
  // parent-layer callbacks.

  useEffect(() => {
    const sigma = sigmaRef.current
    if (!sigma) return

    /**
     * 节点点击事件 → onNodeClick 回调。
     * 从 graphology attributes 重建 KGNode 对象后传递。
     *
     * Node click event → onNodeClick callback. Reconstructs a
     * KGNode object from graphology attributes before forwarding.
     */
    const handleClickNode = (event: any) => {
      const nodeId: string = event.node
      const attrs = sigma.getGraph().getNodeAttributes(nodeId) as any
      onNodeClick?.(nodeId, attrsToKGNode(nodeId, attrs))
    }

    /**
     * 边点击事件 → onEdgeClick 回调。
     * 通过 source/target API 获取两端节点 ID。
     *
     * Edge click event → onEdgeClick callback. Uses the source/target
     * API to obtain both endpoint node IDs.
     */
    const handleClickEdge = (event: any) => {
      const edgeId: string = event.edge
      const attrs = sigma.getGraph().getEdgeAttributes(edgeId) as any
      onEdgeClick?.(edgeId, attrsToKGEdge(edgeId, sigma.getGraph(), attrs))
    }

    /**
     * 鼠标进入节点 → onNodeHover(nodeId)。
     * 带去重逻辑，避免重复触发。
     *
     * Mouse enters a node → onNodeHover(nodeId).
     * Deduplicated to avoid redundant fire.
     */
    const handleEnterNode = (event: any) => {
      const nodeId: string = event.node
      if (hoveredNodeRef.current !== nodeId) {
        hoveredNodeRef.current = nodeId
        onNodeHover?.(nodeId)
      }
    }

    /**
     * 鼠标离开节点 → onNodeHover(null)。
     * 重置悬停追踪器。
     *
     * Mouse leaves a node → onNodeHover(null).
     * Resets the hover tracker.
     */
    const handleLeaveNode = () => {
      hoveredNodeRef.current = null
      onNodeHover?.(null)
    }

    // ── 绑定 Sigma v3 事件 / Bind Sigma v3 events ──

    sigma.on('clickNode', handleClickNode)
    sigma.on('clickEdge', handleClickEdge)
    sigma.on('enterNode', handleEnterNode)
    sigma.on('leaveNode', handleLeaveNode)

    // 清理：移除所有事件监听
    // Cleanup: remove all event listeners
    return () => {
      sigma.removeListener('clickNode', handleClickNode)
      sigma.removeListener('clickEdge', handleClickEdge)
      sigma.removeListener('enterNode', handleEnterNode)
      sigma.removeListener('leaveNode', handleLeaveNode)
    }
  }, [graph, onNodeClick, onEdgeClick, onNodeHover])

  // ----- 工具栏回调 / Toolbar Callbacks ──────────────────────────
  // 需要 useCallback 以避免每次渲染都创建新闭包（button 的 onClick
  // 可能导致多余的 re-render）。
  //
  // useCallback to avoid creating new closures on every render
  // (button onClick can trigger unnecessary re-renders).

  /**
   * 缩放至适应视图 / Zoom to fit the entire graph in view.
   */
  const handleFitToGraph = useCallback(() => {
    sigmaRef.current?.getCamera().animatedReset({ duration: 300 })
  }, [])

  /**
   * 放大 / Zoom in.
   */
  const handleZoomIn = useCallback(() => {
    sigmaRef.current?.getCamera().animatedZoom({ duration: 200, factor: 1.3 })
  }, [])

  /**
   * 缩小 / Zoom out.
   */
  const handleZoomOut = useCallback(() => {
    sigmaRef.current?.getCamera().animatedZoom({ duration: 200, factor: 0.7 })
  }, [])

  // ----- 渲染 / Render ───────────────────────────────────────────

  return (
    <div className="relative w-full h-full" style={{ background: theme?.background ?? 'var(--bg-root)' }}>
      {/* ── Sigma 画布容器 / Sigma Canvas Container ── */}
      <div ref={containerRef} className="absolute inset-0" />

      {/* ── 加载/空状态覆盖层 / Loading / Empty State Overlay ── */}
      {(!graph || graph.order === 0) && (
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

      {/* ── 右下角工具栏 / Bottom-right Toolbar ── */}
      <div className="absolute bottom-4 right-4 flex flex-col gap-1 z-10">
        {/**
         * 适配视图按钮 — 将相机重置以显示所有节点。
         * Fit-to-graph button — resets the camera to show all nodes.
         */}
        <button
          onClick={handleFitToGraph}
          disabled={!graph || graph.order === 0}
          className="p-2 rounded-lg transition-colors disabled:opacity-30 disabled:cursor-not-allowed"
          style={{
            background: 'var(--bg-card)',
            border: '1px solid var(--border-default)',
            color: 'var(--text-secondary)',
          }}
          onMouseEnter={(e) => {
            if (graph && graph.order > 0) {
              e.currentTarget.style.color = 'var(--text-primary)'
            }
          }}
          onMouseLeave={(e) => {
            e.currentTarget.style.color = 'var(--text-secondary)'
          }}
          title="适配视图 / Fit to Graph"
        >
          <Maximize2 className="w-4 h-4" />
        </button>

        {/**
         * 放大按钮 / Zoom in button.
         */}
        <button
          onClick={handleZoomIn}
          disabled={!graph || graph.order === 0}
          className="p-2 rounded-lg transition-colors disabled:opacity-30 disabled:cursor-not-allowed"
          style={{
            background: 'var(--bg-card)',
            border: '1px solid var(--border-default)',
            color: 'var(--text-secondary)',
          }}
          onMouseEnter={(e) => {
            if (graph && graph.order > 0) {
              e.currentTarget.style.color = 'var(--text-primary)'
            }
          }}
          onMouseLeave={(e) => {
            e.currentTarget.style.color = 'var(--text-secondary)'
          }}
          title="放大 / Zoom In"
        >
          <ZoomIn className="w-4 h-4" />
        </button>

        {/**
         * 缩小按钮 / Zoom out button.
         */}
        <button
          onClick={handleZoomOut}
          disabled={!graph || graph.order === 0}
          className="p-2 rounded-lg transition-colors disabled:opacity-30 disabled:cursor-not-allowed"
          style={{
            background: 'var(--bg-card)',
            border: '1px solid var(--border-default)',
            color: 'var(--text-secondary)',
          }}
          onMouseEnter={(e) => {
            if (graph && graph.order > 0) {
              e.currentTarget.style.color = 'var(--text-primary)'
            }
          }}
          onMouseLeave={(e) => {
            e.currentTarget.style.color = 'var(--text-secondary)'
          }}
          title="缩小 / Zoom Out"
        >
          <ZoomOut className="w-4 h-4" />
        </button>
      </div>
    </div>
  )
}

export default SigmaCanvas
