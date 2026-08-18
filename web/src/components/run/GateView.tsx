import { useMemo, useState } from 'react'
import { Check, ChevronDown, ChevronRight, Send, X } from 'lucide-react'
import type { Gate } from '../../api/client'
import { ApiError, resolveGate } from '../../api/client'
import { Markdown } from '../Markdown'
import {
  clearGateDrafts,
  loadGateDraft,
  parseGateQuestions,
  saveGateDraft,
} from '../../lib/gateQuestions'
import { applyGateResolution } from '../../lib/gateResolution'

/*
  Гейт-вью в панели этапа: полноформатная работа с открытым гейтом.
  - question: текст разбивается на карточки-аккордеоны (parseGateQuestions),
    у каждой textarea ответа с предзаполненным дефолтом; черновики —
    в localStorage (glamor.gate-draft.<gateId>.<i>), не теряются ни при
    переключении табов/стадий, ни при перезагрузке. Отправка очищает.
  - plan_approval/final_review: markdown саммари + Approve/Comment/Reject.
  - escalation: findings + поле ответа.
  «✕ закрыть» просто возвращает на таб Стрим, ничего не отправляет.
*/

type ResolveAction = 'approve' | 'reject' | 'answer' | 'comment'

const textareaCls =
  'w-full resize-y rounded-md border border-zinc-700 bg-zinc-950 px-2 py-1.5 text-sm text-zinc-200 placeholder:text-zinc-600 focus:border-violet-500 focus:outline-none'

function useResolve(gate: Gate, onResolved: () => void) {
  const [busy, setBusy] = useState<ResolveAction | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [already, setAlready] = useState(false)

  const doResolve = (action: ResolveAction, text: string | undefined, afterSuccess?: () => void) => {
    if (busy) return
    setBusy(action)
    setError(null)
    resolveGate(gate.id, { action, text }, crypto.randomUUID())
      .then((response) => {
        setAlready(response.already_resolved)
        applyGateResolution(gate.run_id, response.gate, response.already_resolved)
        afterSuccess?.()
        onResolved()
      })
      .catch((err: unknown) => {
        if (err instanceof ApiError && err.code === 'gate_already_resolved') {
          // гонка с другим клиентом (UI/TG): показываем и ждём gate.resolved
          setAlready(true)
          setError('гейт уже резолвнут другим клиентом')
        } else {
          setError(err instanceof Error ? err.message : 'ошибка резолва')
        }
      })
      .finally(() => setBusy(null))
  }

  return { busy, error, already, doResolve }
}

/** question-гейт: карточки вопросов с черновиками ответов. */
function QuestionsView({
  gate,
  onResolved,
  onClose,
}: {
  gate: Gate
  onResolved: () => void
  onClose: () => void
}) {
  const questions = useMemo(() => parseGateQuestions(gate.question), [gate.question])
  // черновик важнее дефолта; дефолт важнее пустого
  const [answers, setAnswers] = useState<string[]>(() =>
    questions.map((q, i) => loadGateDraft(gate.id, i) ?? q.defaultAnswer),
  )
  const [expanded, setExpanded] = useState<boolean[]>(() => questions.map((_, i) => i === 0))
  const { busy, error, already, doResolve } = useResolve(gate, onResolved)

  const setAnswer = (index: number, text: string) => {
    setAnswers((current) => current.map((a, i) => (i === index ? text : a)))
    saveGateDraft(gate.id, index, text)
  }

  const toggle = (index: number) => setExpanded((current) => current.map((v, i) => (i === index ? !v : v)))

  /** агрегированный ответ: «1. …\n2. …» */
  const aggregate = (values: string[]): string =>
    values.map((value, i) => `${i + 1}. ${value.trim() || '—'}`).join('\n')

  const clearDrafts = () => clearGateDrafts(gate.id, questions.length)

  return (
    <div className="flex h-full min-h-0 flex-col">
      <GateViewHeader title={`❓ Вопросы планировщика (${questions.length})`} already={already} onClose={onClose} />
      <div className="min-h-0 flex-1 space-y-2 overflow-auto p-3">
        {questions.map((question, index) => (
          <div key={index} className="rounded-lg border border-zinc-800 bg-zinc-900/60">
            <button
              type="button"
              onClick={() => toggle(index)}
              className="flex w-full items-center gap-2 px-3 py-2 text-left"
            >
              {expanded[index] ? (
                <ChevronDown className="size-3.5 shrink-0 text-zinc-500" aria-hidden />
              ) : (
                <ChevronRight className="size-3.5 shrink-0 text-zinc-500" aria-hidden />
              )}
              <span className="min-w-0 flex-1 truncate text-sm font-medium text-zinc-200">
                {index + 1}. {question.title || 'вопрос'}
              </span>
              {question.defaultAnswer && <span className="shrink-0 text-xs text-zinc-600">есть дефолт</span>}
            </button>
            {expanded[index] && (
              <div className="space-y-2 border-t border-zinc-800 px-3 py-2">
                <Markdown>{question.body}</Markdown>
                <textarea
                  value={answers[index] ?? ''}
                  onChange={(e) => setAnswer(index, e.target.value)}
                  rows={2}
                  placeholder="ответ…"
                  className={textareaCls}
                />
              </div>
            )}
          </div>
        ))}
      </div>
      <div className="flex items-center gap-2 border-t border-zinc-800 px-3 py-2">
        <button
          type="button"
          disabled={busy !== null}
          onClick={() =>
            doResolve('answer', aggregate(questions.map((q) => q.defaultAnswer)), clearDrafts)
          }
          className="rounded-md border border-zinc-700 px-2.5 py-1 text-xs text-zinc-300 hover:bg-zinc-800 disabled:opacity-50"
          title="резолв с ответами по умолчанию"
        >
          Всё по умолчанию
        </button>
        <button
          type="button"
          disabled={busy !== null}
          onClick={() => doResolve('answer', aggregate(answers), clearDrafts)}
          className="flex items-center gap-1 rounded-md bg-violet-600 px-2.5 py-1 text-xs font-medium text-violet-50 hover:bg-violet-500 disabled:opacity-50"
        >
          <Send className="size-3" aria-hidden /> {busy === 'answer' ? 'отправка…' : 'Ответить ▸'}
        </button>
        {error && <span className="text-xs text-red-400">{error}</span>}
      </div>
    </div>
  )
}

