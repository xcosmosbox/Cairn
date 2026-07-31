// ============================================================
// MetricsChart — 演化规模与质量曲线 / Evolution Metrics Chart
//
// 数据源：manifest.metrics（每次 changeset 一行的哨兵采样）。
// 复用 W-B SentinelTrend 的「迷你 SVG 趋势线面板」模式：
// 规模两项（节点数 / 边数）+ 质量四项（Q / 单例率 / 边节点比 / 累计改动），
// 质量项带阈值虚线与越界红点。x 轴 = changeset seq。
// ============================================================

import { useMemo } from 'react'
import { useEvolutionStore } from '../../stores/evolutionStore'
import type { EvolutionMetric } from '../../types'

interface MetricDef {
  title: string
  pick: (m: EvolutionMetric) => number
  color: string
  threshold?: number
  isBreach?: (v: number) => boolean
  fmt: (v: number) => string
}

const METRICS: MetricDef[] = [
  { title: '节点数', pick: (m) => m.node_count, color: '#60a5fa', fmt: (v) => String(v) },
  { title: '边数', pick: (m) => m.edge_count, color: '#34d399', fmt: (v) => String(v) },
  { title: '模块度 Q', pick: (m) => m.modularity_q, color: '#a78bfa', threshold: 0.3, isBreach: (v) => v < 0.3, fmt: (v) => v.toFixed(3) },
  { title: '单例率', pick: (m) => m.singleton_ratio, color: '#f472b6', threshold: 0.4, isBreach: (v) => v > 0.4, fmt: (v) => v.toFixed(3) },
  { title: '边/节点比', pick: (m) => m.edge_node_ratio, color: '#fbbf24', threshold: 3.0, isBreach: (v) => v > 3.0, fmt: (v) => v.toFixed(2) },
  { title: '累计改动', pick: (m) => m.cumulative_ratio, color: '#fb923c', threshold: 0.2, isBreach: (v) => v > 0.2, fmt: (v) => v.toFixed(3) },
]

const W = 132
const H = 40
const PAD = 4

/** 单指标迷你趋势线（W-B SentinelTrend 模式的复用） */
function MiniTrend({ def, samples }: { def: MetricDef; samples: EvolutionMetric[] }) {
  const { path, breachIdxs, yAt, last } = useMemo(() => {
    const vals = samples.map(def.pick)
    const min = Math.min(...vals, def.threshold ?? Infinity)
    const max = Math.max(...vals, def.threshold ?? -Infinity)
    const span = max - min || 1
    const xAt = (i: number) => PAD + (i * (W - 2 * PAD)) / Math.max(1, samples.length - 1)
    const y = (v: number) => H - PAD - ((v - min) / span) * (H - 2 * PAD)
    const d = vals.map((v, i) => `${i === 0 ? 'M' : 'L'}${xAt(i).toFixed(1)},${y(v).toFixed(1)}`).join(' ')
    const breaches = vals
      .map((v, i) => (def.isBreach?.(v) ? i : -1))
      .filter((i) => i >= 0)
    return { path: d, breachIdxs: breaches, yAt: y, last: vals[vals.length - 1] }
  }, [def, samples])

  return (
    <div className="flex flex-col items-center gap-0.5 select-none" title={def.title}>
      <svg width={W} height={H} className="block">
        {def.threshold !== undefined && (
          <line
            x1={PAD}
            x2={W - PAD}
            y1={yAt(def.threshold)}
            y2={yAt(def.threshold)}
            stroke="var(--text-muted)"
            strokeOpacity={0.5}
            strokeDasharray="3 3"
            strokeWidth={1}
          />
        )}
        <path d={path} fill="none" stroke={def.color} strokeWidth={1.5} />
        {breachIdxs.map((i) => (
          <circle
            key={i}
            cx={PAD + (i * (W - 2 * PAD)) / Math.max(1, samples.length - 1)}
            cy={yAt(def.pick(samples[i]))}
            r={2.5}
            fill="#ef4444"
          />
        ))}
      </svg>
      <span className="text-[10px]" style={{ color: 'var(--text-muted)' }}>
        {def.title} <span style={{ color: def.color }}>{def.fmt(last)}</span>
      </span>
    </div>
  )
}

export function MetricsChart() {
  const manifest = useEvolutionStore((s) => s.manifest)
  const samples = manifest?.metrics ?? []
  if (samples.length === 0) return null

  return (
    <div
      className="flex gap-3 overflow-x-auto px-1 pb-1"
      data-testid="evolution-metrics"
      style={{ maxWidth: '60vw' }}
    >
      {METRICS.map((def) => (
        <MiniTrend key={def.title} def={def} samples={samples} />
      ))}
    </div>
  )
}
