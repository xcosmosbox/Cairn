// ============================================================
// evolution-api — 演化后端 API 客户端 / Evolution Backend API Client
//
// 对接 dk-evolve-serve（E-4，默认 127.0.0.1:7801）：
//   GET /api/manifest → 时间轴 + 曲线数据一次拉齐。
// 快照 db 文件不走本模块——由 DBLoader.loadFromURL 直接 fetch
// /snapshots/<file>（前端 sql.js 加载，性能优先）。
// ============================================================

import type { EvolutionManifest } from '../types'

/** 默认后端地址（dk-evolve-serve 的默认监听） */
export const EVOLUTION_DEFAULT_BASE_URL = 'http://127.0.0.1:7801'

/**
 * 拉取演化 manifest（changesets + metrics）。
 *
 * 缺省安全：后端未启动 / evolution.db 不存在 / 格式非法时抛错，
 * 由调用方（evolutionStore.connect）落入 error 状态——演化模式
 * 降级为不可用，绝不影响既有单快照上传模式。
 *
 * Fetch the evolution manifest. Throws on any failure; callers
 * degrade evolution mode gracefully.
 */
export async function fetchManifest(baseUrl: string): Promise<EvolutionManifest> {
  const base = baseUrl.replace(/\/+$/, '')
  const resp = await fetch(`${base}/api/manifest`)
  if (!resp.ok) {
    throw new Error(`manifest 请求失败 / manifest HTTP ${resp.status}`)
  }
  const m = (await resp.json()) as EvolutionManifest
  if (!m || !Array.isArray(m.changesets) || !Array.isArray(m.metrics)) {
    throw new Error('manifest 格式非法 / invalid manifest shape')
  }
  return m
}

/** 拼快照文件 URL（DBLoader.loadFromURL 的入参）。 */
export function snapshotURL(baseUrl: string, snapshotFile: string): string {
  const base = baseUrl.replace(/\/+$/, '')
  return `${base}/snapshots/${snapshotFile}`
}
