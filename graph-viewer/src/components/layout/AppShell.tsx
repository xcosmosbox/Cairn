// ============================================================
// AppShell — 三栏布局外壳 / Three-Panel Layout Shell
//
// 布局结构（flex row）：
//   左侧：DBSidebar (260px fixed)
//   中间：SigmaCanvas (flex-1)
//   右侧：DetailPanel (380px, conditional)
//
// 负责协调：
//   - DB 加载事件 → store 更新
//   - 节点/边点击 → 详情面板显示
//   - 空状态（无 DB 加载时）
//
// Layout (flex row):
//   Left:  DBSidebar (260px fixed)
//   Center: SigmaCanvas (flex-1)
//   Right: DetailPanel (380px, conditional)
//
// Coordinates:
//   - DB load events → store updates
//   - Node/edge clicks → detail panel display
//   - Empty state when no DB is loaded
// ============================================================

import { useCallback } from 'react'
import { DBSidebar } from '../sidebar/DBSidebar'
import { DetailPanel } from '../detail/DetailPanel'
import { GraphView } from '../canvas/GraphView'
import { Timeline } from '../evolution/Timeline'
import { MetricsChart } from '../evolution/MetricsChart'
import { ChangesetPanel } from '../evolution/ChangesetPanel'
import { useDBStore } from '../../stores/dbStore'
import { useUIStore } from '../../stores/uiStore'
import { useEvolutionStore } from '../../stores/evolutionStore'
import type { KGNode, KGEdge, GraphTheme } from '../../types'
import type Graph from 'graphology'

// ─── Props / 组件属性 ──────────────────────────────────────────────

/**
 * AppShell 组件属性。
 *
 * Props for the AppShell component.
 */
export interface AppShellProps {
  /**
   * graphology Graph 实例。由上层（如 GraphEngine）构建并传入。
   * 为 null 时 SigmaCanvas 将显示加载/空状态覆盖层。
   *
   * The graphology Graph instance. Built and passed in by the parent
   * layer (e.g. GraphEngine). When null, SigmaCanvas shows a
   * loading / empty-state overlay.
   */
  graph: Graph | null

  /**
   * 节点点击回调。SigmaCanvas 触发时，AppShell 会先更新 UIStore
   *（选中节点、打开详情面板），再调用此回调通知上层。
   *
   * Node click callback. When SigmaCanvas fires, AppShell first
   * updates the UIStore (select node, open detail panel), then
   * invokes this callback to notify the parent.
   */
  onNodeClick?: (nodeId: string, node: KGNode) => void

  /**
   * 边点击回调。SigmaCanvas 触发时，AppShell 会先更新 UIStore
   *（选中边、打开详情面板），再调用此回调通知上层。
   *
   * Edge click callback. When SigmaCanvas fires, AppShell first
   * updates the UIStore (select edge, open detail panel), then
   * invokes this callback to notify the parent.
   */
  onEdgeClick?: (edgeId: string, edge: KGEdge) => void

  /**
   * 节点悬停回调。直接透传给 SigmaCanvas，同时更新 UIStore 的
   * hoveredNodeId 以支持外部高亮逻辑。
   *
   * Node hover callback. Passed through to SigmaCanvas; also
   * updates the UIStore's hoveredNodeId for external highlight logic.
   */
  onNodeHover?: (nodeId: string | null) => void

  /**
   * 可视化主题配置。透传给 SigmaCanvas，控制节点/边颜色与画布背景。
   *
   * Visualization theme configuration. Passed through to SigmaCanvas
   * to control node/edge colors and canvas background.
   */
  theme?: GraphTheme | null

  /**
   * 数据库加载回调。当用户在侧边栏选择 .db 文件时触发。
   * 由上层（App）负责实际的 SQLite 解析与图构建。
   *
   * Database load callback. Fired when the user selects a .db file
   * in the sidebar. The parent (App) handles actual SQLite parsing
   * and graph construction.
   */
  onLoadDatabase?: (file: File) => void
}

