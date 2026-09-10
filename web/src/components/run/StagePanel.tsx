import { useEffect, useState } from 'react'
import type { Artifact, Gate, RunMetrics, Stage } from '../../api/client'
import { getRunMetrics } from '../../api/client'
import { useStreamsStore } from '../../stores/streams'
import { StreamView } from './StreamView'
import { ArtifactContent } from './ArtifactContent'
import { GateView } from './GateView'
import { StageStateBadge } from '../StateBadge'
import { durationBetween, formatDateTime, formatDuration, formatTokens } from '../../lib/format'
import { artifactFileName, latestPromptArtifact, promptArtifactsForStage } from '../../lib/artifacts'

/*
  Панель этапа (T-14): табы [Стрим] [Артефакты] [Промпт] [Метрики] +
  гейт-таб (❓/✋/⚠), когда у стадии открытый гейт — гейт-вью (GateView).
  Промпт лежит файлом prompt-<stage>-<iter>.md (артефакт kind="prompt").
  Содержимое артефактов читается через GET …/artifacts/{id}/content
  (F-02, fix-task-4) — раскрытие строки грузит и рендерит файл
  (ArtifactContent), метаданные (путь, итерация, время) — над ним.
*/

type TabId = 'stream' | 'artifacts' | 'prompt' | 'metrics'

const TABS: { id: TabId; label: string }[] = [
  { id: 'stream', label: 'Стрим' },
  { id: 'artifacts', label: 'Артефакты' },
  { id: 'prompt', label: 'Промпт' },
  { id: 'metrics', label: 'Метрики' },
]

/** Раскрывающиеся детали артефакта (общий блок для табов Артефакты/Промпт). */
function ArtifactDetails({ artifact }: { artifact: Artifact }) {
  return (
    <div className="mt-1 space-y-2 rounded-md border border-zinc-800 bg-zinc-950 px-3 py-2">
      <p className="break-all font-mono text-xs text-zinc-400">{artifact.path}</p>
      <p className="text-xs text-zinc-600">
        kind: {artifact.kind} · {formatDateTime(artifact.created_at)}
      </p>
      <ArtifactContent artifact={artifact} />
    </div>
  )
}

function ArtifactsTab({ artifacts }: { artifacts: Artifact[] }) {
  const [expandedId, setExpandedId] = useState<number | null>(null)
  if (artifacts.length === 0) {
    return <p className="p-4 text-sm text-zinc-600">артефактов нет</p>
  }
  return (
    <ul className="divide-y divide-zinc-800/60">
      {artifacts.map((artifact) => (
        <li key={artifact.id} className="px-4 py-2 text-sm">
          <button
            type="button"
            onClick={() => setExpandedId((id) => (id === artifact.id ? null : artifact.id))}
            className="flex w-full items-center gap-3 text-left"
          >
            <span className="rounded bg-zinc-800 px-1.5 py-0.5 text-xs text-zinc-400">{artifact.kind}</span>
            <span className="min-w-0 flex-1 truncate font-mono text-xs text-zinc-300">
              {artifactFileName(artifact.path)}
            </span>
            <span className="shrink-0 text-xs text-zinc-600">{formatDateTime(artifact.created_at)}</span>
          </button>
          {expandedId === artifact.id && <ArtifactDetails artifact={artifact} />}
        </li>
      ))}
    </ul>
  )
}

/** Таб «Промпт»: prompt-артефакты стадии (все попытки), актуальная — последняя. */
function PromptTab({ stage, artifacts }: { stage: Stage; artifacts: Artifact[] }) {
  const [expandedId, setExpandedId] = useState<number | null>(null)
  const prompts = promptArtifactsForStage(artifacts, stage.id, stage.stage_key)
  const latest = latestPromptArtifact(artifacts, stage.id, stage.stage_key)

  if (prompts.length === 0) {
    return (
      <p className="p-4 text-sm text-zinc-600">
        промпт этапа недоступен (prompt-{stage.stage_key}-&lt;iter&gt;.md не найден в артефактах)
      </p>
    )
  }
  return (
    <div className="p-4">
      <p className="mb-2 text-xs text-zinc-500">
        отрендеренный промпт попытки сохраняется артефактом рана; попыток: {prompts.length}
        {latest?.iteration != null && `, актуальная — #${latest.iteration}`}
      </p>
      <ul className="space-y-1">
        {prompts.map(({ artifact, iteration }) => (
          <li key={artifact.id}>
            <button
              type="button"
              onClick={() => setExpandedId((id) => (id === artifact.id ? null : artifact.id))}
              className="flex w-full items-center gap-3 text-left text-sm"
            >
              <span className="rounded bg-violet-500/20 px-1.5 py-0.5 text-xs text-violet-300">
                {iteration != null ? `попытка ${iteration}` : 'prompt'}
              </span>
              <span className="min-w-0 flex-1 truncate font-mono text-xs text-zinc-300">
                {artifactFileName(artifact.path)}
              </span>
              <span className="shrink-0 text-xs text-zinc-600">{formatDateTime(artifact.created_at)}</span>
            </button>
            {expandedId === artifact.id && <ArtifactDetails artifact={artifact} />}
          </li>
        ))}
      </ul>
    </div>
  )
}