/** plan_approval/final_review/escalation: markdown-текст + действия. */
function DecisionView({
  gate,
  onResolved,
  onClose,
}: {
  gate: Gate
  onResolved: () => void
  onClose: () => void
}) {
  const [text, setText] = useState('')
  const { busy, error, already, doResolve } = useResolve(gate, onResolved)
  const isEscalation = gate.kind === 'escalation'
  const title = isEscalation ? '⚠ Эскалация' : gate.kind === 'final_review' ? '✋ Финальный гейт' : '✋ Гейт'

  return (
    <div className="flex h-full min-h-0 flex-col">
      <GateViewHeader title={title} already={already} onClose={onClose} />
      <div className="min-h-0 flex-1 overflow-auto p-4">
        <Markdown>{gate.question}</Markdown>
      </div>
      <div className="space-y-2 border-t border-zinc-800 px-3 py-2">
        <textarea
          value={text}
          onChange={(e) => setText(e.target.value)}
          rows={2}
          placeholder={isEscalation ? 'ответ на эскалацию…' : 'комментарий → fix…'}
          className={textareaCls}
        />
        <div className="flex flex-wrap items-center gap-2">
          {isEscalation ? (
            <button
              type="button"
              disabled={busy !== null || text.trim().length === 0}
              onClick={() => doResolve('answer', text.trim())}
              className="flex items-center gap-1 rounded-md bg-violet-600 px-2.5 py-1 text-xs font-medium text-violet-50 hover:bg-violet-500 disabled:opacity-50"
            >
              <Send className="size-3" aria-hidden /> Ответить
            </button>
          ) : (
            <>
              <button
                type="button"
                disabled={busy !== null}
                onClick={() => doResolve('approve', undefined)}
                className="flex items-center gap-1 rounded-md bg-emerald-600/80 px-2.5 py-1 text-xs font-medium text-emerald-50 hover:bg-emerald-600 disabled:opacity-50"
              >
                <Check className="size-3" aria-hidden /> Approve
              </button>
              <button
                type="button"
                disabled={busy !== null || text.trim().length === 0}
                onClick={() => doResolve('comment', text.trim())}
                className="flex items-center gap-1 rounded-md bg-violet-600/80 px-2.5 py-1 text-xs font-medium text-violet-50 hover:bg-violet-600 disabled:opacity-50"
              >
                <Send className="size-3" aria-hidden /> Comment
              </button>
            </>
          )}
          <button
            type="button"
            disabled={busy !== null}
            onClick={() => doResolve('reject', text.trim() || undefined)}
            className="flex items-center gap-1 rounded-md bg-red-600/70 px-2.5 py-1 text-xs font-medium text-red-50 hover:bg-red-600 disabled:opacity-50"
          >
            <X className="size-3" aria-hidden /> Reject
          </button>
          {error && <span className="text-xs text-red-400">{error}</span>}
        </div>
      </div>
    </div>
  )
}

function GateViewHeader({
  title,
  already,
  onClose,
}: {
  title: string
  already: boolean
  onClose: () => void
}) {
  return (
    <div className="flex items-center gap-2 border-b border-zinc-800 px-3 py-2">
      <span className="text-sm font-medium text-amber-300">{title}</span>
      {already && <span className="text-xs text-zinc-500">уже резолвнуто</span>}
      <button
        type="button"
        onClick={onClose}
        title="закрыть (черновики сохранятся), ничего не отправляет"
        className="ml-auto flex items-center gap-1 rounded-md px-2 py-1 text-xs text-zinc-500 hover:bg-zinc-800 hover:text-zinc-200"
      >
        <X className="size-3.5" aria-hidden /> закрыть
      </button>
    </div>
  )
}

export function GateView({
  gate,
  onResolved,
  onClose,
}: {
  gate: Gate
  onResolved: () => void
  onClose: () => void
}) {
  if (gate.kind === 'question') {
    return <QuestionsView gate={gate} onResolved={onResolved} onClose={onClose} />
  }
  return <DecisionView gate={gate} onResolved={onResolved} onClose={onClose} />
}
