import { create } from 'zustand'
import type { Event } from '../api/client'

/*
  Буфер стрим-событий per stage (F-01):
  - до REST-догона (mergeHistory) live-буфер ограничен STREAM_BUFFER_CAP —
    это защита памяти на случай, если догон ещё не случился;
  - после догона история НЕ режется: acceptance T-14 — ран с 5000+
    событий смотрится целиком, исторический ран — вся лента из БД.
    Рендер тысяч событий обеспечивает виртуализация (react-virtuoso
    в StreamView), а не отбрасывание начала ленты.
*/
export const STREAM_BUFFER_CAP = 2000

interface StreamsState {
  byStageId: Record<number, Event[]>
  /** стадии, для которых выполнен REST-догон: дальше cap не применяется */
  historyLoaded: Record<number, boolean>
  append: (stageId: number, event: Event) => void
  appendMany: (stageId: number, events: Event[]) => void
  /** слияние исторического догона (REST /runs/{id}/events) с live-буфером: дедуп по id + сортировка */
  mergeHistory: (stageId: number, events: Event[]) => void
  clear: (stageId?: number) => void
}

function capped(buffer: Event[]): Event[] {
  // дропаем самые старые события сверх cap (только live-буфер до догона)
  return buffer.length > STREAM_BUFFER_CAP ? buffer.slice(buffer.length - STREAM_BUFFER_CAP) : buffer
}

export const useStreamsStore = create<StreamsState>((set) => ({
  byStageId: {},
  historyLoaded: {},
  append: (stageId, event) =>
    set((state) => {
      const next = [...(state.byStageId[stageId] ?? []), event]
      return {
        byStageId: { ...state.byStageId, [stageId]: state.historyLoaded[stageId] ? next : capped(next) },
      }
    }),
  appendMany: (stageId, events) =>
    set((state) => {
      const next = [...(state.byStageId[stageId] ?? []), ...events]
      return {
        byStageId: { ...state.byStageId, [stageId]: state.historyLoaded[stageId] ? next : capped(next) },
      }
    }),
  mergeHistory: (stageId, events) =>
    set((state) => {
      // догон и live-поток пересекаются: сливаем по id журнала;
      // история не режется cap'ом — с этого момента буфер полный
      const byId = new Map<number, Event>()
      for (const event of state.byStageId[stageId] ?? []) byId.set(event.id, event)
      for (const event of events) byId.set(event.id, event)
      const merged = [...byId.values()].sort((a, b) => a.id - b.id)
      return {
        byStageId: { ...state.byStageId, [stageId]: merged },
        historyLoaded: { ...state.historyLoaded, [stageId]: true },
      }
    }),
  clear: (stageId) =>
    set((state) => {
      if (stageId === undefined) return { byStageId: {}, historyLoaded: {} }
      const byStageId = { ...state.byStageId }
      const historyLoaded = { ...state.historyLoaded }
      delete byStageId[stageId]
      delete historyLoaded[stageId]
      return { byStageId, historyLoaded }
    }),
}))
