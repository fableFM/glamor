import { useEffect, useState } from 'react'
import type { ProjectMetrics } from '../../api/client'
import { getProjectMetrics } from '../../api/client'
import { formatDuration, formatTokens } from '../../lib/format'

/*
  Статистика проекта (T-24): GET /projects/{id}/metrics?period=…
  Без чартов: итоги числами, by_state — bar-строки на div'ах.
*/

const PERIODS = ['24h', '7d', '30d', 'all'] as const
type Period = (typeof PERIODS)[number]

const STATE_ORDER = ['running', 'waiting_gate', 'succeeded', 'failed', 'stopped', 'draft']

export function ProjectMetricsPanel({ projectId }: { projectId: number }) {
  const [period, setPeriod] = useState<Period>('7d')
  const [metrics, setMetrics] = useState<ProjectMetrics | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    setError(null)
    getProjectMetrics(projectId, { period })
      .then((m) => {
        if (!cancelled) setMetrics(m)
      })
      .catch((err: unknown) => {
        if (!cancelled) setError(err instanceof Error ? err.message : 'ошибка загрузки метрик')
      })
    return () => {
      cancelled = true
    }
  }, [projectId, period])

  // известные состояния — в каноническом порядке, неизвестные — в конец по алфавиту
  const stateRank = (state: string) => {
    const index = STATE_ORDER.indexOf(state)
    return index === -1 ? 99 : index
  }
  const byState = Object.entries(metrics?.by_state ?? {}).sort(
    (a, b) => stateRank(a[0]) - stateRank(b[0]) || a[0].localeCompare(b[0]),
  )
  const maxCount = Math.max(1, ...byState.map(([, count]) => count))

  return (
    <div className="rounded-lg border border-zinc-800 bg-zinc-900/60 p-4">
      <div className="mb-3 flex items-center gap-1">
        {PERIODS.map((p) => (
          <button
            key={p}
            type="button"
            onClick={() => setPeriod(p)}
            className={`rounded-md px-2.5 py-1 text-xs ${
              period === p ? 'bg-zinc-800 text-zinc-100' : 'text-zinc-500 hover:bg-zinc-800/60 hover:text-zinc-300'
            }`}
          >
            {p}
          </button>
        ))}
      </div>

      {error && <p className="text-xs text-red-400">{error}</p>}
      {!metrics && !error && <p className="text-sm text-zinc-500">загрузка метрик…</p>}

      {metrics && (
        <>
          <div className="mb-4 grid grid-cols-4 gap-3">
            {(
              [
                ['ранов', String(metrics.runs_total)],
                ['tokens in', formatTokens(metrics.tokens_in)],
                ['tokens out', formatTokens(metrics.tokens_out)],
                [
                  'суммарная длительность',
                  metrics.total_duration_sec != null ? formatDuration(metrics.total_duration_sec * 1000) : '—',
                ],
              ] as [string, string][]
            ).map(([label, value]) => (
              <div key={label} className="rounded-md border border-zinc-800 bg-zinc-950/60 px-3 py-2">
                <div className="text-xs text-zinc-500">{label}</div>
                <div className="mt-0.5 font-mono text-sm text-zinc-100">{value}</div>
              </div>
            ))}
          </div>

          <div className="space-y-1.5">
            {byState.length === 0 && <p className="text-sm text-zinc-600">за период ранов нет</p>}
            {byState.map(([state, count]) => (
              <div key={state} className="flex items-center gap-3 text-xs">
                <span className="w-28 shrink-0 font-mono text-zinc-400">{state}</span>
                <div className="h-3.5 min-w-0 flex-1 rounded bg-zinc-800/60">
                  <div
                    className="h-full rounded bg-violet-500/60"
                    style={{ width: `${Math.max(2, (count / maxCount) * 100)}%` }}
                  />
                </div>
                <span className="w-10 shrink-0 text-right font-mono text-zinc-300">{count}</span>
              </div>
            ))}
          </div>
        </>
      )}
    </div>
  )
}
