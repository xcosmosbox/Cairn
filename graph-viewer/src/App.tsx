import { useCallback, useEffect, useRef, useState } from 'react'
import { ErrorBoundary } from './components/ui/ErrorBoundary'
import { AppShell } from './components/layout/AppShell'
import { DBLoader } from './services/db-loader'
import { GraphEngine } from './engine/graph-engine'
import { useDBStore } from './stores/dbStore'
import { useGraphStore } from './stores/graphStore'
import { useUIStore } from './stores/uiStore'
import { useEvolutionStore, evolutionSnapshotPath } from './stores/evolutionStore'
import { snapshotURL } from './services/evolution-api'
import type { KGNode, KGEdge, GraphLoadResult } from './types'
import type Graph from 'graphology'

// ============================================================
// App — 应用根组件（协调器）/ Application Root (Orchestrator)
//
// 职责：
//   1. 持有 DBLoader 实例与「按 path 缓存的已加载图」（useRef 保持稳定）
//   2. 响应文件加载事件：DBLoader 解析 .db → GraphEngine 构建图
//      → 存入缓存 → setActive(path) 切换到该库
//   3. 响应 activeId 变化：从缓存取出对应图并切换渲染（多 DB 切换核心）
//   4. 响应节点/边点击事件：更新 UI store → DetailPanel 显示
//   5. 处理错误、加载和空状态
//
// 多 DB 数据仓模型：
//   每个已加载的 .db 都在 graphCacheRef 中保留一份 { graph, result }。
//   侧边栏点击某个 DB → setActive(path) → activeId 变化 →
//   useEffect([activeId]) 从缓存取出该库的图切换显示。
//   加载第二个库不再覆盖第一个——两者都在缓存中并存，可随时切回。
//
// 数据流（单向）：
//   File → DBLoader.loadFromFile() → GraphLoadResult
//        → new GraphEngine().buildGraph() → graphology Graph（独立实例）
//        → graphCacheRef.set(path, {graph, result}) → setActive(path)
//        → useEffect([activeId]) → setGraph + setGraphData
//        → AppShell ← SigmaCanvas
//        → 用户点击节点 → useUIStore.selectNode() → DetailPanel
// ============================================================

/** 单个已加载数据库在缓存中的条目 / One cached loaded database */
interface CachedDB {
  /** 构建完成的 graphology 图实例（含布局坐标） / built graph with layout */
  graph: Graph
  /** 原始加载结果（nodes/edges/meta），供切换时恢复 graphStore / raw load result */
  result: GraphLoadResult
}