/** токены: 0 = harness не сообщил usage (спека) — показываем «—» */
function tokensOrDash(count: number): string {
  return count > 0 ? formatTokens(count) : '—'
}

/**
 * Крупный блок причины падения этапа (БАГ 2): stage.error + tail стрима
 * (последние ~20 событий: text усечённо, error целиком) — виден на любом табе.
 */
function StageErrorBanner({ stage }: { stage: Stage }) {
  const events = useStreamsStore((s) => s.byStageId[stage.id])
  if (!stage.error) return null

  const tail = (events ?? [])
    .filter((e) => e.kind === 'stream.text' || e.kind === 'stream.error')
    .slice(-20)

  return (
    <div className="border-b border-red-500/40 bg-red-500/10 px-4 py-2">
      <p className="text-sm text-red-300">
        <span className="font-semibold text-red-400">
          ошибка этапа{stage.exit_code != null ? ` (exit ${stage.exit_code})` : ''}:
        </span>{' '}
        {stage.error}
      </p>
      {tail.length > 0 && (
        <details className="mt-1">
          <summary className="cursor-pointer text-xs text-red-400/80">последние события стрима ({tail.length})</summary>
          <div className="mt-1 max-h-48 overflow-auto rounded bg-zinc-950/60 p-2 font-mono text-xs">
            {tail.map((event) => {
              const payload = event.payload ?? {}
              const text = typeof payload.text === 'string' ? payload.text : typeof payload.message === 'string' ? payload.message : ''
              const line = text.trim()
              if (!line) return null
              return (
                <div key={event.id} className={`wrap-anywhere ${event.kind === 'stream.error' ? 'text-red-400' : 'text-zinc-500'}`}>
                  {line.length > 300 ? `${line.slice(0, 300)}…` : line}
                </div>
              )
            })}
          </div>
        </details>
      )}
    </div>
  )
}

/**
 * Таб «Метрики» (T-24): попытки этапа из GET /runs/{id}/metrics
 * (duration/tokens per попытка, resume_count, exit_code) + session_id
 * и ошибка из снапшота стадии. Перечитываем при смене состояния этапа.
 */
