// ============================================================
// 图数据 Store / Graph Data Store
//
// 职责：持有当前活跃数据库所加载的节点与边数据，并对外暴露
// 加载状态与错误信息。本 Store 只关心“当前显示什么图”，不关心
// 图如何被渲染或布局——那是图引擎和 UI 的职责。
//
// Responsibility: Holds the nodes and edges loaded from the
// currently active database, plus loading/error metadata. This
// store answers "what graph is currently displayed" — layout and
// rendering belong to the engine and UI layers.
// ============================================================

import { create } from 'zustand'
import type { KGNode, KGEdge, GraphLoadResult, SentinelSample } from '../types'

// ─── 状态类型 / State Type ─────────────────────────────────────

/**
 * 图数据 Store 的状态与操作。
 *
 * State and actions for the graph data store.
 */
interface GraphState {
  /** 当前图中的所有节点 / all nodes in the current graph */
  nodes: KGNode[]

  /** 当前图中的所有边 / all edges in the current graph */
  edges: KGEdge[]

  /**
   * 当前库的复杂度哨兵历史时序（W-B，老库为空数组）。
   * Sentinel history of the active DB (W-B; empty for older DBs).
   */
  sentinelHistory: SentinelSample[]

  /** 是否正在加载数据 / whether data is currently loading */
  isLoading: boolean

  /** 加载失败时的错误信息；成功时为 null / error message on failure, or null */
  error: string | null

  /**
   * 用加载结果替换当前图数据。通常在从数据库成功读取后调用。
   * Replace the current graph data with a load result. Typically
   * called after a successful database read.
   */
  setGraphData: (result: GraphLoadResult) => void

  /**
   * 清空所有节点与边，并重置加载状态和错误信息。
   * 适用于切换数据库或手动重置场景。
   *
   * Clear all nodes, edges, loading state, and error. Useful when
   * switching databases or performing a manual reset.
   */
  clearGraph: () => void

  /**
   * 手动设置加载中标识。
   * Manually set the loading flag.
   */
  setLoading: (loading: boolean) => void

  /**
   * 设置错误信息（传入 null 表示清除错误）。
   * Set or clear the error message.
   */
  setError: (error: string | null) => void
}

// ─── Store 实现 / Store Implementation ──────────────────────────

export const useGraphStore = create<GraphState>()((set) => ({
  nodes: [],
  edges: [],
  sentinelHistory: [],
  isLoading: false,
  error: null,

  /**
   * 将 GraphLoadResult 写入 Store，同时关闭加载状态并清空错误。
   *
   * Populate nodes and edges from a GraphLoadResult, clear loading
   * and error flags as a side effect.
   */
  setGraphData: (result: GraphLoadResult) =>
    set({
      nodes: result.nodes,
      edges: result.edges,
      sentinelHistory: result.sentinelHistory ?? [],
      isLoading: false,
      error: null,
    }),

  /**
   * 重置图数据至初始空状态。
   * Reset all graph-related state back to initial empty defaults.
   */
  clearGraph: () =>
    set({
      nodes: [],
      edges: [],
      sentinelHistory: [],
      isLoading: false,
      error: null,
    }),

  /**
   * 设置加载中标记。在加载开始时设为 true，完成或出错后设为 false。
   * Toggle the loading state. Set to true at the start of a load
   * operation, false when done or on error.
   */
  setLoading: (loading: boolean) => set({ isLoading: loading }),

  /**
   * 设置错误信息。传入 null 清空错误状态。
   * Store an error string, or pass null to clear.
   */
  setError: (error: string | null) => set({ error }),
}))
