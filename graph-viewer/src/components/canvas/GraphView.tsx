// ============================================================
// GraphView — 2D/3D 渲染切换容器 / 2D/3D Render Switch Container
//
// 单一职责（SOLID）：读取 uiStore.viewMode，选择渲染 SigmaCanvas（2D）
// 或 ForceGraph3DCanvas（3D），并把所有 props 原样透传。容器本身不关心
// 渲染细节——两个渲染器遵循完全相同的 props 契约，故可自由互换（里氏替换）。
// 右上角提供一个 2D/3D 切换按钮组，风格对齐现有工具栏按钮。
//
// Single responsibility (SOLID): read uiStore.viewMode and render either
// SigmaCanvas (2D) or ForceGraph3DCanvas (3D), passing all props straight
// through. The container knows nothing about rendering internals — both
// renderers share the exact same props contract, so they're interchangeable
// (Liskov). A 2D/3D toggle sits top-right, styled to match existing toolbar buttons.
// ============================================================

import { Box, Share2 } from 'lucide-react'
import type Graph from 'graphology'
import { SigmaCanvas } from './SigmaCanvas'
import { ForceGraph3DCanvas } from './ForceGraph3DCanvas'
import { useUIStore, type ViewMode } from '../../stores/uiStore'
import type { KGNode, KGEdge, GraphTheme } from '../../types'

// ─── Props / 组件属性 ────────────────────────────────────────────
// 与 SigmaCanvas / ForceGraph3DCanvas 完全一致的契约，AppShell 原样透传。
// Identical contract to SigmaCanvas / ForceGraph3DCanvas; AppShell passes through.

interface GraphViewProps {
  /** graphology Graph 实例 / graphology Graph instance */
  graph: Graph | null
  /** 节点点击回调 / node click callback */
  onNodeClick?: (nodeId: string, node: KGNode) => void
  /** 边点击回调 / edge click callback */
  onEdgeClick?: (edgeId: string, edge: KGEdge) => void
  /** 节点悬停回调 / node hover callback */
  onNodeHover?: (nodeId: string | null) => void
  /** 可视化主题 / visualization theme */
  theme?: GraphTheme | null
}

// ─── GraphView 组件 / Component ──────────────────────────────────

/**
 * 图渲染切换容器。按 uiStore.viewMode 渲染 2D 或 3D 画布，props 透传。
 *
 * Graph render switch container. Renders the 2D or 3D canvas per
 * uiStore.viewMode, passing props through.
 */
export function GraphView(props: GraphViewProps) {
  const viewMode = useUIStore((s) => s.viewMode)
  const setViewMode = useUIStore((s) => s.setViewMode)

  return (
    <div className="relative w-full h-full">
      {/* ── 渲染器：按 viewMode 二选一 / Renderer: 2D or 3D by viewMode ── */}
      {/* 通过卸载/挂载切换，确保 3D 的 three.js 资源在切回 2D 时被清理。 */}
      {/* Toggling by unmount/mount ensures 3D's three.js resources are freed on switch back. */}
      {viewMode === '3d' ? (
        <ForceGraph3DCanvas {...props} />
      ) : (
        <SigmaCanvas {...props} />
      )}

      {/* ── 右上角 2D/3D 切换按钮组 / Top-right 2D/3D toggle ── */}
      {/* 【问题1 修复】使用 position:fixed（相对视口）而非 absolute。原因：
          ForceGraph3D 内部的 three.js 容器为 position:relative、canvas 为
          position:absolute，会在 GraphView 内形成新的层叠上下文，导致 absolute +
          z-index 的按钮仍被 3D 画布视觉遮挡而"消失"。fixed 让按钮脱离所有祖先层叠
          上下文、直接相对视口渲染，配合极高 z-index，保证 2D/3D 下始终可见可点。
          right:24 贴近画布右上角。 */}
      {/* [Problem #1 fix] Use position:fixed (viewport-relative) instead of
          absolute: ForceGraph3D's relative container + absolute canvas form a new
          stacking context inside GraphView, so an absolute+z-index button still
          gets covered by the 3D canvas. fixed detaches it from all ancestor
          stacking contexts, rendering relative to the viewport with a very high
          z-index — visible/clickable in both 2D and 3D. */}
      <div
        className="fixed top-4 flex gap-1 p-1 rounded-lg"
        style={{
          right: 24,
          background: 'var(--bg-card)',
          border: '1px solid var(--border-default)',
          zIndex: 9999,
          pointerEvents: 'auto',
        }}
      >
        <ViewModeButton
          mode="2d"
          active={viewMode === '2d'}
          onClick={() => setViewMode('2d')}
          icon={<Share2 className="w-4 h-4" />}
          label="2D"
          title="2D 力导向视图 / 2D force-directed view"
        />
        <ViewModeButton
          mode="3d"
          active={viewMode === '3d'}
          onClick={() => setViewMode('3d')}
          icon={<Box className="w-4 h-4" />}
          label="3D"
          title="3D 立体视图（缓解簇/边重叠）/ 3D view (eases cluster/edge overlap)"
        />
      </div>
    </div>
  )
}

// ─── 切换按钮 / Toggle Button ────────────────────────────────────

interface ViewModeButtonProps {
  mode: ViewMode
  active: boolean
  onClick: () => void
  icon: React.ReactNode
  label: string
  title: string
}

/**
 * 单个 2D/3D 切换按钮。激活态用 accent 高亮，非激活态与工具栏按钮一致。
 * A single 2D/3D toggle button. Active state uses the accent highlight;
 * inactive matches the toolbar buttons.
 */
function ViewModeButton({ active, onClick, icon, label, title }: ViewModeButtonProps) {
  return (
    <button
      onClick={onClick}
      title={title}
      className="flex items-center gap-1.5 px-2.5 py-1.5 rounded-md text-xs font-medium transition-colors"
      style={{
        background: active ? 'var(--accent)' : 'transparent',
        color: active ? '#fff' : 'var(--text-secondary)',
      }}
      onMouseEnter={(e) => {
        if (!active) e.currentTarget.style.color = 'var(--text-primary)'
      }}
      onMouseLeave={(e) => {
        if (!active) e.currentTarget.style.color = 'var(--text-secondary)'
      }}
    >
      {icon}
      {label}
    </button>
  )
}

export default GraphView