function App() {
  // ─── 服务实例与缓存（Ref 保持跨渲染稳定）/ Services & cache (refs) ──
  const dbLoaderRef = useRef<DBLoader | null>(null)
  // 按 DB path 缓存已构建的图；这是「多 DB 数据仓」的核心存储。
  // Keyed by DB path — the core store backing the multi-DB warehouse.
  const graphCacheRef = useRef<Map<string, CachedDB>>(new Map())

  // ─── 状态 / State ────────────────────────────────────────────
  const [appError, setAppError] = useState<string | null>(null)
  const [isProcessing, setIsProcessing] = useState(false)

  // ─── Zustand stores ──────────────────────────────────────────
  const loadDatabase = useDBStore(s => s.loadDatabase)
  const setActive = useDBStore(s => s.setActive)
  // 订阅 activeId 与 databases，驱动切换与缓存清理 / drive switching & pruning
  const activeId = useDBStore(s => s.activeId)
  const databases = useDBStore(s => s.databases)
  const setGraphData = useGraphStore(s => s.setGraphData)
  const clearGraph = useGraphStore(s => s.clearGraph)
  const setLoading = useGraphStore(s => s.setLoading)
  const setError = useGraphStore(s => s.setError)
  const selectNode = useUIStore(s => s.selectNode)
  const selectEdge = useUIStore(s => s.selectEdge)
  const setHoveredNode = useUIStore(s => s.setHoveredNode)

  // ─── 当前活跃的 graphology Graph / Current active graph ──────
  const [graph, setGraph] = useState<Graph | null>(null)

  // ─── 加载数据库 / Load Database ──────────────────────────────
  // 由 DBSidebar 的文件选择器触发 / Triggered by DBSidebar's file picker
  const handleLoadDatabase = useCallback(async (file: File) => {
    const dbName = file.name.replace(/\.db$/i, '')
    const path = `/dbs/${dbName}.db`

    // 去重：同名库若已在缓存中，直接切过去，不重复解析、不重复入列。
    // Dedup: if this DB is already cached, just switch to it — no re-parse.
    if (graphCacheRef.current.has(path)) {
      setActive(path)
      return
    }

    setIsProcessing(true)
    setAppError(null)
    setLoading(true)

    try {
      // Step 1: 解析 SQLite 数据库 / Parse SQLite database
      let loader = dbLoaderRef.current
      if (!loader) {
        loader = new DBLoader()
        dbLoaderRef.current = loader
      }
      const result = await loader.loadFromFile(file, dbName)

      // Step 2: 构建图 / Build graph
      // 每个 DB 使用独立的 GraphEngine 实例——GraphEngine.buildGraph 会
      // destroy 自身上一次构建的图，若复用同一 engine 会清空前一个库的图。
      // 独立实例确保多个库的图能在缓存中并存、互不销毁。
      // Each DB uses its own GraphEngine: buildGraph() destroys the engine's
      // previous graph, so reusing one engine would wipe the prior DB's graph.
      // A fresh engine per DB lets multiple graphs coexist in the cache.
      const engine = new GraphEngine()
      const g = engine.buildGraph(result.nodes, result.edges, {
        width: typeof window !== 'undefined' ? window.innerWidth * 0.65 : 1000,
        height: typeof window !== 'undefined' ? window.innerHeight : 800,
      })

      // Step 3: 存入缓存 / Store in cache
      graphCacheRef.current.set(path, { graph: g, result })

      // Step 4: 更新 DB 列表并激活该库 / Register and activate.
      // 切换渲染交由 useEffect([activeId]) 统一处理，此处不直接 setGraph。
      // Rendering is handled by useEffect([activeId]); we don't setGraph here.
      loadDatabase({
        name: dbName,
        path,
        domainCount: result.meta.domainCount,
        nodeCount: result.meta.nodeCount,
        edgeCount: result.meta.edgeCount,
        loadedAt: new Date().toISOString(),
      })
      setActive(path)
      setError(null)
    } catch (err: any) {
      const message = err?.message || 'Unknown error loading database'
      console.error('[GraphViewer] Failed to load database:', err)
      setAppError(message)
      setError(message)
    } finally {
      setIsProcessing(false)
      setLoading(false)
    }
  }, [loadDatabase, setActive, setLoading, setError])

  // ─── 切换活跃数据库 / Switch Active Database ─────────────────
  // activeId 变化时（用户点击侧边栏 DB，或加载后自动激活），从缓存取出
  // 对应库的图与数据切换渲染。这是「点击 DB → 展开对应 Graph」的实现。
  // When activeId changes (sidebar click or post-load auto-activate), pull the
  // corresponding graph + data from cache and switch the view.
  useEffect(() => {
    if (!activeId) {
      setGraph(null)
      clearGraph()
      return
    }
    const entry = graphCacheRef.current.get(activeId)
    if (!entry) return
    setGraph(entry.graph)
    setGraphData(entry.result)
    // 切换库时清空上一个库残留的选中，避免详情面板显示错库的节点。
    // Clear stale selection from the previous DB so the detail panel resets.
    selectNode(null)
  }, [activeId, clearGraph, setGraphData, selectNode])

  // ─── 缓存清理 / Cache Pruning ────────────────────────────────
  // 当某个 DB 从列表中被删除（databases 变化）时，释放其缓存的图实例，
  // 防止内存泄漏，并确保重新加载同名库时不会命中已失效的旧缓存。
  // When a DB is removed from the list, free its cached graph to avoid leaks
  // and ensure a re-load of the same name rebuilds instead of hitting stale cache.
  useEffect(() => {
    const livePaths = new Set(databases.map(db => db.path))
    for (const [path, entry] of graphCacheRef.current) {
      if (!livePaths.has(path)) {
        entry.graph.clear() // 释放 graphology 内部结构 / free graph internals
        graphCacheRef.current.delete(path)
      }
    }
  }, [databases])

  // ─── 启动即连后端（前后端服务器模式）/ Connect to dk-evolve-serve on mount ───
  // 单快照上传模式已移除，前端启动即连 dk-evolve-serve 拉 manifest。
  // Single-snapshot upload removed; the viewer connects to dk-evolve-serve on startup.
  const connectEvolution = useEvolutionStore((s) => s.connect)
  useEffect(() => {
    const st = useEvolutionStore.getState()
    if (st.enabled && st.status === 'idle') {
      void connectEvolution()
    }
  }, [connectEvolution])

  // ─── Stable Bundle 自动加载 / Stable Bundle Auto-Load ───────────
  // 若配置了 VITE_STABLE_BUNDLE_URL，启动时自动 fetch 该 stable KG 作为初始数据库。
  // dkctl bundle pull 安装 Bundle 到本地 → HTTP 服务（如 dk-evolve-serve）提供 .db → 前端自动加载。
  // If VITE_STABLE_BUNDLE_URL is set, auto-load the stable KG on startup.
  const stableBundleURL = import.meta.env.VITE_STABLE_BUNDLE_URL
  const stableBundleName = import.meta.env.VITE_STABLE_BUNDLE_NAME || 'stable'
  useEffect(() => {
    if (!stableBundleURL) return
    // 去重：已缓存则跳过。
    const stablePath = `/dbs/${stableBundleName}.db`
    if (graphCacheRef.current.has(stablePath)) return

    let cancelled = false
    ;(async () => {
      setIsProcessing(true)
      setLoading(true)
      try {
        let loader = dbLoaderRef.current
        if (!loader) {
          loader = new DBLoader()
          dbLoaderRef.current = loader
        }
        const result = await loader.loadFromURL(stableBundleURL)
        if (cancelled) return

        const engine = new GraphEngine()
        const g = engine.buildGraph(result.nodes, result.edges, {
          width: typeof window !== 'undefined' ? window.innerWidth * 0.65 : 1000,
          height: typeof window !== 'undefined' ? window.innerHeight : 800,
        })
        graphCacheRef.current.set(stablePath, { graph: g, result })

        loadDatabase({
          name: stableBundleName,
          path: stablePath,
          domainCount: result.meta.domainCount,
          nodeCount: result.meta.nodeCount,
          edgeCount: result.meta.edgeCount,
          loadedAt: new Date().toISOString(),
        })
        setActive(stablePath)
        setError(null)
      } catch (err: any) {
        console.error('[GraphViewer] Failed to load stable bundle:', err)
        setAppError(`Stable Bundle 加载失败: ${err?.message || err}`)
      } finally {
        if (!cancelled) {
          setIsProcessing(false)
          setLoading(false)
        }
      }
    })()
    return () => { cancelled = true }
  }, [stableBundleURL, stableBundleName, loadDatabase, setActive, setLoading, setError])

  // ─── 演化模式快照懒加载 / Evolution Snapshot Lazy Load ─────────
  // selectedPath 变化时（用户在时间轴上点一个点）→ fetch 该快照 db →
  // 入图仓库缓存 → setActive 切换到快照图。
  // LRU 淘汰：超出缓存上限时移除最久未点过的快照，绝不一次全载。
  const evolutionEnabled = useEvolutionStore((s) => s.enabled)
  const selectedPath = useEvolutionStore((s) => s.selectedPath)
  const baseUrl = useEvolutionStore((s) => s.baseUrl)
  const overlayVersion = useEvolutionStore((s) => s.overlayVersion)
  const registerLoadedPath = useEvolutionStore((s) => s.registerLoadedPath)

  useEffect(() => {
    if (!evolutionEnabled || !selectedPath) return

    // 去重：已缓存的快照直接切过去 / Dedup: cached snapshot → just switch
    if (graphCacheRef.current.has(selectedPath)) {
      setActive(selectedPath)
      return
    }

    let cancelled = false
    ;(async () => {
      const manifest = useEvolutionStore.getState().manifest
      const cs = manifest?.changesets.find((c) => evolutionSnapshotPath(c) === selectedPath)
      if (!cs) return

      setIsProcessing(true)
      setLoading(true)
      try {
        let loader = dbLoaderRef.current
        if (!loader) {
          loader = new DBLoader()
          dbLoaderRef.current = loader
        }
        const url = snapshotURL(baseUrl, cs.snapshot_file)
        const result = await loader.loadFromURL(url)
        if (cancelled) return

        const engine = new GraphEngine()
        const g = engine.buildGraph(result.nodes, result.edges, {
          width: typeof window !== 'undefined' ? window.innerWidth * 0.65 : 1000,
          height: typeof window !== 'undefined' ? window.innerHeight * 0.65 : 800,
        })
        graphCacheRef.current.set(selectedPath, { graph: g, result })

        // LRU 淘汰最久未点过的快照，释放 graphology 资源。
        const evicted = registerLoadedPath(selectedPath)
        if (evicted) {
          const entry = graphCacheRef.current.get(evicted)
          if (entry) {
            entry.graph.clear()
            graphCacheRef.current.delete(evicted)
          }
        }

        setActive(selectedPath)
      } catch (err: any) {
        console.error('[GraphViewer] Failed to load evolution snapshot:', err)
      } finally {
        if (!cancelled) {
          setIsProcessing(false)
          setLoading(false)
        }
      }
    })()
    return () => { cancelled = true }
  }, [evolutionEnabled, selectedPath, baseUrl])

  // 演化模式退出时恢复当前文件图的 activeId（不再盯着进化快照 path）。
  useEffect(() => {
    if (!evolutionEnabled) {
      // 恢复最后一个文件 DB 的 activeId（或清空）。
      const fileDBs = useDBStore.getState().databases
      if (fileDBs.length > 0) {
        setActive(fileDBs[fileDBs.length - 1].path)
      } else {
        setActive('')
      }
    }
  }, [evolutionEnabled])

  // ─── 节点点击 → UI store / Node Click → UI Store ────────────
  const handleNodeClick = useCallback((_nodeId: string, node: KGNode) => {
    selectNode(node)
  }, [selectNode])

  // ─── 边点击 → UI store / Edge Click → UI Store ──────────────
  const handleEdgeClick = useCallback((_edgeId: string, edge: KGEdge) => {
    selectEdge(edge)
  }, [selectEdge])

  // ─── 悬停 → UI store / Hover → UI Store ─────────────────────
  const handleNodeHover = useCallback((nodeId: string | null) => {
    setHoveredNode(nodeId)
  }, [setHoveredNode])

  // ─── 渲染 / Render ───────────────────────────────────────────

  return (
    <ErrorBoundary>
      <div className="h-full w-full flex flex-col" style={{ background: 'var(--bg-root)' }}>
        {/* 全局错误横幅 / Global error banner */}
        {appError && (
          <div
            className="px-4 py-2 text-sm flex items-center justify-between shrink-0"
            style={{ background: 'rgba(239, 68, 68, 0.15)', color: 'var(--error)' }}
          >
            <span>
              ⚠️ 加载失败 / Load failed: {appError}
            </span>
            <button
              onClick={() => setAppError(null)}
              className="px-2 py-1 text-xs rounded hover:opacity-80 transition-opacity"
              style={{ color: 'var(--text-secondary)' }}
            >
              关闭 / Dismiss
            </button>
          </div>
        )}

        {/* 处理进度条 / Processing indicator */}
        {isProcessing && (
          <div className="h-0.5 shrink-0" style={{ background: 'var(--accent)' }}>
            <div
              className="h-full animate-pulse"
              style={{ background: 'var(--accent)', width: '60%' }}
            />
          </div>
        )}

        {/* 主应用外壳 / Main App Shell
            AppShell 负责三栏布局（侧边栏 | 图 | 详情面板），
            事件通过 props 回调向上冒泡到这里，再分发到 Zustand stores。 */}
        <div className="flex-1 min-h-0">
          <AppShell
            graph={graph}
            onNodeClick={handleNodeClick}
            onEdgeClick={handleEdgeClick}
            onNodeHover={handleNodeHover}
            onLoadDatabase={handleLoadDatabase}
          />
        </div>
      </div>
    </ErrorBoundary>
  )
}

export default App
