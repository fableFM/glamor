import { create } from 'zustand'
import type { Gate, RunDetail, Stage } from '../api/client'

/*
  Детальный срез рана (stages/gates/artifacts/notes) per run.
  База — REST-снапшот GET /runs/{id}; дальше двигается WS-событиями
  (stage.state_changed, gate.opened/resolved, run.state_changed).
*/

interface RunDetailsState {
  byRunId: Record<string, RunDetail>
  setDetail: (detail: RunDetail) => void
  applyRunState: (runId: string, to: string) => void
  applyStageState: (runId: string, stageId: number, to: string) => void
  applyGateOpened: (runId: string, gate: Gate) => void
  applyGateResolved: (runId: string, gateId: string, resolution: Gate['state'], answer: string | null, resolvedAt: string) => void
}

export const useRunDetailsStore = create<RunDetailsState>((set) => ({
  byRunId: {},
  setDetail: (detail) => set((state) => ({ byRunId: { ...state.byRunId, [detail.id]: detail } })),
  applyRunState: (runId, to) =>
    set((state) => {
      const detail = state.byRunId[runId]
      if (!detail) return state
      return { byRunId: { ...state.byRunId, [runId]: { ...detail, state: to as RunDetail['state'] } } }
    }),
  applyStageState: (runId, stageId, to) =>
    set((state) => {
      const detail = state.byRunId[runId]
      if (!detail) return state
      const index = detail.stages.findIndex((s) => s.id === stageId)
      if (index === -1) return state // неизвестный stage — его принесёт следующий REST-снапшот
      const stages = detail.stages.slice()
      stages[index] = { ...stages[index], state: to as Stage['state'] }
      return { byRunId: { ...state.byRunId, [runId]: { ...detail, stages } } }
    }),
  applyGateOpened: (runId, gate) =>
    set((state) => {
      const detail = state.byRunId[runId]
      if (!detail) return state
      if (detail.gates.some((g) => g.id === gate.id)) return state // дубль
      return { byRunId: { ...state.byRunId, [runId]: { ...detail, gates: [...detail.gates, gate] } } }
    }),
  applyGateResolved: (runId, gateId, resolution, answer, resolvedAt) =>
    set((state) => {
      const detail = state.byRunId[runId]
      if (!detail) return state
      const index = detail.gates.findIndex((g) => g.id === gateId)
      if (index === -1) return state
      const gates = detail.gates.slice()
      gates[index] = { ...gates[index], state: resolution, answer, resolved_at: resolvedAt }
      return { byRunId: { ...state.byRunId, [runId]: { ...detail, gates } } }
    }),
}))
