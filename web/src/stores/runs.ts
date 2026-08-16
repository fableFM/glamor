import { create } from 'zustand'
import type { Run } from '../api/client'

interface RunsState {
  /** раны по id; полный список подгружается REST'ом, состояния двигаются WS-событиями */
  byId: Record<string, Run>
  upsertRun: (run: Run) => void
  upsertMany: (runs: Run[]) => void
  applyStateChanged: (runId: string, to: string) => void
}

export const useRunsStore = create<RunsState>((set) => ({
  byId: {},
  upsertRun: (run) => set((state) => ({ byId: { ...state.byId, [run.id]: run } })),
  upsertMany: (runs) =>
    set((state) => ({
      byId: { ...state.byId, ...Object.fromEntries(runs.map((run) => [run.id, run])) },
    })),
  applyStateChanged: (runId, to) =>
    set((state) => {
      const run = state.byId[runId]
      // неизвестный ран — не создаём из события, его подтянет REST-снапшот
      if (!run) return state
      return { byId: { ...state.byId, [runId]: { ...run, state: to as Run['state'] } } }
    }),
}))
