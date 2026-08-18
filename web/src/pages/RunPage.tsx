import { useCallback, useEffect, useMemo, useState } from 'react'
import { getRouteApi, Link, useNavigate } from '@tanstack/react-router'
import { ArrowLeft, Download, Play, Square } from 'lucide-react'
import type { Event, Gate, RunMetrics } from '../api/client'
import { getRun, getRunMetrics, getRunMetricsCsv, listRunEvents, resumeRun, stopRun } from '../api/client'
import { useRunDetailsStore } from '../stores/runDetails'
import { useRunsStore } from '../stores/runs'
import { useProjectsStore } from '../stores/projects'
import { useStreamsStore } from '../stores/streams'
import { useRunEventsStore } from '../stores/runEvents'
import { RunStateBadge } from '../components/StateBadge'
import { StageGraph } from '../components/run/StageGraph'
import { StagePanel } from '../components/run/StagePanel'
import { GateChat } from '../components/run/GateChat'
import { firstLine, formatDuration, formatTokens } from '../lib/format'

const routeApi = getRouteApi('/runs/$id')

const EVENTS_PAGE_LIMIT = 500

/** REST-догон журнала рана: stream.* → streams-стор, доменные события → лента чата (T-14/T-15). */
async function backfillRunEvents(runId: string, signal: AbortSignal): Promise<void> {
  const streams: Record<number, Event[]> = {}
  const domainEvents: Event[] = []
  let afterId = 0
  for (;;) {
    const page = await listRunEvents(runId, { after_id: afterId, limit: EVENTS_PAGE_LIMIT })
    if (signal.aborted) return
    if (page.length === 0) break
    for (const event of page) {
      if (event.kind.startsWith('stream.') && event.kind !== 'stream.usage' && event.stage_id != null) {
        ;(streams[event.stage_id] ??= []).push(event)
      } else if (!event.kind.startsWith('stream.')) {
        domainEvents.push(event)
      }
      afterId = Math.max(afterId, event.id)
    }
    if (page.length < EVENTS_PAGE_LIMIT) break
  }
  const streamsStore = useStreamsStore.getState()
  for (const [stageId, events] of Object.entries(streams)) {
    streamsStore.mergeHistory(Number(stageId), events)
  }
  useRunEventsStore.getState().mergeHistory(runId, domainEvents)
}

