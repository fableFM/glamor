import { useEffect, useMemo, useState } from 'react'
import { Check, ChevronDown, ChevronRight, Send, X } from 'lucide-react'
import type { Artifact, Gate } from '../../api/client'
import { ApiError, getArtifactContent, getLesson, resolveGate } from '../../api/client'
import { Markdown } from '../Markdown'
import { artifactFileName } from '../../lib/artifacts'
import { lessonBody, lineDiff, parseLessonOperations } from '../../lib/lessonOps'
import type { LessonOperation } from '../../lib/lessonOps'
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
  - lesson_review (T-30): операции distill (NEW/REFINE/SUPERSEDE/LINK) из
    lessons.md (полный текст — артефактом рана, не усечённым excerpt'ом из
    question), REFINE/SUPERSEDE — с line-diff'ом против текущей версии урока
    (GET /lessons/{id}); per-card резолв через lesson_ops (принять/отклонить
    каждую), fallback — «принять всё»/«отклонить всё» без lesson_ops.
  «✕ закрыть» просто возвращает на таб Стрим, ничего не отправляет.
*/

type ResolveAction = 'approve' | 'reject' | 'answer' | 'comment'

const textareaCls =
  'w-full resize-y rounded-md border border-zinc-700 bg-zinc-950 px-2 py-1.5 text-sm text-zinc-200 placeholder:text-zinc-600 focus:border-violet-500 focus:outline-none'

