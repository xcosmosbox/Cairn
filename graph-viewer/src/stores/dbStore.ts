// ============================================================
// 数据库管理 Store / Database Management Store
//
// 职责：管理已加载的知识图谱数据库列表，跟踪当前活跃数据库。
// 数据持久化逻辑（OPFS 读写）由独立模块处理，本 Store 仅维护
// 数据库的元信息索引和选中状态。
//
// Responsibility: Maintains the list of loaded knowledge-graph
// databases and tracks which one is currently active. OPFS I/O
// is handled by a separate service; this store only holds the
// metadata index and selection state.
// ============================================================

import { create } from 'zustand'
import type { KGMeta } from '../types'

// ─── 状态类型 / State Type ─────────────────────────────────────

interface DBState {
  /** 已加载数据库的元信息列表 / list of loaded database metadata */
  databases: KGMeta[]

  /** 当前活跃数据库的路径（OPFS path），无选中时为 null */
  activeId: string | null

  /**
   * 添加已加载的数据库元信息。
   * 由 App 层在 DBLoader 解析完成后调用，携带真实的节点/边/领域计数。
   *
   * Adds pre-loaded database metadata. Called by the App layer
   * after DBLoader finishes parsing, with real counts.
   */
  loadDatabase: (meta: KGMeta) => void

  /**
   * 按路径从列表中移除一个数据库。
   * Remove a database from the list by its path.
   */
  removeDatabase: (path: string) => void

  /**
   * 将指定路径的数据库设为当前活跃数据库。
   * Set the database at the given path as the currently active one.
   */
  setActive: (path: string) => void
}

// ─── Store 实现 / Store Implementation ──────────────────────────

export const useDBStore = create<DBState>()((set) => ({
  databases: [],
  activeId: null,

  /** 添加预计算的数据库元信息 / Add pre-computed database metadata */
  loadDatabase: (meta: KGMeta) => {
    set((state) => ({
      databases: [...state.databases, meta],
    }))
  },

  /**
   * 按路径删除数据库。若删除的是当前活跃库，同时清除选中。
   * Remove by path. If the removed DB was active, clear activeId.
   */
  removeDatabase: (path: string) =>
    set((state) => ({
      databases: state.databases.filter((db) => db.path !== path),
      activeId: state.activeId === path ? null : state.activeId,
    })),

  /** 设置当前活跃数据库 / Set the active database by path */
  setActive: (path: string) => set({ activeId: path }),
}))
