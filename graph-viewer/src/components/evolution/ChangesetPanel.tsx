// ============================================================
// ChangesetPanel — 单次 changeset 事件流面板 / Changeset Event Panel
//
// 渲染选中 changeset 的 diff_json 事件流（合并/拆分/迁移/改名/内容/
// 增删 + 边增删），配色与 diff 着色图例一致；附版本信息与受影响文档。
// ============================================================

import { useEvolutionStore, DIFF_COLORS } from '../../stores/evolutionStore'
import type { EvolutionChangeset, NodeChangeJSON } from '../../types'
import { X } from 'lucide-react'

/** 单行节点事件的人读渲染（与 core/observe Markdown 的文案一致） */
function nodeLine(nc: NodeChangeJSON): string {
  const d = nc.detail as any
  switch (nc.change) {
    case 'merged_into':
      return `${nc.name} → 合并进 ${d?.into_name ?? ''} (${d?.into ?? '?'})`
    case 'split_into': {
      const names: string[] = d?.into_names ?? []
      const ids: string[] = d?.into ?? []
      const parts = ids.map((u, i) => `${names[i] ?? ''} (${u})`)
      return `${nc.name} → 拆分为 ${parts.join(', ')}`
    }
    case 'migrated':
      return `${nc.name}: ${d?.from_domain}/${d?.from_subdomain} → ${d?.to_domain}/${d?.to_subdomain}`
    case 'renamed':
      return `${d?.old_name ?? ''} → ${d?.new_name ?? nc.name} (${nc.id})`
    case 'content':
      return `${nc.name} (${(d?.fields ?? []).join(', ')})`
    default:
      return `${nc.name} (${nc.id})`
  }
}

interface Section {
  change: NodeChangeJSON['change']
  title: string
  color: string
}

const SECTIONS: Section[] = [
  { change: 'merged_into', title: '🟡 合并 / Merged', color: DIFF_COLORS.merged },
  { change: 'split_into', title: '🟡 拆分 / Split', color: DIFF_COLORS.split },
  { change: 'migrated', title: '🔵 迁移 / Migrated', color: DIFF_COLORS.migrated },
  { change: 'renamed', title: '🔵 改名 / Renamed', color: DIFF_COLORS.renamed },
  { change: 'content', title: '🟣 内容变更 / Content', color: DIFF_COLORS.content },
  { change: 'added', title: '🟢 新增 / Added', color: DIFF_COLORS.added },
  { change: 'deleted', title: '🔴 删除 / Deleted', color: DIFF_COLORS.deleted },
]

/** 版本行（parent → current） */
function VersionLine({ cs }: { cs: EvolutionChangeset }) {
  return (
    <div className="text-[11px]" style={{ color: 'var(--text-muted)' }}>
      {cs.parent_version ? (
        <>
          版本: {cs.parent_version} → {cs.kb_version}
        </>
      ) : (
        <>版本: {cs.kb_version}（首次记录）</>
      )}
    </div>
  )
}

export function ChangesetPanel() {
  const manifest = useEvolutionStore((s) => s.manifest)
  const selectedSeq = useEvolutionStore((s) => s.selectedSeq)
  const clearSelection = useEvolutionStore((s) => s.clearSelection)

  const cs = manifest?.changesets.find((c) => c.seq === selectedSeq)
  if (!cs) return null
  const diff = cs.diff

  return (
    <aside
      className="absolute top-24 bottom-3 right-3 w-[340px] rounded-xl flex flex-col overflow-hidden"
      data-testid="changeset-panel"
      style={{
        background: 'var(--bg-main)',
        border: '1px solid var(--border-default)',
        zIndex: 40,
      }}
    >
      {/* 头部 / Header */}
      <div
        className="px-3 py-2 border-b flex items-start justify-between shrink-0"
        style={{ borderColor: 'var(--border-default)' }}
      >
        <div className="min-w-0">
          <div className="text-sm font-semibold" style={{ color: 'var(--text-primary)' }}>
            Changeset #{cs.seq} · {cs.tool}
          </div>
          <div className="text-[11px] mt-0.5" style={{ color: 'var(--text-muted)' }}>
            {cs.ts}
          </div>
          <VersionLine cs={cs} />
          {cs.trigger_reason && (
            <div className="text-[11px] mt-0.5" style={{ color: 'var(--text-muted)' }}>
              触发: {cs.trigger_reason}
            </div>
          )}
        </div>
        <button
          onClick={clearSelection}
          className="p-1 rounded hover:opacity-70 shrink-0"
          style={{ color: 'var(--text-muted)' }}
          title="关闭事件流（返回当前图）"
        >
          <X size={14} />
        </button>
      </div>

      {/* 计数行 / Counts */}
      <div
        className="px-3 py-2 border-b text-[11px] shrink-0"
        style={{ borderColor: 'var(--border-default)', color: 'var(--text-secondary)' }}
      >
        节点 +{cs.n_added} -{cs.n_deleted} 合{cs.n_merged} 拆{cs.n_split} 迁{cs.n_migrated} 改
        {cs.n_renamed} 内容{cs.n_content} ｜ 边 +{cs.n_edge_added} -{cs.n_edge_dropped}
      </div>

      {/* 事件流 / Event stream */}
      <div className="flex-1 overflow-y-auto px-3 py-2 text-xs">
        {!diff && (
          <p style={{ color: 'var(--text-muted)' }}>（无 diff 数据）</p>
        )}
        {SECTIONS.map((sec) => {
          const items = (diff?.nodes ?? []).filter((n) => n.change === sec.change)
          if (items.length === 0) return null
          return (
            <div key={sec.change} className="mb-3">
              <div className="font-semibold mb-1" style={{ color: sec.color }}>
                {sec.title}（{items.length}）
              </div>
              {items.map((n) => (
                <div
                  key={`${sec.change}-${n.id}`}
                  className="pl-2 py-0.5 leading-snug break-all"
                  style={{ color: 'var(--text-secondary)' }}
                >
                  {nodeLine(n)}
                </div>
              ))}
            </div>
          )
        })}
        {diff && (diff.edges?.length ?? 0) > 0 && (
          <div className="mb-3">
            <div className="font-semibold mb-1" style={{ color: 'var(--text-primary)' }}>
              边变更 / Edges（+{cs.n_edge_added} -{cs.n_edge_dropped}）
            </div>
            {diff.edges.map((e, i) => (
              <div
                key={i}
                className="pl-2 py-0.5 leading-snug break-all"
                style={{
                  color: e.change === 'added' ? DIFF_COLORS.added : DIFF_COLORS.deleted,
                }}
              >
                {e.change === 'added' ? '＋' : '－'} {e.source_name || e.source} —{e.kind}→{' '}
                {e.target_name || e.target}
              </div>
            ))}
          </div>
        )}
        {cs.docs_affected && cs.docs_affected.length > 0 && (
          <div className="mb-2">
            <div className="font-semibold mb-1" style={{ color: 'var(--text-primary)' }}>
              受影响文档（{cs.docs_affected.length}）
            </div>
            {cs.docs_affected.map((d) => (
              <div
                key={d}
                className="pl-2 py-0.5 leading-snug break-all"
                style={{ color: 'var(--text-muted)' }}
              >
                {d}
              </div>
            ))}
          </div>
        )}
      </div>
    </aside>
  )
}
