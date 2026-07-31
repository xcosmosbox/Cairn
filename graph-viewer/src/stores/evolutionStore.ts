// ============================================================
// evolutionStore — 演化模式状态 / Evolution Mode Store
//
// 职责：
//   - 后端连接（baseUrl + manifest 缓存）与降级（缺省安全：无后端不崩）
//   - 当前选中的 changeset（selectedSeq）与其 diff 着色索引
//   - 快照图缓存的 LRU 队列（与 App 的图仓库协作，绝不一次全载）
//
// 快照图本身的加载与渲染复用 App 的「多 DB 数据仓」：
// selectedSeq 变化 → App 的 effect 懒加载该快照 → setActive(path)。
// 本 store 只维护演化元数据与着色状态。
// ============================================================

import { create } from 'zustand'
import type { EvolutionChangeset, EvolutionManifest } from '../types'
import { fetchManifest, EVOLUTION_DEFAULT_BASE_URL } from '../services/evolution-api'

/** 快照图缓存上限（LRU；超出淘汰最久未选中的快照） */
export const EVOLUTION_CACHE_CAP = 6

/** diff 着色板（与 dk show / Markdown 事件配色一致） */
export const DIFF_COLORS = {
  added: '#22c55e', // 🟢 新增
  deleted: '#ef4444', // 🔴 删除（仅事件流；图上该节点已不存在）
  merged: '#eab308', // 🟡 合并汇入点
  split: '#eab308', // 🟡 拆分产物
  migrated: '#3b82f6', // 🔵 迁移
  renamed: '#3b82f6', // 🔵 改名
  content: '#a855f7', // 🟣 内容变更
} as const

/** 演化快照在图仓库中的 path（与 App 的 effect 保持同一公式） */
export function evolutionSnapshotPath(cs: EvolutionChangeset): string {
  return `evolution://${cs.seq}-${cs.snapshot_file}`
}

// ─── 状态类型 / State Type ────────────────────────────────────

interface EvolutionState {
  /** 演化模式开关（sidebar 按钮切换） */
  enabled: boolean
  /** 后端地址 / dk-evolve-serve base URL */
  baseUrl: string
  /** 连接状态 / manifest fetch status */
  status: 'idle' | 'loading' | 'ready' | 'error'
  /** 连接/加载错误信息 / error message */
  error: string | null
  /** 已拉取的 manifest / cached manifest */
  manifest: EvolutionManifest | null
  /** 当前选中的 changeset seq / selected changeset */
  selectedSeq: number | null
  /** 选中快照在图仓库中的 path（着色仅在该图显示时生效） */
  selectedPath: string | null
  /** diff 着色开关 / diff color overlay toggle */
  overlay: boolean
  /** 着色版本号：变化时触发 Sigma refresh / bump to re-run reducers */
  overlayVersion: number
  /** 快照缓存 LRU 队列（新→旧）/ snapshot cache queue for LRU eviction */
  loadedPaths: string[]
  /** 当前 diff 的节点着色索引（uuid → css 颜色）/ node color map */
  nodeColors: Record<string, string>
  /** 当前 diff 的新增边索引（"src tgt kind" → true）/ added edge key set */
  addedEdgeKeys: Record<string, true>

  /** 开关演化模式 / toggle evolution mode */
  toggle: () => void
  /** 设置后端地址（下次 connect 生效）/ set base URL */
  setBaseUrl: (url: string) => void
  /** 连接后端并拉取 manifest / connect and fetch manifest */
  connect: () => Promise<void>
  /** 选中一个 changeset（触发懒加载 + 着色索引重建）/ select a changeset */
  selectSeq: (seq: number) => void
  /** 取消选中（返回当前图）/ clear selection */
  clearSelection: () => void
  /** 开关 diff 着色 / toggle diff overlay */
  setOverlay: (on: boolean) => void
  /** 登记一个已加载快照 path；返回应 LRU 淘汰的 path（无则 null） */
  registerLoadedPath: (path: string) => string | null
}

// ─── 着色索引推导 / Overlay Index Derivation ──────────────────

/**
 * 从 changeset 的 diff_json 推导着色索引：
//  added/content/migrated/renamed → 自身节点；merged_into → 幸存者；
//  split_into → 各产物；deleted/被合并/被拆走的节点已不在快照图中，
//  只在事件流面板呈现。
 */
