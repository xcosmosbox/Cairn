// ============================================================
// DBSidebar — 数据库侧边栏（前后端服务器模式）/ Database Sidebar
//
// 单快照上传模式已移除。侧边栏现在只反映演化模式状态：
// 连接 dk-evolve-serve、manifest 摘要、哨兵趋势。
// Single-snapshot upload removed. The sidebar now reflects evolution
// mode only: dk-evolve-serve connection, manifest summary, sentinel trend.
// ============================================================

import { useEvolutionStore } from '../../stores/evolutionStore'
import { EVOLUTION_DEFAULT_BASE_URL } from '../../services/evolution-api'
import { SentinelTrend } from '../sentinel/SentinelTrend'
import { useDBStore } from '../../stores/dbStore'
import { Database, Loader2, GitBranch, AlertCircle, CheckCircle2, Box } from 'lucide-react'

// ─── Props / 组件属性 ──────────────────────────────────────────────

/**
 * DBSidebar 组件属性。
 *
 * 保留 onLoadDatabase 以维持向后兼容（App 仍可能传入），但前后端服务器模式下
 * 不再使用——单快照上传 UI 已移除。
 */
export interface DBSidebarProps {
  onLoadDatabase?: (file: File) => void
}

// ─── 组件 / Component ──────────────────────────────────────────────

/**
 * 演化模式侧边栏。显示连接状态、manifest 摘要、哨兵趋势。
 * Evolution-mode sidebar: connection status, manifest summary, sentinel trend.
 */
// eslint-disable-next-line @typescript-eslint/no-unused-vars
export function DBSidebar(_props: DBSidebarProps) {
  const evolutionEnabled = useEvolutionStore((s) => s.enabled)
  const evolutionStatus = useEvolutionStore((s) => s.status)
  const evolutionError = useEvolutionStore((s) => s.error)
  const manifest = useEvolutionStore((s) => s.manifest)
  const toggleEvolution = useEvolutionStore((s) => s.toggle)
  const reconnect = useEvolutionStore((s) => s.connect)

  // Stable Bundle 列表（由 App 启动时从 VITE_STABLE_BUNDLE_URL 自动加载）。
  const databases = useDBStore((s) => s.databases)
  const activeId = useDBStore((s) => s.activeId)
  const setActive = useDBStore((s) => s.setActive)

  const changesetCount = manifest?.changesets.length ?? 0
  const latestSeq =
    changesetCount > 0 ? manifest?.changesets[changesetCount - 1]?.seq : null

  return (
    <aside
      className="flex flex-col shrink-0 h-full"
      style={{
        width: 260,
        background: 'var(--bg-main)',
        borderRight: `1px solid var(--border-default)`,
      }}
    >
      {/* ── 标题栏 + 演化开关 / Header + Evolution Toggle ─── */}
      <div
        className="px-4 py-3 border-b shrink-0 flex items-center gap-2"
        style={{ borderColor: 'var(--border-default)' }}
      >
        <Database size={18} style={{ color: 'var(--accent)' }} />
        <h1
          className="text-sm font-semibold select-none"
          style={{ color: 'var(--text-primary)' }}
        >
          GraphViewer
        </h1>
        <button
          onClick={toggleEvolution}
          title={
            evolutionEnabled
              ? '关闭演化模式 / Exit evolution mode'
              : `演化模式（需运行 dk-evolve-serve 在 ${EVOLUTION_DEFAULT_BASE_URL}）/ Evolution mode`
          }
          className="ml-auto p-1 rounded transition-colors"
          style={{
            color: evolutionEnabled ? 'var(--accent)' : 'var(--text-muted)',
            background: evolutionEnabled ? 'var(--bg-active)' : 'transparent',
          }}
        >
          <GitBranch size={16} />
        </button>
      </div>

      {/* ── 演化状态面板 / Evolution Status Panel ─── */}
      <div className="p-3 shrink-0">
        {evolutionStatus === 'loading' && (
          <div
            className="flex items-center gap-2 text-xs"
            style={{ color: 'var(--text-muted)' }}
          >
            <Loader2 size={14} className="animate-spin" />
            连接后端中... / Connecting...
          </div>
        )}
        {evolutionStatus === 'ready' && (
          <div
            className="flex items-center gap-2 text-xs"
            style={{ color: 'var(--text-secondary)' }}
          >
            <CheckCircle2 size={14} style={{ color: 'var(--accent)' }} />
            已连后端 · {changesetCount} 条 changeset
            {latestSeq != null ? `（最新 #${latestSeq}）` : ''}
          </div>
        )}
        {evolutionStatus === 'error' && (
          <div className="text-xs" style={{ color: 'var(--text-muted)' }}>
            <div className="flex items-center gap-2 mb-1">
              <AlertCircle size={14} style={{ color: '#e5a00d' }} />
              连接失败 / Connection failed
            </div>
            <p className="leading-relaxed">
              {evolutionError ?? '无法连接 dk-evolve-serve / Cannot reach dk-evolve-serve'}
            </p>
            <p className="mt-1 opacity-70">
              请运行 / Run: dk-evolve-serve --dir &lt;evolution&gt;
            </p>
            <button
              onClick={() => reconnect()}
              className="mt-2 px-2 py-1 rounded text-xs transition-colors hover:opacity-80"
              style={{
                background: 'var(--accent-muted)',
                color: 'var(--accent)',
              }}
            >
              重试 / Retry
            </button>
          </div>
        )}
        {evolutionStatus === 'idle' && (
          <div className="text-xs" style={{ color: 'var(--text-muted)' }}>
            演化模式已关闭。点击右上角 GitBranch 图标开启。
            <br />
            Evolution mode off. Click the GitBranch icon to enable.
          </div>
        )}
      </div>

      {/* ── Stable Bundle 面板 / Stable Bundle Panel ─── */}
      {databases.length > 0 && (
        <div className="px-3 py-2 border-b shrink-0" style={{ borderColor: 'var(--border-default)' }}>
          <div className="flex items-center gap-1.5 mb-1.5">
            <Box size={13} style={{ color: 'var(--text-muted)' }} />
            <span className="text-[11px] font-medium" style={{ color: 'var(--text-secondary)' }}>
              已加载知识库 / Loaded KBs
            </span>
          </div>
          {databases.map((db) => (
            <button
              key={db.path}
              onClick={() => setActive(db.path)}
              className="w-full flex items-center gap-2 px-2 py-1.5 rounded text-left transition-colors"
              style={{
                background: activeId === db.path ? 'var(--bg-active)' : 'transparent',
              }}
            >
              <Database
                size={12}
                style={{ color: activeId === db.path ? 'var(--accent)' : 'var(--text-muted)' }}
              />
              <span
                className="text-xs truncate"
                style={{ color: activeId === db.path ? 'var(--text-primary)' : 'var(--text-muted)' }}
              >
                {db.name}
              </span>
              <span className="ml-auto text-[10px]" style={{ color: 'var(--text-muted)' }}>
                {db.nodeCount}n
              </span>
            </button>
          ))}
        </div>
      )}

      {/* ── 复杂度哨兵趋势（W-B）/ Sentinel Trend ── */}
      <SentinelTrend />

      {/* ── 底部状态栏 / Footer Status ─── */}
      <div
        className="mt-auto p-2 border-t shrink-0"
        style={{ borderColor: 'var(--border-default)' }}
      >
        <p
          className="text-[10px] text-center select-none"
          style={{ color: 'var(--text-muted)' }}
        >
          前后端服务器模式 · {EVOLUTION_DEFAULT_BASE_URL}
        </p>
      </div>
    </aside>
  )
}

export default DBSidebar
