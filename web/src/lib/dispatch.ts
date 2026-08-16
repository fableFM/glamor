import type { Event, Gate } from '../api/client'
import type { EventPhase } from './events'
import { useRunsStore } from '../stores/runs'
import { useRunDetailsStore } from '../stores/runDetails'
import { useStreamsStore } from '../stores/streams'

/*
  Роутинг событий журнала в zustand-сторы по kind (D-11, T-13):
    run.*    → runs + runDetails
    stage.*  → runDetails
    gate.*   → runDetails
    stream.* → streams (буфер per stage)
    system   → игнор (диагностика демона)
*/

function str(value: unknown): string | undefined {
  return typeof value === 'string' ? value : undefined
}

/** Полиморфный payload события (см. EventPayload в спеке) — достаём поля с проверкой. */
function payloadOf(event: Event): Record<string, unknown> {
  return event.payload ?? {}
}

function routeEvent(event: Event): void {
  const kind = event.kind
  const payload = payloadOf(event)

  if (kind.startsWith('run.')) {
    if (kind === 'run.state_changed') {
      const to = str(payload.to)
      if (to !== undefined) {
        useRunsStore.getState().applyStateChanged(event.run_id, to)
        useRunDetailsStore.getState().applyRunState(event.run_id, to)
      }
    }
    return
  }

  if (kind.startsWith('stage.')) {
    if (kind === 'stage.state_changed') {
      const to = str(payload.to)
      if (to !== undefined && event.stage_id != null) {
        useRunDetailsStore.getState().applyStageState(event.run_id, event.stage_id, to)
      }
    }
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
    } else if (kind === 'gate.resolved') {
      const resolution = str(payload.resolution)
      if (resolution !== undefined) {
        useRunDetailsStore
          .getState()
          .applyGateResolved(event.run_id, gateId, resolution as Gate['state'], str(payload.answer) ?? null, event.ts)
      }
    }
    return
  }

  if (kind.startsWith('stream.')) {
    if (event.stage_id != null) {
      useStreamsStore.getState().append(event.stage_id, event)
    }
    return
  }

  // kind === 'system' и неизвестные — пропускаем
}

/**
 * Приёмник для EventClient: replay приходит батчем, live — по одному.
 * Разница фаз важна вызывающему (атомарность replay), внутри сторов
 * события идемпотентны, поэтому применяются одинаково.
 */
export function dispatchEvents(events: Event[], _phase: EventPhase): void {
  for (const event of events) routeEvent(event)
}
