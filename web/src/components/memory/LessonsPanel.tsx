import { useCallback, useEffect, useState } from 'react'
import { Check, X } from 'lucide-react'
import type { Lesson, LessonDetail } from '../../api/client'
import { getLesson, listLessons, patchLesson } from '../../api/client'
import { Markdown } from '../Markdown'
import { formatDateTime } from '../../lib/format'

/*
  Вкладка «Уроки» (T-29): список уроков causal memory с фильтрами
  по status/scope, просмотр содержимого (markdown), смена статуса
  confirm/reject прямо из списка или из просмотра.
*/

const STATUS_LABELS: Record<Lesson['status'], string> = {
  proposed: 'предложен',
  confirmed: 'подтверждён',
  rejected: 'отклонён',
  superseded: 'вытеснен',
}

const STATUS_CLASSES: Record<Lesson['status'], string> = {
  proposed: 'bg-amber-500/15 text-amber-300',
  confirmed: 'bg-emerald-500/15 text-emerald-300',
  rejected: 'bg-red-500/15 text-red-300',
  superseded: 'bg-zinc-500/15 text-zinc-400',
}

const STATUS_FILTERS: { value: Lesson['status'] | ''; label: string }[] = [
  { value: '', label: 'все статусы' },
  { value: 'proposed', label: 'предложенные' },
  { value: 'confirmed', label: 'подтверждённые' },
  { value: 'rejected', label: 'отклонённые' },
  { value: 'superseded', label: 'вытесненные' },
]

const SCOPE_FILTERS: { value: Lesson['scope'] | ''; label: string }[] = [
  { value: '', label: 'все' },
  { value: 'project', label: 'проектные' },
  { value: 'global', label: 'глобальные' },
]

function LessonView({
  lesson,
  onStatus,
  onClose,
}: {
  lesson: Lesson
  onStatus: (status: Lesson['status']) => void
  onClose: () => void
}) {
  const [detail, setDetail] = useState<LessonDetail | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    getLesson(lesson.id)
      .then(setDetail)
      .catch((err: unknown) => setError(err instanceof Error ? err.message : 'ошибка загрузки'))
  }, [lesson.id])

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex items-center gap-2 border-b border-zinc-800 px-4 py-2">
        <span className="min-w-0 flex-1 truncate text-sm font-medium text-zinc-100">{lesson.title}</span>
        <span className={`rounded-full px-2 py-0.5 text-xs ${STATUS_CLASSES[lesson.status]}`}>
          {STATUS_LABELS[lesson.status]}
        </span>
        {lesson.status === 'proposed' && (
          <>
            <button
              type="button"
              onClick={() => onStatus('confirmed')}
              className="flex items-center gap-1 rounded-md bg-emerald-600/80 px-2 py-1 text-xs font-medium text-emerald-50 hover:bg-emerald-600"
            >
              <Check className="size-3" aria-hidden /> Подтвердить
            </button>
            <button
              type="button"
              onClick={() => onStatus('rejected')}
              className="flex items-center gap-1 rounded-md bg-red-600/70 px-2 py-1 text-xs font-medium text-red-50 hover:bg-red-600"
            >
              <X className="size-3" aria-hidden /> Отклонить
            </button>
          </>
        )}
        <button type="button" onClick={onClose} className="rounded-md px-2 py-1 text-xs text-zinc-500 hover:bg-zinc-800">
          закрыть
        </button>
      </div>
      <div className="min-h-0 flex-1 overflow-auto p-4">
        <p className="mb-3 text-xs text-zinc-600">
          {lesson.scope}
          {lesson.stage_key ? ` · этап ${lesson.stage_key}` : ''} · применён {lesson.applied_count ?? 0} раз ·{' '}
          {formatDateTime(lesson.created_at)}
        </p>
        {error && <p className="text-xs text-red-400">{error}</p>}
        {detail ? <Markdown>{detail.content}</Markdown> : !error && <p className="text-sm text-zinc-500">загрузка…</p>}
      </div>
    </div>
  )
}