// ─── 组件 / Component ──────────────────────────────────────────────

/**
 * 应用的三栏布局外壳组件。
 *
 * 负责将 DBSidebar（左）、SigmaCanvas（中）、DetailPanel（右）
 * 组合为一个 flex-row 布局。作为图可视化交互的协调中心：
 *
 * - 将 DB 侧边栏的加载事件连接到 UIStore
 * - 拦截 SigmaCanvas 的节点/边点击事件，先写入 UIStore
 *   （以驱动详情面板），再转发给外部回调
 * - 拦截节点悬停事件，同步更新 UIStore 的 hoveredNodeId
 * - 仅在 UIStore.detailPanelOpen 为 true 时渲染 DetailPanel
 *
 * Three-panel layout shell for the application.
 *
 * Composes DBSidebar (left), SigmaCanvas (center), and DetailPanel
 * (right) into a flex-row layout. Acts as the coordination hub
 * for graph visualization interactions:
 *
 * - Connects DB sidebar load events to the UIStore
 * - Intercepts SigmaCanvas node/edge clicks: writes to UIStore
 *   first (to drive the detail panel), then forwards to external callbacks
 * - Intercepts node hover events and syncs UIStore.hoveredNodeId
 * - Only renders DetailPanel when UIStore.detailPanelOpen is true
 */
