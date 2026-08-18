import type { Event, Gate, Run, RunDetail, Stage } from '../api/client'
import { getRun } from '../api/client'
import type { EventPhase } from './events'
import { playSound } from './sound'
import { useRunsStore } from '../stores/runs'
import { useRunDetailsStore } from '../stores/runDetails'
import { useStreamsStore } from '../stores/streams'
import { useRunEventsStore } from '../stores/runEvents'
import { useInboxStore } from '../stores/inbox'

/*
  Роутинг событий журнала в zustand-сторы по kind (D-11, T-13/T-15):
    run.*    → runs + runDetails + runEvents (лента чата)
    stage.*  → runDetails + runEvents
    gate.*   → runDetails + inbox (глобальный инбокс) + runEvents
    stream.* → streams (буфер per stage); stream.usage — ещё и токены стадии
    system   → игнор (диагностика демона)

  F-03/F-01: новый ран от другого клиента (напр. TG-адаптера) прилетает
  событием run.created — payload это сериализованный Run, карточка
  строится из него без REST. Старые раны события run.created не имеют
  (созданы до F-01), для них остаётся fallback: на live run.*-событие
  с неизвестным run_id карточка догоняется REST'ом (ensureRunKnown).
  Только live: на replay начальный REST-снапшот списков уже всё покрыл,
  догоняться по каждому историческому событию — лишний fetch-шторм.
*/

/** защита от параллельных догонов одного и того же рана: runId → события, пришедшие за время полёта fetch */
const pendingRunFetches = new Map<string, Event[]>()

/**
 * Догон карточки рана, которого нет в runs-сторе (новый ран от другого клиента).
 * Пока летит fetch, события этого рана складываются в очередь: снапшот getRun
 * мог быть прочитан до коммита этих событий, поэтому после upsert снапшота
 * очередь реплеится через routeEvent — иначе карточка показывала бы устаревший
 * стейт до следующего события (fix-task-3 п.3). Реплей безопасен: сторы
 * идемпотентны, runEvents дедупит по id журнала.
 */
export function ensureRunKnown(runId: string, event?: Event): void {
  if (useRunsStore.getState().byId[runId] !== undefined) return
  const queued = pendingRunFetches.get(runId)
  if (queued !== undefined) {
    if (event !== undefined) queued.push(event)
    return
  }
  const deferred: Event[] = event !== undefined ? [event] : []
  pendingRunFetches.set(runId, deferred)
  getRun(runId)
    .then((run) => {
      useRunsStore.getState().upsertRun(run)
      useRunDetailsStore.getState().setDetail(run)
      for (const deferredEvent of deferred) routeEvent(deferredEvent, 'live')
    })
    .catch(() => undefined) // ран мог быть удалён/недоступен — следующее событие повторит попытку
    .finally(() => pendingRunFetches.delete(runId))
}

function str(value: unknown): string | undefined {
  return typeof value === 'string' ? value : undefined
}

function num(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}

/** Полиморфный payload события (см. EventPayload в спеке) — достаём поля с проверкой. */
function payloadOf(event: Event): Record<string, unknown> {
  return event.payload ?? {}
}

/**
 * Карточка рана из payload run.created (F-01, fix-task-4): форма совпадает
 * со схемой Run (спека, RunCreatedPayload). Возвращает null, если
 * обязательных полей нет — тогда сработает fallback-догон ensureRunKnown.
 */
export function runFromCreatedPayload(payload: Record<string, unknown>): Run | null {
  const id = str(payload.id)
  const projectId = num(payload.project_id)
  const pipelineVersionId = num(payload.pipeline_version_id)
  const taskText = str(payload.task_text)
  const baseBranch = str(payload.base_branch)
  const branch = str(payload.branch)
  const state = str(payload.state)
  const createdAt = str(payload.created_at)
  if (
    id === undefined ||
    projectId === undefined ||
    pipelineVersionId === undefined ||
    taskText === undefined ||
    baseBranch === undefined ||
    branch === undefined ||
    state === undefined ||
    createdAt === undefined
  ) {
    return null
  }
  return {
    id,
    project_id: projectId,
    pipeline_version_id: pipelineVersionId,
    task_text: taskText,
    base_branch: baseBranch,
    branch,
    state: state as Run['state'],
    depth: num(payload.depth) ?? 0,
    notify_tg: payload.notify_tg === true,
    created_at: createdAt,
  }
}

/**
 * Upsert карточки из run.created без REST: runs-стор всегда, runDetails —
 * только если детали ещё не загружены (REST-снапшот/live-деталь не затираем
 * пустым каркасом нового рана: stages/gates/artifacts/notes у него пусты).
 */
function applyRunCreated(payload: Record<string, unknown>): void {
  const run = runFromCreatedPayload(payload)
  if (run === null) return
  useRunsStore.getState().upsertRun(run)
  if (useRunDetailsStore.getState().byRunId[run.id] === undefined) {
    const detail: RunDetail = { ...run, stages: [], gates: [], artifacts: [], notes: [] }
    useRunDetailsStore.getState().setDetail(detail)
  }
}

