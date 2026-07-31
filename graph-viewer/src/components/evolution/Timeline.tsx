// ============================================================
// Timeline — 演化时间轴 / Evolution Timeline
//
// 每个 changeset 一个点（tool 着色：ingest 蓝 / incremental 绿 /
// rebalance 琥珀），大小按变更量（增删+合并+拆分）对数缩放。
// 点击 → selectSeq（懒加载该版快照 + 着色 + 事件流）。
// ============================================================

import { useEvolutionStore } from '../../stores/evolutionStore'
import type { EvolutionChangeset } from '../../types'

/** tool → 颜色（与图例一致） */
export const TOOL_COLORS: Record<string, string> = {
  ingest: '#60a5fa',
  incremental: '#34d399',
  rebalance: '#f59e0b',
}

/** 变更量 → 点半径（对数缩放，5..13px） */
function dotSize(cs: EvolutionChangeset): number {
  const volume =
    cs.n_added + cs.n_deleted + cs.n_merged + cs.n_split + cs.n_edge_added + cs.n_edge_dropped
  return Math.round(5 + Math.min(8, Math.log2(volume + 1) * 1.6))
}

/** 点的人读提示 / tooltip text */
function dotTitle(cs: EvolutionChangeset): string {
  return (
    `#${cs.seq} ${cs.tool} · ${cs.ts}\n` +
    `节点 +${cs.n_added} -${cs.n_deleted} 合${cs.n_merged} 拆${cs.n_split} 迁${cs.n_migrated} 改${cs.n_renamed} 内容${cs.n_content}\n` +
    `边 +${cs.n_edge_added} -${cs.n_edge_dropped}`
  )
}

export function Timeline() {
  const manifest = useEvolutionStore((s) => s.manifest)
  const selectedSeq = useEvolutionStore((s) => s.selectedSeq)
  const selectSeq = useEvolutionStore((s) => s.selectSeq)

  if (!manifest || manifest.changesets.length === 0) {
    return (
      <span className="text-[11px]" style={{ color: 'var(--text-muted)' }}>
        尚无 changeset（先运行 dk-ingest / dk-incremental / dk-rebalance）
      </span>
    )
  }

  return (
    <div
      className="flex items-center gap-1 overflow-x-auto py-1"
      data-testid="evolution-timeline"
      style={{ maxWidth: '52vw' }}
    >
      {manifest.changesets.map((cs) => {
        const active = cs.seq === selectedSeq
        const size = dotSize(cs)
        const color = TOOL_COLORS[cs.tool] ?? '#94a3b8'
        return (
          <button
            key={cs.seq}
            onClick={() => selectSeq(cs.seq)}
            title={dotTitle(cs)}
            className="shrink-0 rounded-full transition-transform hover:scale-125"
            style={{
              width: size * 2,
              height: size * 2,
              background: color,
              opacity: active ? 1 : 0.55,
              outline: active ? `2px solid ${color}` : 'none',
              outlineOffset: 2,
              border: 'none',
              cursor: 'pointer',
            }}
          />
        )
      })}
    </div>
  )
}