export function LessonsPanel() {
  const [statusFilter, setStatusFilter] = useState<Lesson['status'] | ''>('')
  const [scopeFilter, setScopeFilter] = useState<Lesson['scope'] | ''>('')
  const [lessons, setLessons] = useState<Lesson[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [selected, setSelected] = useState<Lesson | null>(null)

  const reload = useCallback(() => {
    setError(null)
    listLessons({
      ...(statusFilter ? { status: statusFilter } : {}),
      ...(scopeFilter ? { scope: scopeFilter } : {}),
    })
      .then(setLessons)
      .catch((err: unknown) => setError(err instanceof Error ? err.message : 'ошибка загрузки уроков'))
  }, [statusFilter, scopeFilter])

  useEffect(() => reload(), [reload])

  const changeStatus = (lesson: Lesson, status: Lesson['status']) => {
    patchLesson(lesson.id, { status })
      .then((updated) => {
        setLessons((list) => (list ?? []).map((l) => (l.id === lesson.id ? { ...l, ...updated } : l)))
        setSelected((current) => (current?.id === lesson.id ? { ...current, ...updated } : current))
      })
      .catch((err: unknown) => setError(err instanceof Error ? err.message : 'ошибка смены статуса'))
  }

  if (selected) {
    return <LessonView lesson={selected} onStatus={(status) => changeStatus(selected, status)} onClose={() => setSelected(null)} />
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex items-center gap-2 border-b border-zinc-800 px-3 py-2">
        <select
          value={statusFilter}
          onChange={(e) => setStatusFilter(e.target.value as Lesson['status'] | '')}
          className="rounded-md border border-zinc-700 bg-zinc-900 px-2 py-1 text-xs text-zinc-300 focus:border-violet-500 focus:outline-none"
        >
          {STATUS_FILTERS.map((f) => (
            <option key={f.value} value={f.value}>{f.label}</option>
          ))}
        </select>
        <select
          value={scopeFilter}
          onChange={(e) => setScopeFilter(e.target.value as Lesson['scope'] | '')}
          className="rounded-md border border-zinc-700 bg-zinc-900 px-2 py-1 text-xs text-zinc-300 focus:border-violet-500 focus:outline-none"
        >
          {SCOPE_FILTERS.map((f) => (
            <option key={f.value} value={f.value}>{f.label}</option>
          ))}
        </select>
        <span className="ml-auto text-xs text-zinc-600">{lessons?.length ?? 0} уроков</span>
      </div>
      {error && <p className="px-3 py-1.5 text-xs text-red-400">{error}</p>}
      <div className="min-h-0 flex-1 overflow-auto">
        {!lessons ? (
          <p className="p-3 text-sm text-zinc-500">загрузка…</p>
        ) : lessons.length === 0 ? (
          <p className="p-3 text-sm text-zinc-600">уроков нет</p>
        ) : (
          <table className="w-full text-xs">
            <thead>
              <tr className="text-left text-zinc-600">
                <th className="px-3 pb-1.5 font-medium">урок</th>
                <th className="pb-1.5 pr-3 font-medium">scope</th>
                <th className="pb-1.5 pr-3 font-medium">статус</th>
                <th className="pb-1.5 pr-3 font-medium">применён</th>
                <th className="pb-1.5 pr-3 font-medium">создан</th>
                <th className="pb-1.5 pr-3 font-medium" />
              </tr>
            </thead>
            <tbody>
              {lessons.map((lesson) => (
                <tr key={lesson.id} className="border-t border-zinc-800/60 hover:bg-zinc-800/40">
                  <td className="px-3 py-1.5">
                    <button
                      type="button"
                      onClick={() => setSelected(lesson)}
                      className="max-w-md truncate text-left text-zinc-200 hover:text-violet-300"
                    >
                      {lesson.title}
                    </button>
                  </td>
                  <td className="py-1.5 pr-3 text-zinc-500">{lesson.scope}</td>
                  <td className="py-1.5 pr-3">
                    <span className={`rounded-full px-1.5 py-0.5 ${STATUS_CLASSES[lesson.status]}`}>
                      {STATUS_LABELS[lesson.status]}
                    </span>
                  </td>
                  <td className="py-1.5 pr-3 font-mono text-zinc-400">{lesson.applied_count ?? 0}×</td>
                  <td className="py-1.5 pr-3 text-zinc-600">{formatDateTime(lesson.created_at)}</td>
                  <td className="py-1.5 pr-3">
                    {lesson.status === 'proposed' && (
                      <span className="flex gap-1">
                        <button
                          type="button"
                          title="подтвердить"
                          onClick={() => changeStatus(lesson, 'confirmed')}
                          className="rounded p-1 text-emerald-400 hover:bg-emerald-500/15"
                        >
                          <Check className="size-3.5" aria-hidden />
                        </button>
                        <button
                          type="button"
                          title="отклонить"
                          onClick={() => changeStatus(lesson, 'rejected')}
                          className="rounded p-1 text-red-400 hover:bg-red-500/15"
                        >
                          <X className="size-3.5" aria-hidden />
                        </button>
                      </span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  )
}