function deriveOverlay(cs: EvolutionChangeset): {
  nodeColors: Record<string, string>
  addedEdgeKeys: Record<string, true>
} {
  const nodeColors: Record<string, string> = {}
  const addedEdgeKeys: Record<string, true> = {}
  const diff = cs.diff
  if (!diff) return { nodeColors, addedEdgeKeys }

  for (const n of diff.nodes ?? []) {
    switch (n.change) {
      case 'added':
        nodeColors[n.id] = DIFF_COLORS.added
        break
      case 'content':
        nodeColors[n.id] = DIFF_COLORS.content
        break
      case 'migrated':
      case 'renamed':
        nodeColors[n.id] = DIFF_COLORS.migrated
        break
      case 'merged_into': {
        const d = n.detail as { into?: string } | undefined
        if (d?.into) nodeColors[d.into] = DIFF_COLORS.merged
        break
      }
      case 'split_into': {
        const d = n.detail as { into?: string[] } | undefined
        if (Array.isArray(d?.into)) {
          for (const u of d.into) nodeColors[u] = DIFF_COLORS.split
        }
        break
      }
      default:
        break // unchanged / deleted：不着色
    }
  }
  for (const e of diff.edges ?? []) {
    if (e.change === 'added') {
      addedEdgeKeys[`${e.source} ${e.target} ${e.kind}`] = true
    }
  }
  return { nodeColors, addedEdgeKeys }
}

// ─── Store 实现 / Store Implementation ────────────────────────

export const useEvolutionStore = create<EvolutionState>()((set, get) => ({
  // 默认开启演化模式（前后端服务器模式）：前端启动即连 dk-evolve-serve。
  // 单快照上传模式已移除，演化模式为唯一工作模式。
  // Evolution mode is on by default: the viewer connects to dk-evolve-serve on startup.
  // Single-snapshot upload has been removed; evolution is the only mode.
  enabled: true,
  baseUrl: EVOLUTION_DEFAULT_BASE_URL,
  status: 'idle',
  error: null,
  manifest: null,
  selectedSeq: null,
  selectedPath: null,
  overlay: true,
  overlayVersion: 0,
  loadedPaths: [],
  nodeColors: {},
  addedEdgeKeys: {},

  toggle: () => {
    const next = !get().enabled
    set({ enabled: next })
    if (next && get().status === 'idle') {
      void get().connect()
    }
    if (!next) {
      // 退出演化模式：清选中与着色，快照图仍留在图仓库里可手动切换。
      set((s) => ({
        selectedSeq: null,
        selectedPath: null,
        nodeColors: {},
        addedEdgeKeys: {},
        overlayVersion: s.overlayVersion + 1,
      }))
    }
  },

  setBaseUrl: (url) => set({ baseUrl: url }),

  connect: async () => {
    set({ status: 'loading', error: null })
    try {
      const manifest = await fetchManifest(get().baseUrl)
      set((s) => ({
        manifest,
        status: 'ready',
        // 后端可能已换库：旧快照缓存与新 manifest 不再对应，清 LRU 队列。
        loadedPaths: [],
        overlayVersion: s.overlayVersion + 1,
      }))
    } catch (err: any) {
      set({
        status: 'error',
        manifest: null,
        error: err?.message ?? String(err),
      })
    }
  },

  selectSeq: (seq) => {
    const { manifest } = get()
    const cs = manifest?.changesets.find((c) => c.seq === seq)
    if (!cs) return
    const { nodeColors, addedEdgeKeys } = deriveOverlay(cs)
    set((s) => ({
      selectedSeq: seq,
      selectedPath: evolutionSnapshotPath(cs),
      nodeColors,
      addedEdgeKeys,
      overlayVersion: s.overlayVersion + 1,
    }))
  },

  clearSelection: () =>
    set((s) => ({
      selectedSeq: null,
      selectedPath: null,
      nodeColors: {},
      addedEdgeKeys: {},
      overlayVersion: s.overlayVersion + 1,
    })),

  setOverlay: (on) =>
    set((s) => ({ overlay: on, overlayVersion: s.overlayVersion + 1 })),

  registerLoadedPath: (path) => {
    const queue = [path, ...get().loadedPaths.filter((p) => p !== path)]
    if (queue.length <= EVOLUTION_CACHE_CAP) {
      set({ loadedPaths: queue })
      return null
    }
    // 淘汰最久未选中的（队列尾）；不淘汰当前选中项。
    let evicted: string | null = null
    while (queue.length > EVOLUTION_CACHE_CAP) {
      const candidate = queue.pop()!
      if (candidate === get().selectedPath) {
        // 当前正在看的快照不淘汰，留回队列头一侧。
        queue.unshift(candidate)
        break
      }
      evicted = candidate
    }
    set({ loadedPaths: queue })
    return evicted
  },
}))
