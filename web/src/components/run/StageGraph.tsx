import { AlertTriangle, Check, Circle, User, X } from 'lucide-react'
import type { Gate, Stage } from '../../api/client'
import { useStreamsStore } from '../../stores/streams'
import { parseGateQuestions } from '../../lib/gateQuestions'

/*
  Граф этапов рана (T-14): M1-вариант — вертикальная колонка нод
  (xyflow появится в редакторе пайплайнов T-20). Нода: stage_key,
  состояние, итерация, resume_count; у running — пульс и последние
  2 строки стрима прямо в ноде. У стадии с открытым гейтом — янтарный
  пульсирующий бейдж (❓ N вопросов / ✋ гейт / ⚠ эскалация).
  Клик — выбор стадии (+ открытие гейт-вью, логика на странице рана),
  j/k — навигация.
*/

/** подпись бейджа по первому открытому гейту стадии */
function gateBadgeLabel(gates: Gate[]): string {
  const first = gates[0]
  if (first.kind === 'question') {
    const count = parseGateQuestions(first.question).length
    return `❓ ${count} вопрос${count === 1 ? '' : count < 5 ? 'а' : 'ов'}`
  }
  if (first.kind === 'escalation') return '⚠ эскалация'
  return '✋ гейт'
}

/** последние 2 строки текстового стрима стадии (для живой ноды) */
function useLastStreamLines(stageId: number, enabled: boolean): string[] {
  const events = useStreamsStore((s) => s.byStageId[stageId])
  if (!enabled || !events) return []
  for (let i = events.length - 1; i >= 0; i--) {
    const event = events[i]
    if (event.kind !== 'stream.text' && event.kind !== 'stream.thinking') continue
    const text = event.payload?.text
    if (typeof text !== 'string') continue
    const lines = text.split('\n').filter((l) => l.trim().length > 0)
    if (lines.length > 0) return lines.slice(-2).map((l) => (l.length > 90 ? `${l.slice(0, 90)}…` : l))
  }
  return []
}

function StateIcon({ stage, waiting }: { stage: Stage; waiting: boolean }) {
  if (waiting) return <User className="size-4 text-amber-300" aria-hidden />
  switch (stage.state) {
    case 'succeeded':
      return <Check className="size-4 text-emerald-400" aria-hidden />
    case 'failed':
      return <X className="size-4 text-red-400" aria-hidden />
    case 'interrupted':
      return <AlertTriangle className="size-4 text-amber-400" aria-hidden />
    case 'running':
      return <span className="size-2.5 animate-pulse rounded-full bg-blue-400" aria-hidden />
    default:
      return <Circle className="size-4 text-zinc-600" aria-hidden />
  }
}

function StageNode({
  stage,
  openGates,
  selected,
  onSelect,
}: {
  stage: Stage
  openGates: Gate[]
  selected: boolean
  onSelect: () => void
}) {
  const lines = useLastStreamLines(stage.id, stage.state === 'running')
  const waiting = openGates.length > 0

  return (
    <button
      type="button"
      onClick={onSelect}
      data-stage-node={stage.id}
      className={`w-full rounded-lg border px-3 py-2 text-left transition-colors ${
        selected
          ? 'border-violet-500/60 bg-violet-500/10'
          : waiting
            ? 'border-amber-500/50 bg-amber-500/5 hover:border-amber-500/70'
            : 'border-zinc-800 bg-zinc-900/60 hover:border-zinc-700'
      }`}
    >
      <div className="flex items-center gap-2">
        <StateIcon stage={stage} waiting={waiting} />
        <span className="font-mono text-sm font-medium text-zinc-200">{stage.stage_key}</span>
        {stage.iteration > 1 && (
          <span className="rounded bg-zinc-800 px-1.5 text-xs text-zinc-400">iter {stage.iteration}</span>
        )}
        <span className="ml-auto text-xs text-zinc-600">{stage.state}</span>
      </div>
      {waiting && (
        <span className="mt-1.5 inline-flex animate-pulse items-center gap-1 rounded-full bg-amber-500/15 px-2 py-0.5 text-xs font-medium text-amber-300">
          {gateBadgeLabel(openGates)}
          {openGates.length > 1 && ` ×${openGates.length}`}
        </span>
      )}
      {stage.state === 'interrupted' && stage.resume_count > 0 && (
        <div className="mt-1 text-xs text-amber-400/80">resume ×{stage.resume_count}</div>
      )}
      {stage.state === 'running' && lines.length > 0 && (
        <div className="mt-1.5 space-y-0.5 border-l-2 border-blue-500/30 pl-2">
          {lines.map((line, i) => (
            <div key={i} className="truncate text-xs text-zinc-500">
              {line}
            </div>
          ))}
        </div>
      )}
    </button>
  )
}

export function StageGraph({
  stages,
  gates,
  selectedStageId,
  onSelect,
}: {
  stages: Stage[]
  gates: Gate[]
  selectedStageId: number | null
  onSelect: (stageId: number) => void
}) {
  // открытые гейты по стадиям (гейты уровня рана без stage_id нод не касаются)
  const openGatesByStage = new Map<number, Gate[]>()
  for (const gate of gates) {
    if (gate.state !== 'open' || gate.stage_id == null) continue
    const list = openGatesByStage.get(gate.stage_id) ?? []
    list.push(gate)
    openGatesByStage.set(gate.stage_id, list)
  }

  return (
    <div className="flex h-full flex-col gap-2 overflow-auto p-3">
      {stages.map((stage) => (
        <StageNode
          key={stage.id}
          stage={stage}
          openGates={openGatesByStage.get(stage.id) ?? []}
          selected={stage.id === selectedStageId}
          onSelect={() => onSelect(stage.id)}
        />
      ))}
      {stages.length === 0 && <p className="p-2 text-sm text-zinc-600">этапов пока нет</p>}
    </div>
  )
}