export function AppShell({
  graph,
  onNodeClick,
  onEdgeClick,
  onNodeHover,
  theme,
  onLoadDatabase,
}: AppShellProps) {
  // ── Store 选择器 / Store Selectors ────────────────────────────

  /** 获取 UIStore 的 selectNode 操作 / get the selectNode action from UIStore */
  const selectNode = useUIStore((s) => s.selectNode)

  /** 获取 UIStore 的 selectEdge 操作 / get the selectEdge action from UIStore */
  const selectEdge = useUIStore((s) => s.selectEdge)

  /** 获取 UIStore 的 setHoveredNode 操作 / get the setHoveredNode action from UIStore */
  const setHoveredNode = useUIStore((s) => s.setHoveredNode)

  /** 详情面板是否打开 / whether the detail panel is open */
  const detailPanelOpen = useUIStore((s) => s.detailPanelOpen)

  /** 演化模式是否开启 / whether evolution mode is enabled */
  const evolutionEnabled = useEvolutionStore((s) => s.enabled)
  const evolutionStatus = useEvolutionStore((s) => s.status)

  // ── 事件处理器 / Event Handlers ──────────────────────────────
  // 这些处理器将 SigmaCanvas 的底层事件连接到 UIStore 和外部回调。
  // These handlers connect SigmaCanvas low-level events to the
  // UIStore and external callbacks.

  /**
   * 节点点击处理。
   *
   * 1. 将点击的节点写入 UIStore（选中节点、打开详情面板）
   * 2. 调用外部 onNodeClick 回调（如果提供）
   *
   * Node click handler.
   *
   * 1. Write the clicked node to UIStore (select node, open detail panel)
   * 2. Call the external onNodeClick callback (if provided)
   */
  const handleNodeClick = useCallback(
    (nodeId: string, node: KGNode) => {
      selectNode(node)
      onNodeClick?.(nodeId, node)
    },
    [selectNode, onNodeClick],
  )

  /**
   * 边点击处理。
   *
   * 1. 将点击的边写入 UIStore（选中边、打开详情面板）
   * 2. 调用外部 onEdgeClick 回调（如果提供）
   *
   * Edge click handler.
   *
   * 1. Write the clicked edge to UIStore (select edge, open detail panel)
   * 2. Call the external onEdgeClick callback (if provided)
   */
  const handleEdgeClick = useCallback(
    (edgeId: string, edge: KGEdge) => {
      selectEdge(edge)
      onEdgeClick?.(edgeId, edge)
    },
    [selectEdge, onEdgeClick],
  )

  /**
   * 节点悬停处理。
   *
   * 1. 将悬停的节点 ID 写入 UIStore（用于外部高亮等逻辑）
   * 2. 调用外部 onNodeHover 回调（如果提供）
   *
   * Node hover handler.
   *
   * 1. Write the hovered node ID to UIStore (for external highlight logic)
   * 2. Call the external onNodeHover callback (if provided)
   */
  const handleNodeHover = useCallback(
    (nodeId: string | null) => {
      setHoveredNode(nodeId)
      onNodeHover?.(nodeId)
    },
    [setHoveredNode, onNodeHover],
  )

  // ── 渲染 / Render ────────────────────────────────────────────

  return (
    <div className="flex h-full w-full">
      {/* ── 左侧：数据库侧边栏 / Left: Database Sidebar ──────── */}
      <DBSidebar onLoadDatabase={onLoadDatabase} />

      {/* ── 中间：图可视化画布（2D/3D 可切换）/ Center: Graph Canvas (2D/3D switchable) ── */}
      {/* overflow-hidden 关键：ForceGraph3D 的 WebGL canvas 是 position:absolute，
          其宽度由 ResizeObserver 测量值驱动。当 DetailPanel 弹出使 flex 布局让 main
          变窄时，canvas 会短暂以旧的较大宽度溢出到右侧、盖住 DetailPanel（这正是
          "detailPanelOpen=true 但面板看不见"的根因）。overflow-hidden 将 canvas
          裁剪在画布区域内，使其无法侵入右侧详情面板。
          overflow-hidden is key: ForceGraph3D's absolute WebGL canvas can briefly
          overflow to the right (covering DetailPanel) when the flex layout shrinks
          main as the panel opens — the root cause of "detailPanelOpen=true but the
          panel is invisible". Clipping keeps the canvas inside the graph area. */}
      <main
        className="flex-1 relative overflow-hidden"
        style={{ background: 'var(--bg-root)' }}
      >
        {/* ── 演化模式顶栏（时间轴 + 曲线）/ Evolution top bar ── */}
        {evolutionEnabled && evolutionStatus === 'ready' && (
          <div
            className="absolute top-3 left-3 right-3 z-30 flex flex-col gap-1.5"
            style={{ pointerEvents: 'none' }}
          >
            <div
              className="flex items-center gap-2 px-2 py-1.5 rounded-lg"
              style={{
                background: 'var(--bg-card)',
                border: '1px solid var(--border-default)',
                pointerEvents: 'auto',
              }}
            >
              <span
                className="text-[11px] font-semibold shrink-0 select-none"
                style={{ color: 'var(--accent)' }}
              >
                📅 演化时间轴
              </span>
              <Timeline />
            </div>
            <div
              className="px-2 py-1 rounded-lg self-start"
              style={{
                background: 'var(--bg-card)',
                border: '1px solid var(--border-default)',
                pointerEvents: 'auto',
              }}
            >
              <MetricsChart />
            </div>
          </div>
        )}
        {evolutionEnabled && evolutionStatus === 'error' && (
          <div
            className="absolute top-3 left-3 z-30 px-3 py-2 rounded-lg text-xs"
            style={{
              background: 'rgba(239, 68, 68, 0.12)',
              border: '1px solid rgba(239, 68, 68, 0.3)',
              color: 'var(--error)',
            }}
          >
            ⚠ 演化后端未连接（启动 dk-evolve-serve --dir &lt;evolution-dir&gt;）
          </div>
        )}
        <GraphView
          graph={graph}
          onNodeClick={handleNodeClick}
          onEdgeClick={handleEdgeClick}
          onNodeHover={handleNodeHover}
          theme={theme}
        />

        {/* ── 演化事件流面板（浮动右侧）/ Changeset event panel ── */}
        {evolutionEnabled && <ChangesetPanel />}
      </main>

      {/* ── 右侧：详情面板（条件渲染）/ Right: Detail Panel (conditional) ── */}
      {detailPanelOpen && <DetailPanel />}
    </div>
  )
}

export default AppShell
