import { create } from 'zustand'
import type { Gate } from '../api/client'

/*
  Глобальный инбокс открытых гейтов (T-15): наполняется WS-событиями
  gate.opened / gate.resolved, приходящими по подписке run_id=*.
  REST-источника для «все открытые гейты» нет, поэтому исторические
  открытые гейты появляются здесь из WS-догона журнала.
*/

export interface InboxItem {
  gateId: string
  runId: string
  stageId: number | null
  kind: Gate['kind']
  question: string
  openedAt: string
}

interface InboxState {
  /** открытые гейты по gateId; resolved выпадают */
  open: Record<string, InboxItem>
  addGate: (item: InboxItem) => void
  removeGate: (gateId: string) => void
}

export const useInboxStore = create<InboxState>((set) => ({
  open: {},
  addGate: (item) => set((state) => ({ open: { ...state.open, [item.gateId]: item } })),
  removeGate: (gateId) =>
    set((state) => {
      if (!(gateId in state.open)) return state
      const open = { ...state.open }
      delete open[gateId]
      return { open }
    }),
}))