function routeEvent(event: Event, phase: EventPhase): void {
  const kind = event.kind
  const payload = payloadOf(event)

  if (kind.startsWith('run.')) {
    // F-01: run.created несёт объект рана целиком — карточка без REST-догона
    if (kind === 'run.created') applyRunCreated(payload)
    // новый ран (другой клиент/TG-адаптер) без run.created: догоняем карточку (F-03, fallback)
    if (phase === 'live') ensureRunKnown(event.run_id, event)
    if (kind === 'run.state_changed') {
      const to = str(payload.to)
      if (to !== undefined) {
        useRunsStore.getState().applyStateChanged(event.run_id, to)
        useRunDetailsStore.getState().applyRunState(event.run_id, to)
      }
    }
    // run.branch_mismatch и прочие run.* — в ленту чата
    useRunEventsStore.getState().append(event.run_id, event)
    return
  }

  if (kind.startsWith('stage.')) {
    if (kind === 'stage.state_changed') {
      const to = str(payload.to)
      if (to !== undefined && event.stage_id != null) {
        // Новая попытка стадии (строка) приходит событием с данными стадии
        // в payload (stage_key/iteration, backend fix 2026-08-18) — вставляем
        // сразу, без перезагрузки страницы. Нет данных (старые события) и
        // стадия неизвестна — догоняем срез рана REST'ом.
        const detail = useRunDetailsStore.getState().byRunId[event.run_id]
        const known = detail?.stages.some((st) => st.id === event.stage_id) ?? false
        if (!known) {
          const stageKey = str(payload.stage_key)
          if (stageKey !== undefined) {
            useRunDetailsStore.getState().upsertStage(event.run_id, {
              id: event.stage_id,
              run_id: event.run_id,
              stage_key: stageKey,
              iteration: num(payload.iteration) ?? 1,
              state: to as Stage['state'],
              harness: str(payload.harness) ?? '',
              resume_count: 0,
              tokens_in: 0,
              tokens_out: 0,
            })
          } else {
            getRun(event.run_id)
              .then((run) => useRunDetailsStore.getState().setDetail(run))
              .catch(() => undefined)
          }
        }
        useRunDetailsStore.getState().applyStageState(event.run_id, event.stage_id, to)
      }
    } else if (kind === 'stage.resumed' && event.stage_id != null) {
      useRunDetailsStore.getState().applyStageResumed(event.run_id, event.stage_id)
    }
    useRunEventsStore.getState().append(event.run_id, event)
    return
  }

  if (kind.startsWith('gate.')) {
    const gateId = str(payload.gate_id)
    if (gateId === undefined) return
    if (kind === 'gate.opened') {
      // payload gate.opened — сериализованный объект гейта (спека, GatePayload)
      const gate: Gate = {
        id: gateId,
        run_id: event.run_id,
        stage_id: event.stage_id ?? null,
        kind: (str(payload.kind) ?? 'question') as Gate['kind'],
        question: str(payload.question) ?? '',
        context_json: str(payload.context_json) ?? '',
        state: 'open',
        created_at: event.ts,
      }
      useRunDetailsStore.getState().applyGateOpened(event.run_id, gate)
      useInboxStore.getState().addGate({
        gateId,
        runId: event.run_id,
        stageId: event.stage_id ?? null,
        kind: gate.kind,
        question: gate.question,
        openedAt: event.ts,
      })
    } else if (kind === 'gate.resolved') {
      const resolution = str(payload.resolution)
      if (resolution !== undefined) {
        useRunDetailsStore
          .getState()
          .applyGateResolved(event.run_id, gateId, resolution as Gate['state'], str(payload.answer) ?? null, event.ts)
      }
      useInboxStore.getState().removeGate(gateId)
    }
    useRunEventsStore.getState().append(event.run_id, event)
    return
  }

  if (kind.startsWith('stream.')) {
    if (event.stage_id != null) {
      if (kind === 'stream.usage') {
        // usage кумулятивный в рамках сессии — стор возьмёт max
        const tokensIn = num(payload.tokens_in)
        const tokensOut = num(payload.tokens_out)
        if (tokensIn !== undefined || tokensOut !== undefined) {
          useRunDetailsStore
            .getState()
            .applyStageUsage(event.run_id, event.stage_id, tokensIn ?? 0, tokensOut ?? 0)
        }
        return // usage в ленту стрима не кладём — он живёт в метриках
      }
      useStreamsStore.getState().append(event.stage_id, event)
    }
    return
  }

  // kind === 'system' и неизвестные — пропускаем
}

/**
 * Звук по событию (только live-фаза: replay исторического журнала при
 * старте не должен устраивать какофонию). Маппинг — см. lib/sound.ts.
 */
function soundForEvent(event: Event): void {
  const kind = event.kind
  const payload = payloadOf(event)
  switch (kind) {
    case 'stage.state_changed': {
      const to = str(payload.to)
      if (to === 'succeeded') playSound('success')
      else if (to === 'failed') playSound('error')
      break
    }
    case 'stream.error':
    case 'run.branch_mismatch':
      playSound('error')
      break
    case 'gate.opened':
      playSound('gate')
      break
    case 'stage.interrupted':
    case 'stage.resumed':
      playSound('warning')
      break
  }
}

/**
 * Приёмник для EventClient: replay приходит батчем, live — по одному.
 * Разница фаз важна вызывающему (атомарность replay), внутри сторов
 * события идемпотентны, поэтому применяются одинаково.
 */
export function dispatchEvents(events: Event[], phase: EventPhase): void {
  for (const event of events) routeEvent(event, phase)
  if (phase === 'live') for (const event of events) soundForEvent(event)
}