/** Экран рана (T-14 + чат T-15): header, граф этапов, панель этапа, чат гейтов. */
export function RunPage() {
  const { id } = routeApi.useParams()
  const detail = useRunDetailsStore((s) => s.byRunId[id])
  const projects = useProjectsStore((s) => s.items)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [backfillError, setBackfillError] = useState<string | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)
  const [selectedStageId, setSelectedStageId] = useState<number | null>(null)
  const [actionBusy, setActionBusy] = useState(false)
  /** итоговые метрики рана (T-24): токены + суммарное ожидание гейтов в header */
  const [metrics, setMetrics] = useState<RunMetrics | null>(null)

  // REST-снапшот + догон журнала; live — через общую WS-подписку (dispatch)
  useEffect(() => {
    const abort = new AbortController()
    setLoadError(null)
    getRun(id)
      .then((fresh) => {
        if (abort.signal.aborted) return
        useRunDetailsStore.getState().setDetail(fresh)
        useRunsStore.getState().upsertRun(fresh)
      })
      .catch((error: unknown) => {
        if (!abort.signal.aborted) setLoadError(error instanceof Error ? error.message : 'ошибка загрузки')
      })
    setBackfillError(null)
    backfillRunEvents(id, abort.signal).catch((error: unknown) => {
      // фоновый догон: не блокируем экран, но и не глотаем молча
      console.error('backfillRunEvents failed', error)
      if (!abort.signal.aborted) {
        setBackfillError(error instanceof Error ? error.message : 'ошибка догона журнала')
      }
    })
    return () => abort.abort()
  }, [id])

  // метрики рана: при монтировании и при смене состояния (после финиша — финальные цифры)
  const runState = detail?.state
  useEffect(() => {
    let cancelled = false
    getRunMetrics(id)
      .then((m) => {
        if (!cancelled) setMetrics(m)
      })
      .catch(() => undefined) // метрики — вторичны, экран не блокируем
    return () => {
      cancelled = true
    }
  }, [id, runState])

  const stages = useMemo(() => detail?.stages ?? [], [detail])

  // стадия по умолчанию: running → последняя по списку (самая свежая
  // попытка/этап — запрос пользователя 2026-08-18), иначе первая
  const effectiveSelectedId =
    selectedStageId ??
    stages.find((s) => s.state === 'running')?.id ??
    stages[stages.length - 1]?.id ??
    null
  const selectedStage = stages.find((s) => s.id === effectiveSelectedId) ?? null

  // открытый гейт выбранной стадии (гейт-таб в панели этапа)
  const selectedStageGate =
    detail?.gates.find((g) => g.state === 'open' && g.stage_id === effectiveSelectedId) ?? null
  const [gateViewOn, setGateViewOn] = useState(false)

  // смена стадии: если у неё открытый гейт — сразу показываем гейт-вью
  useEffect(() => {
    setGateViewOn(selectedStageGate !== null)
    // намеренно только по смене стадии: резолв гейта не должен дёргать таб
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [effectiveSelectedId])

  /** «Открыть» из чата/хоткей: выбрать стадию гейта и включить гейт-вью */
  const openGateView = useCallback(
    (gate: Gate) => {
      if (gate.stage_id != null) setSelectedStageId(gate.stage_id)
      setGateViewOn(true)
    },
    [],
  )

  // keyboard: j/k — навигация по этапам (D-64)
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      const target = event.target as HTMLElement | null
      if (target && (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.isContentEditable)) return
      if (event.key !== 'j' && event.key !== 'k') return
      if (stages.length === 0) return
      const index = stages.findIndex((s) => s.id === effectiveSelectedId)
      const next = event.key === 'j' ? Math.min(stages.length - 1, index + 1) : Math.max(0, index <= 0 ? 0 : index - 1)
      setSelectedStageId(stages[next].id)
    }
    document.addEventListener('keydown', onKeyDown)
    return () => document.removeEventListener('keydown', onKeyDown)
  }, [stages, effectiveSelectedId])

  // keyboard: g — открыть/закрыть гейт-вью активного (первого открытого) гейта
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      const target = event.target as HTMLElement | null
      if (target && (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.isContentEditable)) return
      if (event.key !== 'g' || !detail) return
      if (gateViewOn) {
        setGateViewOn(false)
        return
      }
      // активный гейт: у выбранной стадии, иначе первый открытый по рану
      const gate = selectedStageGate ?? detail.gates.find((g) => g.state === 'open') ?? null
      if (gate) openGateView(gate)
    }
    document.addEventListener('keydown', onKeyDown)
    return () => document.removeEventListener('keydown', onKeyDown)
  }, [detail, gateViewOn, selectedStageGate, openGateView])

  // keyboard: u / Escape — назад к задачам проекта (вне полей ввода)
  const navigate = useNavigate()
  const backProjectId = detail?.project_id
  useEffect(() => {
    if (backProjectId === undefined) return
    const onKeyDown = (event: KeyboardEvent) => {
      const target = event.target as HTMLElement | null
      if (target && (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.isContentEditable)) return
      if (event.key !== 'u' && event.key !== 'Escape') return
      void navigate({ to: '/projects/$id', params: { id: String(backProjectId) } })
    }
    document.addEventListener('keydown', onKeyDown)
    return () => document.removeEventListener('keydown', onKeyDown)
  }, [backProjectId, navigate])

  const tokensIn = stages.reduce((sum, s) => sum + s.tokens_in, 0)
  const tokensOut = stages.reduce((sum, s) => sum + s.tokens_out, 0)
  const project = detail ? projects.find((p) => p.id === detail.project_id) : undefined

  const doStop = useCallback(() => {
    if (!detail || actionBusy) return
    if (!window.confirm('Остановить ран? Текущий этап будет прерван.')) return
    setActionBusy(true)
    setActionError(null)
    stopRun(detail.id, crypto.randomUUID())
      .then((run) => {
        useRunsStore.getState().upsertRun(run)
        useRunDetailsStore.getState().applyRunState(run.id, run.state)
        // стадии/гейты после остановки — полный срез
        return getRun(run.id).then((fresh) => useRunDetailsStore.getState().setDetail(fresh))
      })
      .catch((error: unknown) => setActionError(error instanceof Error ? error.message : 'ошибка остановки'))
      .finally(() => setActionBusy(false))
  }, [detail, actionBusy])

  const doResume = useCallback(() => {
    if (!detail || actionBusy) return
    setActionBusy(true)
    setActionError(null)
    resumeRun(detail.id, crypto.randomUUID())
      .then((run) => {
        useRunsStore.getState().upsertRun(run)
        useRunDetailsStore.getState().applyRunState(run.id, run.state)
      })
      .catch((error: unknown) => setActionError(error instanceof Error ? error.message : 'ошибка возобновления'))
      .finally(() => setActionBusy(false))
  }, [detail, actionBusy])

  /** экспорт метрик рана в CSV (T-24): blob → скачивание */
  const doExportCsv = useCallback(() => {
    if (!detail) return
    getRunMetricsCsv(detail.id)
      .then((csv) => {
        const blob = new Blob([csv], { type: 'text/csv' })
        const url = URL.createObjectURL(blob)
        const link = document.createElement('a')
        link.href = url
        link.download = `run-${detail.id.slice(0, 8)}-metrics.csv`
        link.click()
        URL.revokeObjectURL(url)
      })
      .catch((error: unknown) => setActionError(error instanceof Error ? error.message : 'ошибка экспорта CSV'))
  }, [detail])

  if (loadError && !detail) {
    return <p className="text-sm text-red-400">не удалось загрузить ран: {loadError}</p>
  }
  if (!detail) {
    return <p className="text-sm text-zinc-500">загрузка рана…</p>
  }

  const runActive = detail.state === 'running' || detail.state === 'waiting_gate' || detail.state === 'draft'

  return (
    <div className="flex h-full min-h-0 flex-col gap-3">
      {/* крошки + назад: Проекты / <проект> / Ран (u или Esc — тоже назад) */}
      <nav className="flex items-center gap-1.5 text-xs text-zinc-500">
        <Link
          to="/projects/$id"
          params={{ id: String(detail.project_id) }}
          className="flex items-center gap-1 rounded-md border border-zinc-800 px-2 py-1 hover:bg-zinc-800 hover:text-zinc-200"
          title="горячая клавиша: u (или Esc)"
        >
          <ArrowLeft className="size-3.5" aria-hidden /> К задачам
        </Link>
        <Link to="/" className="hover:text-zinc-300">
          Проекты
        </Link>
        <span>/</span>
        <Link to="/projects/$id" params={{ id: String(detail.project_id) }} className="hover:text-zinc-300">
          {project?.name ?? `проект #${detail.project_id}`}
        </Link>
        <span>/</span>
        <span className="text-zinc-400">ран {detail.id.slice(0, 8)}</span>
      </nav>

      {/* header: задача · проект · ветка · состояние · токены · действия */}
      <header className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-lg border border-zinc-800 bg-zinc-900/60 px-4 py-2.5">
        <h1 className="max-w-xl truncate text-sm font-semibold text-zinc-100" title={detail.task_text}>
          {firstLine(detail.task_text, 100) || '(без текста задачи)'}
        </h1>
        {project && (
          <Link
            to="/projects/$id"
            params={{ id: String(project.id) }}
            className="text-xs text-violet-400 hover:underline"
          >
            {project.name}
          </Link>
        )}
        <span className="rounded bg-zinc-800 px-1.5 py-0.5 font-mono text-xs text-zinc-400">{detail.branch}</span>
        <RunStateBadge state={detail.state} />
        <span className="text-xs text-zinc-500" title={`in ${tokensIn} / out ${tokensOut}`}>
          ⧉ {formatTokens(metrics ? metrics.totals.tokens_in + metrics.totals.tokens_out : tokensIn + tokensOut)} tok
        </span>
        {metrics && metrics.gate_wait_seconds > 0 && (
          <span className="text-xs text-amber-300/80" title="суммарное ожидание гейтов">
            ⏳ {formatDuration(metrics.gate_wait_seconds * 1000)}
          </span>
        )}
        <span className="ml-auto flex gap-2">
          <button
            type="button"
            onClick={doExportCsv}
            title="метрики рана в CSV (T-24)"
            className="flex items-center gap-1 rounded-md border border-zinc-700 px-2.5 py-1 text-xs text-zinc-400 hover:bg-zinc-800 hover:text-zinc-200"
          >
            <Download className="size-3" aria-hidden /> CSV
          </button>
          {runActive && (
            <button
              type="button"
              onClick={doStop}
              disabled={actionBusy}
              className="flex items-center gap-1 rounded-md bg-red-600/70 px-2.5 py-1 text-xs font-medium text-red-50 hover:bg-red-600 disabled:opacity-50"
            >
              <Square className="size-3" aria-hidden /> stop
            </button>
          )}
          {(detail.state === 'failed' || detail.state === 'stopped') && (
            <button
              type="button"
              onClick={doResume}
              disabled={actionBusy}
              className="flex items-center gap-1 rounded-md bg-emerald-600/80 px-2.5 py-1 text-xs font-medium text-emerald-50 hover:bg-emerald-600 disabled:opacity-50"
            >
              <Play className="size-3" aria-hidden /> resume
            </button>
          )}
        </span>
      </header>

      {/* причина падения (БАГ 2): error последней failed-стадии крупно на экране рана */}
      {detail.state === 'failed' &&
        (() => {
          const failedStage = [...stages].reverse().find((s) => s.state === 'failed' && s.error)
          return failedStage ? (
            <button
              type="button"
              onClick={() => setSelectedStageId(failedStage.id)}
              className="flex items-start gap-2 rounded-lg border border-red-500/40 bg-red-500/10 px-4 py-2 text-left"
              title="перейти к упавшему этапу"
            >
              <span className="mt-0.5 text-xs font-semibold uppercase text-red-400">
                этап {failedStage.stage_key} упал
                {failedStage.exit_code != null ? ` (exit ${failedStage.exit_code})` : ''}:
              </span>
              <span className="min-w-0 flex-1 text-sm text-red-300">{failedStage.error}</span>
            </button>
          ) : null
        })()}

      {/* ошибки действий/фонового догона — не молчим (fix-task-2 П.3) */}
      {actionError && <p className="text-sm text-red-400">{actionError}</p>}
      {backfillError && (
        <p className="text-xs text-amber-500/80">
          догон журнала не удался: {backfillError} — лента может быть неполной, поможет перезагрузка страницы
        </p>
      )}

      {/* граф этапов + панель этапа */}
      <div className="flex min-h-0 flex-1 gap-3">
        <div className="w-72 shrink-0 rounded-lg border border-zinc-800 bg-zinc-900/40">
          <StageGraph
            stages={stages}
            gates={detail.gates}
            selectedStageId={effectiveSelectedId}
            onSelect={setSelectedStageId}
          />
        </div>
        <div className="min-w-0 flex-1 rounded-lg border border-zinc-800 bg-zinc-900/40">
          <StagePanel
            stage={selectedStage}
            artifacts={detail.artifacts}
            runId={detail.id}
            openGate={selectedStageGate}
            gateViewOn={gateViewOn}
            onGateViewChange={setGateViewOn}
          />
        </div>
      </div>

      {/* чат с пайплайном (T-15) */}
      <div className="h-64 shrink-0 rounded-lg border border-zinc-800 bg-zinc-900/40">
        <GateChat detail={detail} onOpenGateView={openGateView} />
      </div>
    </div>
  )
}