function useResolve(gate: Gate, onResolved: () => void) {
  const [busy, setBusy] = useState<ResolveAction | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [already, setAlready] = useState(false)

  const doResolve = (
    action: ResolveAction,
    text: string | undefined,
    afterSuccess?: () => void,
    /** per-card резолв lesson_review (T-30): без него — «всё или ничего» */
    lessonOps?: { accept: number[]; reject: number[] },
  ) => {
    if (busy) return
    setBusy(action)
    setError(null)
    resolveGate(gate.id, { action, text, ...(lessonOps ? { lesson_ops: lessonOps } : {}) }, crypto.randomUUID())
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

// --- lesson_review (T-30): операции distill с diff'ом и per-card резолвом ---

const OP_LABELS: Record<LessonOperation['op'], string> = {
  new: 'новый урок',
  question: 'вопрос',
  refine: 'уточнение',
  supersede: 'вытеснение',
  link: 'связь',
}

const OP_CLASSES: Record<LessonOperation['op'], string> = {
  new: 'bg-emerald-500/15 text-emerald-300',
  question: 'bg-amber-500/15 text-amber-300',
  refine: 'bg-sky-500/15 text-sky-300',
  supersede: 'bg-violet-500/15 text-violet-300',
  link: 'bg-zinc-500/15 text-zinc-400',
}

/** line-diff старой и новой формулировки (REFINE/SUPERSEDE), без зависимостей */
function LessonDiff({ oldText, newText }: { oldText: string; newText: string }) {
  const lines = useMemo(() => lineDiff(oldText, newText), [oldText, newText])
  return (
    <pre className="max-h-72 overflow-auto rounded bg-zinc-950 p-2 font-mono text-xs leading-5">
      {lines.map((line, index) => (
        <div
          key={index}
          className={
            line.type === 'add'
              ? 'bg-emerald-500/10 text-emerald-300'
              : line.type === 'del'
                ? 'bg-red-500/10 text-red-300'
                : 'text-zinc-500'
          }
        >
          {line.type === 'add' ? '+ ' : line.type === 'del' ? '- ' : '  '}
          {line.text.length > 0 ? line.text : ' '}
        </div>
      ))}
    </pre>
  )
}

/** одна операция distill: карточка с решением принять/отклонить */
function OperationCard({
  index,
  operation,
  oldBody,
  decision,
  onDecision,
}: {
  index: number
  operation: LessonOperation
  /** тело текущей версии target-урока (REFINE/SUPERSEDE); undefined — ещё грузится, null — недоступно */
  oldBody?: string | null
  decision: 'accept' | 'reject'
  onDecision: (index: number, decision: 'accept' | 'reject') => void
}) {
  const [expanded, setExpanded] = useState(true)
  const isDelta = operation.op === 'refine' || operation.op === 'supersede'

  return (
    <div className="rounded-lg border border-zinc-800 bg-zinc-900/60">
      <div className="flex w-full items-center gap-2 px-3 py-2">
        <button type="button" onClick={() => setExpanded((v) => !v)} className="flex min-w-0 flex-1 items-center gap-2 text-left">
          {expanded ? (
            <ChevronDown className="size-3.5 shrink-0 text-zinc-500" aria-hidden />
          ) : (
            <ChevronRight className="size-3.5 shrink-0 text-zinc-500" aria-hidden />
          )}
          <span className={`shrink-0 rounded px-1.5 py-0.5 text-xs ${OP_CLASSES[operation.op]}`}>
            {OP_LABELS[operation.op]}
          </span>
          <span className="min-w-0 flex-1 truncate text-sm font-medium text-zinc-200">
            {operation.title || `${operation.targetId} ↔ ${operation.target2Id}`}
          </span>
        </button>
        {/* per-card решение (T-30): индексы — в lesson_ops запроса резолва */}
        <span className="flex shrink-0 gap-1">
          <button
            type="button"
            onClick={() => onDecision(index, 'accept')}
            title="принять операцию"
            className={`rounded px-1.5 py-0.5 text-xs ${
              decision === 'accept' ? 'bg-emerald-500/25 text-emerald-200' : 'text-zinc-600 hover:text-emerald-300'
            }`}
          >
            <Check className="size-3.5" aria-hidden />
          </button>
          <button
            type="button"
            onClick={() => onDecision(index, 'reject')}
            title="отклонить операцию"
            className={`rounded px-1.5 py-0.5 text-xs ${
              decision === 'reject' ? 'bg-red-500/25 text-red-200' : 'text-zinc-600 hover:text-red-300'
            }`}
          >
            <X className="size-3.5" aria-hidden />
          </button>
        </span>
      </div>
      {expanded && (
        <div className="space-y-2 border-t border-zinc-800 px-3 py-2">
          {operation.kind === 'vendor' && (
            <p className="text-xs text-sky-300/80">
              vendor-урок: {[operation.vendor, operation.vendorVersion].filter(Boolean).join('@')}
              {operation.area ? ` · ${operation.area}` : ''}
            </p>
          )}
          {operation.op === 'link' && (
            <p className="text-xs text-zinc-400">
              связать уроки: <span className="font-mono">{operation.targetId}</span> ↔{' '}
              <span className="font-mono">{operation.target2Id}</span>
            </p>
          )}
          {isDelta && (
            <p className="text-xs text-zinc-500">
              {operation.op === 'refine' ? 'уточняет' : 'вытесняет'} урок{' '}
              <span className="font-mono">{operation.targetId}</span>
            </p>
          )}
          {isDelta && oldBody !== null && oldBody !== undefined ? (
            <LessonDiff oldText={oldBody} newText={operation.body} />
          ) : isDelta && oldBody === null ? (
            <>
              <p className="text-xs text-amber-400/80">текущая версия урока недоступна — показана новая формулировка</p>
              <Markdown>{operation.body}</Markdown>
            </>
          ) : operation.op !== 'link' ? (
            <Markdown>{operation.body}</Markdown>
          ) : null}
          {operation.triggers.length > 0 && (
            <p className="text-xs text-zinc-600">триггеры: {operation.triggers.join(', ')}</p>
          )}
        </div>
      )}
    </div>
  )
}

/*
  lesson_review (T-30): разбор операций distill из lessons.md.
  Полный текст черновика читаем артефактом рана (question несёт только
  усечённый excerpt — по нему индексы операций lesson_ops не восстановить).
  Per-card: принятые → confirmed (проект), отклонённые NEW → rejected (dedup),
  отклонённые дельты просто не применяются (семантика backend finalizer'а).
  Fallback «принять всё»/«отклонить всё» — резолв без lesson_ops.
*/
function LessonReviewView({
  gate,
  artifacts,
  onResolved,
  onClose,
}: {
  gate: Gate
  artifacts: Artifact[]
  onResolved: () => void
  onClose: () => void
}) {
  const [text, setText] = useState('')
  const { busy, error, already, doResolve } = useResolve(gate, onResolved)

  // полный черновик lessons.md — артефакт distill-этапа
  const lessonsArtifact = useMemo(() => {
    const candidates = artifacts.filter((a) => artifactFileName(a.path) === 'lessons.md')
    const own = gate.stage_id != null ? candidates.filter((a) => a.stage_id === gate.stage_id) : []
    const pool = own.length > 0 ? own : candidates
    return pool.reduce<Artifact | null>((max, a) => (max === null || a.id > max.id ? a : max), null)
  }, [artifacts, gate.stage_id])

  const [draft, setDraft] = useState<string | null>(null)
  const [draftError, setDraftError] = useState<string | null>(null)
  useEffect(() => {
    if (!lessonsArtifact) {
      setDraftError('артефакт lessons.md не найден — доступен только резолв «всё или ничего»')
      return
    }
    let cancelled = false
    getArtifactContent(gate.run_id, lessonsArtifact.id)
      .then((content) => {
        if (!cancelled) setDraft(content)
      })
      .catch((err: unknown) => {
        if (!cancelled) setDraftError(err instanceof Error ? err.message : 'ошибка загрузки черновика')
      })
    return () => {
      cancelled = true
    }
  }, [gate.run_id, lessonsArtifact])

  const operations = useMemo(() => (draft !== null ? parseLessonOperations(draft) : []), [draft])

  // старые версии target-уроков для diff'а (REFINE/SUPERSEDE)
  const [oldBodies, setOldBodies] = useState<Record<string, string | null>>({})
  useEffect(() => {
    const targets = [
      ...new Set(
        operations
          .filter((op) => op.op === 'refine' || op.op === 'supersede')
          .map((op) => op.targetId),
      ),
    ]
    if (targets.length === 0) return
    let cancelled = false
    for (const id of targets) {
      getLesson(id)
        .then((detail) => {
          if (!cancelled) setOldBodies((current) => ({ ...current, [id]: lessonBody(detail.content) }))
        })
        .catch(() => {
          if (!cancelled) setOldBodies((current) => ({ ...current, [id]: null }))
        })
    }
    return () => {
      cancelled = true
    }
  }, [operations])

  // решение по каждой операции; по умолчанию — принять
  const [decisions, setDecisions] = useState<Record<number, 'accept' | 'reject'>>({})
  const decisionAt = (index: number): 'accept' | 'reject' => decisions[index] ?? 'accept'
  const setDecision = (index: number, decision: 'accept' | 'reject') =>
    setDecisions((current) => ({ ...current, [index]: decision }))

  const applyPerCard = () => {
    const accept: number[] = []
    const reject: number[] = []
    operations.forEach((_, index) => (decisionAt(index) === 'accept' ? accept : reject).push(index))
    doResolve('approve', text.trim() || undefined, undefined, { accept, reject })
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      <GateViewHeader title={`📝 Уроки рана (${operations.length})`} already={already} onClose={onClose} />
      <div className="min-h-0 flex-1 space-y-2 overflow-auto p-3">
        {draftError && <p className="text-xs text-amber-400">{draftError}</p>}
        {!draftError && draft === null && <p className="text-sm text-zinc-500">загрузка черновика…</p>}
        {draft !== null && operations.length === 0 && (
          <p className="text-sm text-zinc-600">операций нет (NO_LESSONS или мусор) — гейт можно просто закрыть резолвом</p>
        )}
        {operations.map((operation, index) => (
          <OperationCard
            key={index}
            index={index}
            operation={operation}
            oldBody={oldBodies[operation.targetId]}
            decision={decisionAt(index)}
            onDecision={setDecision}
          />
        ))}
      </div>
      <div className="space-y-2 border-t border-zinc-800 px-3 py-2">
        <textarea
          value={text}
          onChange={(e) => setText(e.target.value)}
          rows={2}
          placeholder="ответ/комментарий (допишется в «Причину» принятых уроков)…"
          className={textareaCls}
        />
        <div className="flex flex-wrap items-center gap-2">
          {operations.length > 0 && (
            <button
              type="button"
              disabled={busy !== null}
              onClick={applyPerCard}
              title="per-card резолв: принятые применяются, отклонённые — нет (lesson_ops)"
              className="flex items-center gap-1 rounded-md bg-emerald-600/80 px-2.5 py-1 text-xs font-medium text-emerald-50 hover:bg-emerald-600 disabled:opacity-50"
            >
              <Check className="size-3" aria-hidden /> Применить выбор
            </button>
          )}
          <button
            type="button"
            disabled={busy !== null}
            onClick={() => doResolve('approve', text.trim() || undefined)}
            title="fallback: все операции принимаются (без lesson_ops)"
            className="flex items-center gap-1 rounded-md border border-emerald-700/60 px-2.5 py-1 text-xs text-emerald-300 hover:bg-emerald-500/10 disabled:opacity-50"
          >
            Принять всё
          </button>
          <button
            type="button"
            disabled={busy !== null}
            onClick={() => doResolve('reject', text.trim() || undefined)}
            title="fallback: все операции отклоняются (без lesson_ops)"
            className="flex items-center gap-1 rounded-md bg-red-600/70 px-2.5 py-1 text-xs font-medium text-red-50 hover:bg-red-600 disabled:opacity-50"
          >
            <X className="size-3" aria-hidden /> Отклонить всё
          </button>
          {error && <span className="text-xs text-red-400">{error}</span>}
        </div>
      </div>
    </div>
  )
}

export function GateView({
  gate,
  artifacts,
  onResolved,
  onClose,
}: {
  gate: Gate
  /** артефакты рана — lesson_review читает полный черновик lessons.md (T-30) */
  artifacts?: Artifact[]
  onResolved: () => void
  onClose: () => void
}) {
  if (gate.kind === 'question') {
    return <QuestionsView gate={gate} onResolved={onResolved} onClose={onClose} />
  }
  if (gate.kind === 'lesson_review') {
    return <LessonReviewView gate={gate} artifacts={artifacts ?? []} onResolved={onResolved} onClose={onClose} />
  }
  return <DecisionView gate={gate} onResolved={onResolved} onClose={onClose} />
}
