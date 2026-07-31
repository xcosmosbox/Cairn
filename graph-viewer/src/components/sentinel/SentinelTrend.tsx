// ============================================================
// SentinelTrend — 复杂度哨兵趋势图 / Complexity Sentinel Trend
//
// 可视化 dk-service Sentinel（W-B）写入 kg_manifest.sentinel.history 的
// 时序：现状模块度 Q / 单例子域占比 / 边节点比 / 累计改动占比四条迷你
// 时间线，各自带阈值参考线（0.3 下限 / 0.4 / 3.0 / 0.2 上限），越界点
// 红色高亮。数据来自 graphStore.sentinelHistory（DBLoader 加载时读取，
// 老库无该键则本组件不渲染）。
//
// 纯 SVG 手绘，零新增依赖；风格沿用 CSS 变量与 lucide 图标。
//
// Renders the sentinel.history time series written by dk-service Sentinel
// (W-B): four mini charts (Q / singleton ratio / edge-node ratio /
// cumulative change ratio) with threshold reference lines and breach
// highlighting. Pure SVG, no new dependencies.
// ============================================================

import { useMemo } from 'react'
import { Activity } from 'lucide-react'
import { useGraphStore } from '../../stores/graphStore'
import type { SentinelSample } from '../../types'

// ─── 指标定义 / Metric Definitions ────────────────────────────

/**
 * 单条趋势线的渲染配置。
 * Render config for one trend line.
 */
interface MetricDef {
  /** 面板标题 / panel title */
  title: string
  /** 从采样点取值 / extract the metric value from a sample */
  pick: (s: SentinelSample) => number
  /** 阈值参考线位置 / threshold reference value */
  threshold: number
  /** 阈值判定：true = 越界 / breach predicate */
  isBreach: (v: number) => boolean
  /** 阈值方向文案（下限/上限）/ threshold direction label */
  direction: '下限' | '上限'
  /** 系列颜色 / series color */
  color: string
  /** Y 轴展示上限（留 20% 头部空间）/ Y display cap */
  yMax: (vals: number[]) => number
}

const METRICS: MetricDef[] = [
  {
    title: '模块度 Q',
    pick: (s) => s.q,
    threshold: 0.3,
    isBreach: (v) => v < 0.3,
    direction: '下限',
    color: '#60a5fa',
    yMax: (vals) => Math.max(0.5, ...vals) * 1.2,
  },
  {
    title: '单例率',
    pick: (s) => s.singleton_ratio,
    threshold: 0.4,
    isBreach: (v) => v > 0.4,
    direction: '上限',
    color: '#a78bfa',
    yMax: (vals) => Math.max(0.5, ...vals) * 1.2,
  },
  {
    title: '边/节点比',
    pick: (s) => s.edge_node_ratio,
    threshold: 3.0,
    isBreach: (v) => v > 3.0,
    direction: '上限',
    color: '#34d399',
    yMax: (vals) => Math.max(3.5, ...vals) * 1.2,
  },
  {
    title: '累计改动',
    pick: (s) => s.cumulative_ratio,
    threshold: 0.2,
    isBreach: (v) => v > 0.2,
    direction: '上限',
    color: '#fbbf24',
    yMax: (vals) => Math.max(0.3, ...vals) * 1.2,
  },
]

// ─── 迷你趋势面板 / Mini Trend Panel ──────────────────────────

const PANEL_W = 236
const PANEL_H = 44
const PAD_L = 4
const PAD_R = 4
const PAD_T = 4
const PAD_B = 4

/**
 * 单指标迷你趋势面板：折线 + 阈值虚线 + 越界点红色高亮。
 * One metric mini panel: polyline + dashed threshold + red breach points.
 */
