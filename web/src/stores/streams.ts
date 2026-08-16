import { create } from 'zustand'
import type { Event } from '../api/client'

/** Кольцевой буфер стрим-событий per stage: храним только последние N, чтобы не раздувать память. */
export const STREAM_BUFFER_CAP = 2000

interface StreamsState {
  byStageId: Record<number, Event[]>
  append: (stageId: number, event: Event) => void
  appendMany: (stageId: number, events: Event[]) => void
  clear: (stageId?: number) => void
}

function capped(buffer: Event[]): Event[] {
  // дропаем самые старые события сверх cap
  return buffer.length > STREAM_BUFFER_CAP ? buffer.slice(buffer.length - STREAM_BUFFER_CAP) : buffer
}

export const useStreamsStore = create<StreamsState>((set) => ({
  byStageId: {},
  append: (stageId, event) =>
    set((state) => ({
      byStageId: { ...state.byStageId, [stageId]: capped([...(state.byStageId[stageId] ?? []), event]) },
    })),
  appendMany: (stageId, events) =>
    set((state) => ({
      byStageId: { ...state.byStageId, [stageId]: capped([...(state.byStageId[stageId] ?? []), ...events]) },
    })),
  clear: (stageId) =>
    set((state) => {
      if (stageId === undefined) return { byStageId: {} }
      const byStageId = { ...state.byStageId }
      delete byStageId[stageId]
      return { byStageId }
    }),
}))
