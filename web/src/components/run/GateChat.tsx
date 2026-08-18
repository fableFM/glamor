import { useEffect, useMemo, useRef, useState } from 'react'
import { AlertTriangle, Check, ChevronRight, MessageSquarePlus, Send, X, Zap } from 'lucide-react'
import type { Event, Gate, Note, RunDetail, Stage } from '../../api/client'
import { ApiError, createNote, interruptStage, resolveGate } from '../../api/client'
import { useRunDetailsStore } from '../../stores/runDetails'
import { useRunEventsStore } from '../../stores/runEvents'
import { parseGateQuestions } from '../../lib/gateQuestions'
import { applyGateResolution } from '../../lib/gateResolution'
import { formatAge, formatDateTime } from '../../lib/format'

/*
  Чат с пайплайном (T-15) — нижняя панель экрана рана:
  - гейты как сообщения с действиями (approve/reject/answer/comment);
  - системные события рана — сворачиваемые строки;
  - queue note + список заметок; interrupt & steer на running-стадии;
  - оптимистичный UI: после resolve гейт сразу «резолвнуто»,
    reconciliation по событию gate.resolved (диспатчится в стор).
  Keyboard: `a` — approve первого открытого гейта, `c` — фокус на его поле.
*/

const GATE_KIND_LABELS: Record<Gate['kind'], string> = {
  plan_approval: 'План на утверждение',
  question: 'Вопрос планировщика',
  escalation: 'Эскалация',
  final_review: 'Финальный гейт',
  lesson_review: 'Урок на разбор',
}

const GATE_STATE_LABELS: Record<Gate['state'], string> = {
  open: 'открыт',
  answered: 'отвечен',
  approved: 'аппрув',
  rejected: 'отклонён',
  expired: 'истёк',
}

type ResolveAction = 'approve' | 'reject' | 'answer' | 'comment'

