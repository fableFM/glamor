import { create } from 'zustand'
import type { Event } from '../api/client'

/*
  Лента доменных событий рана (run-, stage-, gate-пространства) для чата T-15:
  системные строки («этап code начат», branch_mismatch и т.п.).
  Стрим-события (stream.*) здесь не хранятся — за них отвечает streams-стор.
  Наполняется из dispatch (WS) и REST-догона GET /runs/{id}/events.
  Дедуп по id журнала: WS-догон и REST-догон могут привезти одно и то же.
*/

/** потолок ленты per run — чат не обязан хранить весь журнал */
const RUN_EVENTS_CAP = 500

interface RunEventsState {
  byRunId: Record<string, Event[]>
  append: (runId: string, event: Event) => void
  /** слияние исторической страницы с имеющимся буфером (дедуп + сортировка по id) */
  mergeHistory: (runId: string, events: Event[]) => void
}

function mergeSorted(existing: Event[], incoming: Event[]): Event[] {
  const byId = new Map<number, Event>()
  for (const event of existing) byId.set(event.id, event)
  for (const event of incoming) byId.set(event.id, event)
  const merged = [...byId.values()].sort((a, b) => a.id - b.id)
  return merged.length > RUN_EVENTS_CAP ? merged.slice(merged.length - RUN_EVENTS_CAP) : merged
}

export const useRunEventsStore = create<RunEventsState>((set) => ({
  byRunId: {},
  append: (runId, event) =>
    set((state) => {
      const existing = state.byRunId[runId] ?? []
      if (existing.some((e) => e.id === event.id)) return state // дубль
      return { byRunId: { ...state.byRunId, [runId]: mergeSorted(existing, [event]) } }
    }),
  mergeHistory: (runId, events) =>
    set((state) => ({
      byRunId: { ...state.byRunId, [runId]: mergeSorted(state.byRunId[runId] ?? [], events) },
    })),
}))
