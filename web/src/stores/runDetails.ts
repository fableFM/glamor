import { create } from 'zustand'
import type { Gate, Note, RunDetail, Stage } from '../api/client'

/*
  Детальный срез рана (stages/gates/artifacts/notes) per run.
  База — REST-снапшот GET /runs/{id}; дальше двигается WS-событиями
  (stage.state_changed, gate.opened/resolved, run.state_changed)
  и ответами мутаций (resolve/stop/resume/note/interrupt).
*/

interface RunDetailsState {
  byRunId: Record<string, RunDetail>
  setDetail: (detail: RunDetail) => void
  applyRunState: (runId: string, to: string) => void
  applyStageState: (runId: string, stageId: number, to: string) => void
  /** полная замена стадии (ответ interrupt и т.п.) */
  upsertStage: (runId: string, stage: Stage) => void
  /** live-токены из stream.usage: usage в сессии кумулятивный, берём max */
  applyStageUsage: (runId: string, stageId: number, tokensIn: number, tokensOut: number) => void
  applyStageResumed: (runId: string, stageId: number) => void
  applyGateOpened: (runId: string, gate: Gate) => void
  applyGateResolved: (runId: string, gateId: string, resolution: Gate['state'], answer: string | null, resolvedAt: string) => void
  /** полная замена гейта (ответ POST /gates/{id}/resolve) */
  upsertGate: (runId: string, gate: Gate) => void
  addNote: (runId: string, note: Note) => void
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
  upsertStage: (runId, stage) =>
    set((state) => {
      const detail = state.byRunId[runId]
      if (!detail) return state
      const index = detail.stages.findIndex((s) => s.id === stage.id)
      const stages = detail.stages.slice()
      if (index === -1) {
        // новая попытка (строка) — вставляем, а не ждём REST-перезагрузку
        stages.push(stage)
      } else {
        stages[index] = stage
      }
      return { byRunId: { ...state.byRunId, [runId]: { ...detail, stages } } }
    }),
  applyStageUsage: (runId, stageId, tokensIn, tokensOut) =>
    set((state) => {
      const detail = state.byRunId[runId]
      if (!detail) return state
      const index = detail.stages.findIndex((s) => s.id === stageId)
      if (index === -1) return state
      const stage = detail.stages[index]
      const stages = detail.stages.slice()
      stages[index] = {
        ...stage,
        tokens_in: Math.max(stage.tokens_in, tokensIn),
        tokens_out: Math.max(stage.tokens_out, tokensOut),
      }
      return { byRunId: { ...state.byRunId, [runId]: { ...detail, stages } } }
    }),
  applyStageResumed: (runId, stageId) =>
    set((state) => {
      const detail = state.byRunId[runId]
      if (!detail) return state
      const index = detail.stages.findIndex((s) => s.id === stageId)
      if (index === -1) return state
      const stages = detail.stages.slice()
      stages[index] = { ...stages[index], resume_count: stages[index].resume_count + 1 }
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
  upsertGate: (runId, gate) =>
    set((state) => {
      const detail = state.byRunId[runId]
      if (!detail) return state
      const index = detail.gates.findIndex((g) => g.id === gate.id)
      const gates =
        index === -1 ? [...detail.gates, gate] : detail.gates.map((g) => (g.id === gate.id ? gate : g))
      return { byRunId: { ...state.byRunId, [runId]: { ...detail, gates } } }
    }),
  addNote: (runId, note) =>
    set((state) => {
      const detail = state.byRunId[runId]
      if (!detail) return state
      if (detail.notes.some((n) => n.id === note.id)) return state // дубль
      return { byRunId: { ...state.byRunId, [runId]: { ...detail, notes: [...detail.notes, note] } } }
    }),
}))