function MetricsTab({ runId, stage }: { runId: string; stage: Stage }) {
  const [metrics, setMetrics] = useState<RunMetrics | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    getRunMetrics(runId)
      .then((m) => {
        if (!cancelled) setMetrics(m)
      })
      .catch((err: unknown) => {
        if (!cancelled) setError(err instanceof Error ? err.message : 'ошибка загрузки метрик')
      })
    return () => {
      cancelled = true
    }
  }, [runId, stage.state])

  // попытки этого этапа: в метриках запись на (stage_key, iteration)
  const attempts = (metrics?.stages ?? []).filter((s) => s.stage_key === stage.stage_key)

  return (
    <div className="p-4">
      {error && <p className="mb-2 text-xs text-red-400">{error}</p>}
      {attempts.length > 0 ? (
        <table className="w-full text-xs">
          <thead>
            <tr className="text-left text-zinc-600">
              <th className="pb-1.5 pr-3 font-medium">попытка</th>
              <th className="pb-1.5 pr-3 font-medium">состояние</th>
              <th className="pb-1.5 pr-3 font-medium">длительность</th>
              <th className="pb-1.5 pr-3 font-medium">tokens in</th>
              <th className="pb-1.5 pr-3 font-medium">tokens out</th>
              <th className="pb-1.5 pr-3 font-medium">resume</th>
              <th className="pb-1.5 font-medium">exit</th>
            </tr>
          </thead>
          <tbody className="text-zinc-300">
            {attempts.map((attempt) => (
              <tr key={`${attempt.stage_id}-${attempt.iteration}`} className="border-t border-zinc-800/60">
                <td className="py-1.5 pr-3 font-mono">#{attempt.iteration}</td>
                <td className="py-1.5 pr-3">{attempt.state}</td>
                <td className="py-1.5 pr-3">
                  {attempt.duration_sec != null ? formatDuration(attempt.duration_sec * 1000) : '—'}
                </td>
                <td className="py-1.5 pr-3 font-mono">{tokensOrDash(attempt.tokens_in)}</td>
                <td className="py-1.5 pr-3 font-mono">{tokensOrDash(attempt.tokens_out)}</td>
                <td className="py-1.5 pr-3">{attempt.resume_count}</td>
                <td className="py-1.5">{attempt.exit_code != null ? attempt.exit_code : '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : (
        !error && <p className="text-sm text-zinc-600">метрик пока нет</p>
      )}
      <dl className="mt-3 space-y-1.5 border-t border-zinc-800 pt-3 text-sm">
        <div className="flex gap-3">
          <dt className="w-32 shrink-0 text-zinc-500">harness</dt>
          <dd className="font-mono text-xs leading-5 text-zinc-300">{stage.harness || '—'}</dd>
        </div>
        <div className="flex gap-3">
          <dt className="w-32 shrink-0 text-zinc-500">session_id</dt>
          <dd className="min-w-0 flex-1 truncate font-mono text-xs leading-5 text-zinc-300">{stage.session_id ?? '—'}</dd>
        </div>
        <div className="flex gap-3">
          <dt className="w-32 shrink-0 text-zinc-500">начат / завершён</dt>
          <dd className="font-mono text-xs leading-5 text-zinc-300">
            {formatDateTime(stage.started_at)} → {formatDateTime(stage.finished_at)} (
            {durationBetween(stage.started_at, stage.finished_at) ?? '—'})
          </dd>
        </div>
      </dl>
      {stage.error && (
        <div className="mt-2 rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
          {stage.error}
        </div>
      )}
    </div>
  )
}

export function StagePanel({
  stage,
  artifacts,
  runId,
  openGate,
  gateViewOn,
  onGateViewChange,
}: {
  stage: Stage | null
  artifacts: Artifact[]
  runId: string
  /** открытый гейт выбранной стадии (если есть) — появляется гейт-таб */
  openGate: Gate | null
  /** внешнее управление гейт-табом (клик по ноде/бейджу, «Открыть» из чата, хоткей g) */
  gateViewOn: boolean
  onGateViewChange: (on: boolean) => void
}) {
  const [tab, setTab] = useState<TabId>('stream')

  if (!stage) {
    return (
      <div className="flex h-full items-center justify-center text-sm text-zinc-600">
        выберите этап слева (j/k — навигация)
      </div>
    )
  }

  // гейт-таб активен, пока гейт открыт и его не закрыли (✕); резолв → назад на Стрим
  const gateTabActive = openGate !== null && gateViewOn
  const effectiveTab: TabId | 'gate' = gateTabActive ? 'gate' : tab

  const gateTabLabel =
    openGate === null
      ? ''
      : openGate.kind === 'question'
        ? '❓ Вопросы'
        : openGate.kind === 'escalation'
          ? '⚠ Эскалация'
          : '✋ Гейт'

  // артефакты стадии + артефакты уровня рана (stage_id null) показываем отдельно
  const stageArtifacts = artifacts.filter((a) => a.stage_id === stage.id)

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex items-center gap-3 border-b border-zinc-800 px-4 py-2">
        <span className="font-mono text-sm font-medium text-zinc-200">{stage.stage_key}</span>
        <StageStateBadge state={stage.state} />
        {stage.error && <span className="text-xs text-red-400">есть ошибка — см. баннер ниже</span>}
        <div className="ml-auto flex gap-1">
          {openGate !== null && (
            <button
              type="button"
              onClick={() => onGateViewChange(!gateTabActive)}
              className={`flex items-center gap-1.5 rounded-md px-2.5 py-1 text-xs ${
                gateTabActive
                  ? 'bg-amber-500/20 text-amber-200'
                  : 'text-amber-300/80 hover:bg-amber-500/10'
              }`}
            >
              {/* янтарная точка, пока гейт открыт */}
              <span className="size-1.5 animate-pulse rounded-full bg-amber-400" aria-hidden />
              {gateTabLabel}
            </button>
          )}
          {TABS.map((t) => (
            <button
              key={t.id}
              type="button"
              onClick={() => {
                setTab(t.id)
                onGateViewChange(false) // ручной уход с гейт-таба
              }}
              className={`rounded-md px-2.5 py-1 text-xs ${
                effectiveTab === t.id ? 'bg-zinc-800 text-zinc-100' : 'text-zinc-500 hover:bg-zinc-800/60 hover:text-zinc-300'
              }`}
            >
              {t.label}
            </button>
          ))}
        </div>
      </div>
      {/* причина падения крупно, на любом табе (БАГ 2) */}
      <StageErrorBanner stage={stage} />
      {/* стрим виртуализирован и скроллится сам (Virtuoso), остальные табы — обычный overflow */}
      <div className={`min-h-0 flex-1 ${effectiveTab === 'stream' || effectiveTab === 'gate' ? '' : 'overflow-auto'}`}>
        {gateTabActive && openGate !== null ? (
          <GateView
            key={openGate.id}
            gate={openGate}
            artifacts={artifacts}
            onResolved={() => onGateViewChange(false)}
            onClose={() => onGateViewChange(false)}
          />
        ) : (
          <>
            {/* key={stage.id}: перемонтирование при смене стадии — Virtuoso заново
                применяет initialTopMostItemIndex и лента открывается с конца (fix-task-3 п.1) */}
            {effectiveTab === 'stream' && (
              <StreamView key={stage.id} stageId={stage.id} stageRunning={stage.state === 'running'} />
            )}
            {effectiveTab === 'artifacts' && <ArtifactsTab artifacts={stageArtifacts} />}
            {effectiveTab === 'prompt' && <PromptTab stage={stage} artifacts={artifacts} />}
            {effectiveTab === 'metrics' && <MetricsTab runId={runId} stage={stage} />}
          </>
        )}
      </div>
    </div>
  )
}