/** человекочитаемая строка системного события ленты */
function systemEventLine(event: Event): string {
  const payload = event.payload ?? {}
  const to = typeof payload.to === 'string' ? payload.to : ''
  switch (event.kind) {
    case 'run.state_changed':
      return `ран → ${to}`
    case 'run.branch_mismatch':
      return 'branch_mismatch! ветка рана не совпадает с чекаутом'
    case 'stage.state_changed':
      return `этап${event.stage_id != null ? ` #${event.stage_id}` : ''} → ${to}`
    case 'stage.queued':
      return `этап поставлен в очередь${event.stage_id != null ? ` (#${event.stage_id})` : ''}`
    case 'stage.resumed':
      return `этап возобновлён${event.stage_id != null ? ` (#${event.stage_id})` : ''}`
    case 'stage.interrupted':
      return `этап прерван${event.stage_id != null ? ` (#${event.stage_id})` : ''}`
    default:
      return event.kind
  }
}

function SystemEventRow({ event }: { event: Event }) {
  const mismatch = event.kind === 'run.branch_mismatch'
  return (
    <details className="group px-3 py-0.5">
      <summary
        className={`flex cursor-pointer list-none items-center gap-2 text-xs ${
          mismatch ? 'text-amber-300' : 'text-zinc-600'
        }`}
      >
        <ChevronRight className="size-3 transition-transform group-open:rotate-90" aria-hidden />
        {mismatch && <AlertTriangle className="size-3" aria-hidden />}
        <span>{systemEventLine(event)}</span>
        <span className="ml-auto">{formatDateTime(event.ts)}</span>
      </summary>
      <pre className="mt-1 overflow-auto rounded bg-zinc-900 p-2 text-xs text-zinc-500">
        {JSON.stringify(event.payload, null, 2)}
      </pre>
    </details>
  )
}

function GateCard({
  gate,
  onResolved,
  registerControls,
  onOpenGateView,
}: {
  gate: Gate
  onResolved: (gate: Gate, alreadyResolved: boolean) => void
  /** регистрация контролов первого открытого гейта для keyboard-шорткатов */
  registerControls?: (controls: { approve: () => void; focusComment: () => void } | null) => void
  /** «Открыть» — гейт-вью в панели этапа (полноформатные ответы) */
  onOpenGateView?: (gate: Gate) => void
}) {
  const [text, setText] = useState('')
  const [busy, setBusy] = useState<ResolveAction | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [already, setAlready] = useState(false)
  const textRef = useRef<HTMLTextAreaElement>(null)

  const open = gate.state === 'open'
  // поле ответа: question/escalation — обязательный ответ, остальным — comment → fix
  const answerAction: ResolveAction = gate.kind === 'question' || gate.kind === 'escalation' ? 'answer' : 'comment'
  // lesson_review (T-29): осмысленные лейблы действий вместо generic approve/reject
  const isLesson = gate.kind === 'lesson_review'
  const approveLabel = isLesson ? 'Сохранить в проект' : 'Approve'
  const rejectLabel = isLesson ? 'Отклонить' : 'Reject'
  const answerLabel = isLesson ? 'Сохранить с ответом' : answerAction === 'answer' ? 'Ответить' : 'Comment → fix'

  const doResolve = (action: ResolveAction, answerText?: string) => {
    if (!open || busy) return
    setBusy(action)
    setError(null)
    resolveGate(gate.id, { action, text: answerText }, crypto.randomUUID())
      .then((response) => {
        setAlready(response.already_resolved)
        onResolved(response.gate, response.already_resolved)
        setText('')
      })
      .catch((err: unknown) => {
        if (err instanceof ApiError && err.code === 'gate_already_resolved') {
          // гонка двух клиентов (UI/TG): помечаем и ждём gate.resolved по WS
          setAlready(true)
          setError('гейт уже резолвнут другим клиентом')
        } else {
          setError(err instanceof Error ? err.message : 'ошибка резолва')
        }
      })
      .finally(() => setBusy(null))
  }

  // keyboard-шорткаты (a/c) действуют на первый открытый гейт
  useEffect(() => {
    if (!registerControls) return
    registerControls({
      approve: () => doResolve('approve'),
      focusComment: () => textRef.current?.focus(),
    })
    return () => registerControls(null)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [registerControls, open, busy])

  // длинные открытые гейты — компактно: заголовок + «Открыть» (гейт-вью) + текст под спойлер
  const isLongOpen = open && gate.question.length > 280
  const questionCount = isLongOpen && gate.kind === 'question' ? parseGateQuestions(gate.question).length : 0
  const compactTitle =
    gate.kind === 'question'
      ? `❓ ${questionCount} вопрос${questionCount === 1 ? '' : questionCount > 1 && questionCount < 5 ? 'а' : 'ов'} планировщика`
      : GATE_KIND_LABELS[gate.kind]

  return (
    <div className="mx-3 my-1.5 rounded-lg border border-zinc-700 bg-zinc-900 px-3 py-2">
      <div className="flex items-center gap-2 text-xs text-zinc-500">
        <span className="font-medium text-amber-300">{GATE_KIND_LABELS[gate.kind]}</span>
        <span>ждёт {formatAge(gate.created_at)}</span>
        {isLongOpen && onOpenGateView && (
          <button
            type="button"
            onClick={() => onOpenGateView(gate)}
            className="rounded-md bg-amber-500/15 px-2 py-0.5 font-medium text-amber-300 hover:bg-amber-500/25"
          >
            Открыть
          </button>
        )}
        {!open && (
          <span className="ml-auto rounded bg-zinc-800 px-1.5 py-0.5 text-zinc-400">
            {GATE_STATE_LABELS[gate.state]}
          </span>
        )}
        {open && already && <span className="ml-auto text-zinc-500">резолвнуто</span>}
      </div>
      {gate.question &&
        (isLongOpen ? (
          <div className="mt-1.5">
            <p className="text-sm font-medium text-zinc-200">{compactTitle}</p>
            <details className="mt-1">
              <summary className="cursor-pointer text-xs text-zinc-600">текст гейта</summary>
              <p className="mt-1 whitespace-pre-wrap text-sm text-zinc-400">{gate.question}</p>
            </details>
          </div>
        ) : (
          <p className="mt-1.5 whitespace-pre-wrap text-sm text-zinc-200">{gate.question}</p>
        ))}
      {gate.answer && <p className="mt-1 text-sm text-zinc-500">ответ: {gate.answer}</p>}

      {open && !already && (
        <div className="mt-2 space-y-2">
          <textarea
            ref={textRef}
            value={text}
            onChange={(e) => setText(e.target.value)}
            rows={2}
            placeholder={answerAction === 'answer' ? 'ответ…' : 'комментарий → fix…'}
            className="w-full resize-y rounded-md border border-zinc-700 bg-zinc-950 px-2 py-1.5 text-sm text-zinc-200 placeholder:text-zinc-600 focus:border-violet-500 focus:outline-none"
          />
          <div className="flex flex-wrap gap-2">
            <button
              type="button"
              disabled={busy !== null}
              onClick={() => doResolve('approve')}
              className="flex items-center gap-1 rounded-md bg-emerald-600/80 px-2.5 py-1 text-xs font-medium text-emerald-50 hover:bg-emerald-600 disabled:opacity-50"
            >
              <Check className="size-3" aria-hidden /> {approveLabel}
            </button>
            <button
              type="button"
              disabled={busy !== null || text.trim().length === 0}
              onClick={() => doResolve(isLesson ? 'answer' : answerAction, text.trim())}
              className="flex items-center gap-1 rounded-md bg-violet-600/80 px-2.5 py-1 text-xs font-medium text-violet-50 hover:bg-violet-600 disabled:opacity-50"
            >
              <Send className="size-3" aria-hidden />
              {answerLabel}
            </button>
            {isLesson && (
              <button
                type="button"
                disabled={busy !== null}
                onClick={() => doResolve('comment', text.trim() || undefined)}
                className="flex items-center gap-1 rounded-md bg-sky-600/80 px-2.5 py-1 text-xs font-medium text-sky-50 hover:bg-sky-600 disabled:opacity-50"
              >
                <Send className="size-3" aria-hidden /> В глобальную
              </button>
            )}
            <button
              type="button"
              disabled={busy !== null}
              onClick={() => doResolve('reject', text.trim() || undefined)}
              className="flex items-center gap-1 rounded-md bg-red-600/70 px-2.5 py-1 text-xs font-medium text-red-50 hover:bg-red-600 disabled:opacity-50"
            >
              <X className="size-3" aria-hidden /> {rejectLabel}
            </button>
          </div>
        </div>
      )}
      {error && <p className="mt-1.5 text-xs text-red-400">{error}</p>}
    </div>
  )
}

function NotesBlock({ runId, notes }: { runId: string; notes: Note[] }) {
  const [text, setText] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const submit = () => {
    const value = text.trim()
    if (!value || busy) return
    setBusy(true)
    setError(null)
    createNote(runId, { text: value }, crypto.randomUUID())
      .then((note) => {
        useRunDetailsStore.getState().addNote(runId, note)
        setText('')
      })
      .catch((err: unknown) => setError(err instanceof Error ? err.message : 'ошибка'))
      .finally(() => setBusy(false))
  }

  return (
    <div className="border-t border-zinc-800 px-3 py-2">
      <div className="flex items-center gap-2">
        <MessageSquarePlus className="size-3.5 text-zinc-500" aria-hidden />
        <input
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') submit()
          }}
          placeholder="заметка к рану (queue note, без прерывания)"
          className="min-w-0 flex-1 rounded-md border border-zinc-700 bg-zinc-950 px-2 py-1 text-xs text-zinc-200 placeholder:text-zinc-600 focus:border-violet-500 focus:outline-none"
        />
        <button
          type="button"
          onClick={submit}
          disabled={busy || text.trim().length === 0}
          className="rounded-md bg-zinc-800 px-2 py-1 text-xs text-zinc-300 hover:bg-zinc-700 disabled:opacity-50"
        >
          Добавить
        </button>
      </div>
      {error && <p className="mt-1 text-xs text-red-400">{error}</p>}
      {notes.length > 0 && (
        <ul className="mt-1.5 space-y-0.5">
          {notes.map((note) => (
            <li key={note.id} className="flex items-center gap-2 text-xs">
              <span
                className={`size-1.5 shrink-0 rounded-full ${note.consumed ? 'bg-zinc-600' : 'bg-violet-400'}`}
                aria-hidden
              />
              <span className={note.consumed ? 'text-zinc-600 line-through' : 'text-zinc-400'}>{note.text}</span>
              <span className="ml-auto shrink-0 text-zinc-700">
                {note.kind === 'steer' ? 'steer · ' : ''}
                {note.consumed ? 'consumed' : 'в очереди'}
              </span>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

function InterruptBlock({ runId, runningStage }: { runId: string; runningStage: Stage | null }) {
  const [text, setText] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  if (!runningStage) return null

  const submit = () => {
    const message = text.trim()
    if (!message || busy) return
    // подтверждение: этап будет прерван и возобновлён с сообщением (D-22)
    if (!window.confirm(`Этап «${runningStage.stage_key}» будет прерван и возобновлён с вашим сообщением. Продолжить?`)) {
      return
    }
    setBusy(true)
    setError(null)
    // D-12: Idempotency-Key на мутацию (новый ключ на каждую попытку submit)
    interruptStage(runningStage.id, { message }, crypto.randomUUID())
      .then((stage) => {
        useRunDetailsStore.getState().upsertStage(runId, stage)
        setText('')
      })
      .catch((err: unknown) => setError(err instanceof Error ? err.message : 'ошибка'))
      .finally(() => setBusy(false))
  }

  return (
    <div className="border-t border-zinc-800 px-3 py-2">
      <div className="flex items-center gap-2">
        <Zap className="size-3.5 text-amber-400" aria-hidden />
        <input
          value={text}
          onChange={(e) => setText(e.target.value)}
          placeholder={`interrupt & steer: прервать «${runningStage.stage_key}» с сообщением…`}
          className="min-w-0 flex-1 rounded-md border border-amber-600/40 bg-zinc-950 px-2 py-1 text-xs text-zinc-200 placeholder:text-zinc-600 focus:border-amber-500 focus:outline-none"
        />
        <button
          type="button"
          onClick={submit}
          disabled={busy || text.trim().length === 0}
          className="rounded-md bg-amber-600/70 px-2 py-1 text-xs font-medium text-amber-50 hover:bg-amber-600 disabled:opacity-50"
        >
          Прервать
        </button>
      </div>
      {error && <p className="mt-1 text-xs text-red-400">{error}</p>}
    </div>
  )
}

/** элемент ленты: гейт или системное событие, общая сортировка по времени */
type FeedItem =
  | { type: 'gate'; ts: string; gate: Gate }
  | { type: 'system'; ts: string; id: number; event: Event }

export function GateChat({
  detail,
  onOpenGateView,
}: {
  detail: RunDetail
  /** «Открыть» в карточке гейта — гейт-вью в панели этапа */
  onOpenGateView?: (gate: Gate) => void
}) {
  const systemEvents = useRunEventsStore((s) => s.byRunId[detail.id])
  const scrollRef = useRef<HTMLDivElement>(null)
  const controlsRef = useRef<{ approve: () => void; focusComment: () => void } | null>(null)

  const feed = useMemo<FeedItem[]>(() => {
    const items: FeedItem[] = detail.gates.map((gate) => ({ type: 'gate', ts: gate.created_at, gate }))
    for (const event of systemEvents ?? []) {
      items.push({ type: 'system', ts: event.ts, id: event.id, event })
    }
    return items.sort((a, b) => a.ts.localeCompare(b.ts) || (a.type === 'system' && b.type === 'system' ? a.id - b.id : 0))
  }, [detail.gates, systemEvents])

  // автoскролл ленты вниз при новых сообщениях
  useEffect(() => {
    const node = scrollRef.current
    if (node) node.scrollTop = node.scrollHeight
  }, [feed.length])

  const firstOpenGate = detail.gates.find((g) => g.state === 'open')

  // keyboard: `a` — approve активного гейта, `c` — фокус на поле comment (D-64)
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      const target = event.target as HTMLElement | null
      if (target && (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.isContentEditable)) return
      if (event.key === 'a') controlsRef.current?.approve()
      else if (event.key === 'c') controlsRef.current?.focusComment()
    }
    document.addEventListener('keydown', onKeyDown)
    return () => document.removeEventListener('keydown', onKeyDown)
  }, [])

  const onResolved = (gate: Gate, alreadyResolved: boolean) => {
    applyGateResolution(detail.id, gate, alreadyResolved)
  }

  const runningStage = detail.stages.find((s) => s.state === 'running') ?? null

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="border-b border-zinc-800 px-3 py-1.5 text-xs font-semibold uppercase tracking-wide text-zinc-500">
        Чат с пайплайном
        <span className="ml-2 font-normal normal-case text-zinc-600">a — approve · c — комментарий</span>
      </div>
      <div ref={scrollRef} className="min-h-0 flex-1 overflow-y-auto overflow-x-hidden py-1 wrap-anywhere">
        {feed.length === 0 && <p className="px-3 py-2 text-sm text-zinc-600">событий пока нет</p>}
        {feed.map((item) =>
          item.type === 'gate' ? (
            <GateCard
              key={item.gate.id}
              gate={item.gate}
              onResolved={onResolved}
              registerControls={firstOpenGate?.id === item.gate.id ? (c) => (controlsRef.current = c) : undefined}
              onOpenGateView={onOpenGateView}
            />
          ) : (
            <SystemEventRow key={item.id} event={item.event} />
          ),
        )}
      </div>
      <InterruptBlock runId={detail.id} runningStage={runningStage} />
      <NotesBlock runId={detail.id} notes={detail.notes} />
    </div>
  )
}
