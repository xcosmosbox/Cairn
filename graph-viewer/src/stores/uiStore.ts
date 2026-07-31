// ============================================================
// UI 状态 Store / UI State Store
//
// 职责：管理所有与图可视化相关的 UI 交互状态——节点/边的选中、
// 悬停高亮、详情面板开关、搜索查询、主题配置。本 Store 只跟踪
// "用户当前在做什么"，不持有图数据本身。
//
// Responsibility: Manages all UI interaction state for the graph
// visualization — node/edge selection, hover highlighting, detail
// panel visibility, search query, and theme. This store tracks
// "what the user is currently doing", not the graph data itself.
// ============================================================

import { create } from 'zustand'
import type { KGNode, KGEdge, GraphTheme } from '../types'

// ─── 视图模式 / View Mode ──────────────────────────────────────
// 看板渲染维度：'2d' = Sigma.js 力导向（默认），'3d' = react-force-graph-3d。
// 两种视图共享同一 graphology Graph 与同一套点击/悬停回调，仅渲染维度不同。
// Board rendering dimensionality: '2d' = Sigma.js force layout (default),
// '3d' = react-force-graph-3d. Both share the same graphology Graph and the
// same click/hover callbacks; only the rendering dimension differs.
export type ViewMode = '2d' | '3d'

// ─── 状态类型 / State Type ─────────────────────────────────────

/**
 * UI 状态 Store 的状态与操作。
 *
 * State and actions for the UI state store.
 */
interface UIState {
  /** 当前选中的节点；无选中时为 null / currently selected node, or null */
  selectedNode: KGNode | null

  /** 当前选中的边；无选中时为 null / currently selected edge, or null */
  selectedEdge: KGEdge | null

  /** 当前鼠标悬停的节点 ID；无悬停时为 null / id of the currently hovered node, or null */
  hoveredNodeId: string | null

  /** 详情面板是否打开 / whether the detail panel is open */
  detailPanelOpen: boolean

  /** 搜索框中的查询文本 / current search query string */
  searchQuery: string

  /** 当前应用的可视化主题；未设置时为 null / currently applied graph theme, or null */
  theme: GraphTheme | null

  /** 看板渲染维度：'2d'（默认）| '3d' / board rendering dimension: '2d' (default) | '3d' */
  viewMode: ViewMode

  /**
   * 选中一个节点。传入 null 取消选中。选中节点时会自动清空已选边，
   * 非 null 值时自动打开详情面板。
   *
   * Select or deselect a node. Selecting a node auto-clears any
   * selected edge and opens the detail panel when non-null.
   */
  selectNode: (node: KGNode | null) => void

  /**
   * 选中一条边。传入 null 取消选中。选中边时会自动清空已选节点，
   * 非 null 值时自动打开详情面板。
   *
   * Select or deselect an edge. Selecting an edge auto-clears any
   * selected node and opens the detail panel when non-null.
   */
  selectEdge: (edge: KGEdge | null) => void

  /**
   * 记录当前鼠标悬停的节点 ID。传入 null 表示鼠标离开所有节点。
   * Record the id of the node currently under the pointer, or null
   * when the pointer leaves all nodes.
   */
  setHoveredNode: (id: string | null) => void

  /**
   * 切换详情面板的展开/收起状态。可选参数显式指定开关状态。
   * Toggle the detail panel open/closed. An optional boolean argument
   * explicitly sets the state.
   */
  toggleDetailPanel: (open?: boolean) => void

  /**
   * 更新搜索框中的查询文本。
   * Update the search query string.
   */
  setSearchQuery: (query: string) => void

  /**
   * 设置当前图可视化主题。
   * Apply a graph visualization theme.
   */
  setTheme: (theme: GraphTheme) => void

  /**
   * 切换看板渲染维度（2D / 3D）。用于 GraphView 选择渲染器。
   * Set the board rendering dimension (2D / 3D). Used by GraphView to pick the renderer.
   */
  setViewMode: (mode: ViewMode) => void
}

// ─── Store 实现 / Store Implementation ──────────────────────────

export const useUIStore = create<UIState>()((set) => ({
  selectedNode: null,
  selectedEdge: null,
  hoveredNodeId: null,
  detailPanelOpen: false,
  searchQuery: '',
  theme: null,
  viewMode: '2d', // 默认 2D，保留既有观感 / default 2D, preserves existing look

  /**
   * 选中一个节点。规则：
   * - 传入 null → 取消选中，关闭详情面板
   * - 传入节点 → 设为当前选中节点，清空已选边，打开详情面板
   *
   * Select or deselect a node. Rules:
   * - null → clear selection, close detail panel
   * - non-null → select the node, clear any edge selection, open detail panel
   */
  selectNode: (node: KGNode | null) =>
    set({
      selectedNode: node,
      selectedEdge: null,
      detailPanelOpen: node !== null,
    }),

  /**
   * 选中一条边。规则：
   * - 传入 null → 取消选中，关闭详情面板
   * - 传入边 → 设为当前选中边，清空已选节点，打开详情面板
   *
   * Select or deselect an edge. Rules:
   * - null → clear selection, close detail panel
   * - non-null → select the edge, clear any node selection, open detail panel
   */
  selectEdge: (edge: KGEdge | null) =>
    set({
      selectedEdge: edge,
      selectedNode: null,
      detailPanelOpen: edge !== null,
    }),

  /**
   * 更新悬停节点 ID。用于节点高亮等交互反馈。
   * Update the hovered node id for highlight / interaction feedback.
   */
  setHoveredNode: (id: string | null) => set({ hoveredNodeId: id }),

  /**
   * 切换详情面板开关。若未传参数则取反当前状态。
   * Toggle the detail panel. If no argument is passed, flip the current state.
   */
  toggleDetailPanel: (open?: boolean) =>
    set((state) => ({
      detailPanelOpen: open !== undefined ? open : !state.detailPanelOpen,
    })),

  /**
   * 设置搜索查询文本，用于节点/边过滤。
   * Set the search query used for node/edge filtering.
   */
  setSearchQuery: (query: string) => set({ searchQuery: query }),

  /**
   * 应用指定的可视化主题。
   * Apply the given graph visualization theme.
   */
  setTheme: (theme: GraphTheme) => set({ theme }),

  /**
   * 切换 2D / 3D 渲染维度。
   * Switch between 2D / 3D rendering.
   */
  setViewMode: (mode: ViewMode) => set({ viewMode: mode }),
}))