function TrendPanel({ def, samples }: { def: MetricDef; samples: SentinelSample[] }) {
  const vals = samples.map(def.pick)
  const max = def.yMax(vals)
  const n = vals.length
  const x = (i: number) =>
    PAD_L + (n <= 1 ? (PANEL_W - PAD_L - PAD_R) / 2 : (i * (PANEL_W - PAD_L - PAD_R)) / (n - 1))
  const y = (v: number) => PANEL_H - PAD_B - (Math.min(v, max) / max) * (PANEL_H - PAD_T - PAD_B)

  const points = vals.map((v, i) => `${x(i)},${y(v)}`).join(' ')
  const latest = vals[n - 1]
  const breached = def.isBreach(latest)

  return (
    <div className="mb-2 last:mb-0">
      <div className="flex items-center justify-between mb-0.5">
        <span className="text-[10px] select-none" style={{ color: 'var(--text-muted)' }}>
          {def.title}
          <span style={{ opacity: 0.6 }}>
            {' '}（{def.direction} {def.threshold}）
          </span>
        </span>
        <span
          className="text-[10px] font-mono select-none"
          style={{ color: breached ? 'var(--error, #ef4444)' : 'var(--text-secondary)' }}
          title={breached ? '已越阈值 / threshold breached' : '阈值内 / within threshold'}
        >
          {breached ? '⚠ ' : ''}{latest.toFixed(3)}
        </span>
      </div>
      <svg width={PANEL_W} height={PANEL_H} className="block" role="img" aria-label={def.title}>
        {/* 阈值参考虚线 / dashed threshold reference */}
        <line
          x1={PAD_L}
          x2={PANEL_W - PAD_R}
          y1={y(def.threshold)}
          y2={y(def.threshold)}
          stroke="var(--text-muted)"
          strokeOpacity={0.5}
          strokeDasharray="3 3"
          strokeWidth={1}
        />
        {/* 趋势线 / trend polyline */}
        {n > 1 && (
          <polyline points={points} fill="none" stroke={def.color} strokeWidth={1.5} />
        )}
        {/* 数据点（越界红色高亮）/ data points (breach highlighted red) */}
        {vals.map((v, i) => (
          <circle
            key={i}
            cx={x(i)}
            cy={y(v)}
            r={def.isBreach(v) ? 3 : 2}
            fill={def.isBreach(v) ? 'var(--error, #ef4444)' : def.color}
          >
            <title>
              {samples[i].ts}: {v.toFixed(3)}
              {def.isBreach(v) ? '（越界 / breach）' : ''}
              {samples[i].breach_reasons?.length ? `\n${samples[i].breach_reasons!.join('\n')}` : ''}
            </title>
          </circle>
        ))}
      </svg>
    </div>
  )
}

// ─── 组件 / Component ─────────────────────────────────────────

/**
 * 复杂度哨兵趋势区块（侧边栏嵌入版）。
 * 老库无 sentinel.history 时渲染为 null（不占位、不报错）。
 *
 * Sentinel trend block for the sidebar. Renders null when the active DB
 * has no sentinel.history (older DBs stay visually unchanged).
 */
export function SentinelTrend() {
  const sentinelHistory = useGraphStore((s) => s.sentinelHistory)

  // 最新一条的越界原因汇总（面板顶部告警条）。
  // Latest breach reasons aggregated for the header alert line.
  const latestBreaches = useMemo(
    () => sentinelHistory[sentinelHistory.length - 1]?.breach_reasons ?? [],
    [sentinelHistory],
  )

  if (sentinelHistory.length === 0) return null

  return (
    <div
      className="p-2 border-t shrink-0"
      style={{ borderColor: 'var(--border-default)' }}
      data-testid="sentinel-trend"
    >
      <div className="flex items-center gap-1.5 mb-1.5">
        <Activity size={13} style={{ color: 'var(--accent)' }} />
        <span className="text-[11px] font-medium select-none" style={{ color: 'var(--text-primary)' }}>
          复杂度哨兵
        </span>
        <span className="text-[10px] select-none" style={{ color: 'var(--text-muted)' }}>
          {sentinelHistory.length} 次采样
        </span>
      </div>

      {latestBreaches.length > 0 && (
        <div
          className="text-[10px] px-1.5 py-1 rounded mb-1.5 leading-relaxed"
          style={{ background: 'rgba(239, 68, 68, 0.12)', color: 'var(--error, #ef4444)' }}
          title={latestBreaches.join('\n')}
        >
          ⚠ 最新采样越阈值 {latestBreaches.length} 项，建议 dk-rebalance
        </div>
      )}

      {METRICS.map((def) => (
        <TrendPanel key={def.title} def={def} samples={sentinelHistory} />
      ))}
    </div>
  )
}

export default SentinelTrend
