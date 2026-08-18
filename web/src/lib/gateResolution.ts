import type { Gate } from '../api/client'
import { getRun } from '../api/client'
import { useRunDetailsStore } from '../stores/runDetails'
import { useInboxStore } from '../stores/inbox'

/*
  Общий пост-резолв гейта (T-15 + гейт-вью): оптимистично применяем
  ответ в сторы (событие gate.resolved по WS придёт тем же состоянием,
  идемпотентно), выносим из глобального инбокса; already_resolved
  (гонка с другим клиентом/TG) — догоняем полный срез рана REST'ом.
*/
export function applyGateResolution(runId: string, gate: Gate, alreadyResolved: boolean): void {
  useRunDetailsStore.getState().upsertGate(runId, gate)
  useInboxStore.getState().removeGate(gate.id)
  if (alreadyResolved) {
    getRun(runId)
      .then((fresh) => useRunDetailsStore.getState().setDetail(fresh))
      .catch(() => undefined)
  }
}
