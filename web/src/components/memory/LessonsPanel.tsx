import { useCallback, useEffect, useState } from 'react'
import { Check, X } from 'lucide-react'
import type { Lesson, LessonConsolidation, LessonDetail } from '../../api/client'
import { getLesson, lessonConsolidation, listLessons, patchLesson } from '../../api/client'
import { Markdown } from '../Markdown'
import { formatDateTime } from '../../lib/format'

/*
  Вкладка «Уроки» (T-29 + T-30): список уроков causal memory с фильтрами
  по status/scope/kind и «требуют внимания» (attention), просмотр содержимого
  (markdown), смена статуса прямо из списка или из просмотра.
  T-30: бейдж kind (behavior/vendor), статус outdated («версия изменилась»),
  vendor-метаданные (vendor@version, area), здоровье урока
  (applied_success_count vs relapse_count), секция «Консолидация» —
  кандидаты из GET /lessons/consolidation, действия только ручные.
*/

const STATUS_LABELS: Record<Lesson['status'], string> = {
  proposed: 'предложен',
  confirmed: 'подтверждён',
  rejected: 'отклонён',
  superseded: 'вытеснен',
  outdated: 'устарел',
}

const STATUS_CLASSES: Record<Lesson['status'], string> = {
  proposed: 'bg-amber-500/15 text-amber-300',
  confirmed: 'bg-emerald-500/15 text-emerald-300',
  rejected: 'bg-red-500/15 text-red-300',
  superseded: 'bg-zinc-500/15 text-zinc-400',
  outdated: 'bg-orange-500/15 text-orange-300',
}

const STATUS_FILTERS: { value: Lesson['status'] | ''; label: string }[] = [
  { value: '', label: 'все статусы' },
  { value: 'proposed', label: 'предложенные' },
  { value: 'confirmed', label: 'подтверждённые' },
  { value: 'rejected', label: 'отклонённые' },
  { value: 'superseded', label: 'вытесненные' },
  { value: 'outdated', label: 'устаревшие' },
]

const SCOPE_FILTERS: { value: Lesson['scope'] | ''; label: string }[] = [
  { value: '', label: 'все' },
  { value: 'project', label: 'проектные' },
  { value: 'global', label: 'глобальные' },
]

const KIND_FILTERS: { value: NonNullable<Lesson['kind']> | ''; label: string }[] = [
  { value: '', label: 'все виды' },
  { value: 'behavior', label: 'behavior' },
  { value: 'vendor', label: 'vendor' },
]

/** статусы, доступные ручной смене (enum PATCH /lessons/{id}) */
type SettableStatus = 'proposed' | 'confirmed' | 'rejected' | 'superseded'

/** бейдж статуса; outdated — с пояснением «версия изменилась» (T-30) */
function StatusBadge({ status }: { status: Lesson['status'] }) {
  return (
    <span
      className={`rounded-full px-1.5 py-0.5 ${STATUS_CLASSES[status]}`}
      title={status === 'outdated' ? 'версия изменилась: vendor_version урока разошлась с lockfile проекта — перепроверить' : undefined}
    >
      {STATUS_LABELS[status]}
    </span>
  )
}

/** бейдж вида урока (T-30) */
function KindBadge({ kind }: { kind: Lesson['kind'] }) {
  if (kind === 'vendor') {
    return <span className="rounded bg-sky-500/15 px-1.5 py-0.5 text-sky-300">vendor</span>
  }
  return <span className="rounded bg-zinc-700/40 px-1.5 py-0.5 text-zinc-500">behavior</span>
}

/**
 * Здоровье урока (T-30): успешные исходы ранов с инъекцией vs рецидивы.
 * relapse >= applied_success — «урок не работает», подсветка.
 */
function HealthCell({ lesson }: { lesson: Lesson }) {
  const success = lesson.applied_success_count ?? 0
  const relapse = lesson.relapse_count ?? 0
  const unhealthy = relapse >= success && relapse > 0
  return (
    <span
      className={`font-mono ${unhealthy ? 'text-red-400' : 'text-zinc-400'}`}
      title={`успешных исходов: ${success}, рецидивов: ${relapse}${unhealthy ? ' — урок не работает, уточнить формулировку?' : ''}`}
    >
      ✓{success} / ✗{relapse}
    </span>
  )
}

/** vendor-метаданные урока (T-30): вендор@версия · область */
function vendorMeta(lesson: Lesson): string {
  if (lesson.kind !== 'vendor') return ''
  const name = [lesson.vendor, lesson.vendor_version].filter(Boolean).join('@')
  return [name, lesson.area].filter(Boolean).join(' · ')
}

function LessonView({
  lesson,
  onStatus,
  onClose,
}: {
  lesson: Lesson
  onStatus: (status: SettableStatus) => void
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
        <KindBadge kind={lesson.kind} />
        <span className="min-w-0 flex-1 truncate text-sm font-medium text-zinc-100">{lesson.title}</span>
        <StatusBadge status={lesson.status} />
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
        {vendorMeta(lesson) !== '' && <p className="mb-3 text-xs text-sky-300/80">{vendorMeta(lesson)}</p>}
        <p className="mb-3 text-xs text-zinc-600">
          здоровье: <HealthCell lesson={lesson} />
          {lesson.superseded_by ? ` · вытеснен уроком ${lesson.superseded_by}` : ''}
          {lesson.related && lesson.related.length > 0 ? ` · связан: ${lesson.related.join(', ')}` : ''}
        </p>
        {error && <p className="text-xs text-red-400">{error}</p>}
        {detail ? <Markdown>{detail.content}</Markdown> : !error && <p className="text-sm text-zinc-500">загрузка…</p>}
      </div>
    </div>
  )
}

/** строка урока в секциях консолидации: переход к уроку + ручная смена статуса */
function ConsolidationRow({
  lesson,
  hint,
  onOpen,
  onStatus,
}: {
  lesson: Lesson
  hint?: string
  onOpen: (lesson: Lesson) => void
  onStatus: (lesson: Lesson, status: SettableStatus) => void
}) {
  return (
    <li className="flex items-center gap-2 border-t border-zinc-800/60 px-3 py-1.5 text-xs">
      <KindBadge kind={lesson.kind} />
      <button
        type="button"
        onClick={() => onOpen(lesson)}
        className="min-w-0 flex-1 truncate text-left text-zinc-200 hover:text-violet-300"
      >
        {lesson.title}
      </button>
      {hint && <span className="shrink-0 text-zinc-600">{hint}</span>}
      <HealthCell lesson={lesson} />
      <StatusBadge status={lesson.status} />
      <select
        value={lesson.status}
        onChange={(e) => onStatus(lesson, e.target.value as SettableStatus)}
        title="сменить статус (ручная консолидация)"
        className="shrink-0 rounded-md border border-zinc-700 bg-zinc-900 px-1 py-0.5 text-xs text-zinc-300 focus:border-violet-500 focus:outline-none"
      >
        {(['proposed', 'confirmed', 'rejected', 'superseded'] as const).map((status) => (
          <option key={status} value={status}>{STATUS_LABELS[status]}</option>
        ))}
      </select>
    </li>
  )
}

/*
  Секция «Консолидация» (T-30): кандидаты из GET /lessons/consolidation —
  дубли, superseded старше N дней, нездоровые (relapse >= applied).
  Никакой авто-консолидации: действия только ручные (открыть / сменить статус).
*/
function ConsolidationView({ onOpen }: { onOpen: (lesson: Lesson) => void }) {
  const [days, setDays] = useState(30)
  const [data, setData] = useState<LessonConsolidation | null>(null)
  const [error, setError] = useState<string | null>(null)

  const reload = useCallback(() => {
    setError(null)
    lessonConsolidation(days)
      .then(setData)
      .catch((err: unknown) => setError(err instanceof Error ? err.message : 'ошибка загрузки'))
  }, [days])

  useEffect(() => reload(), [reload])

  // ручная смена статуса кандидата — после патча перечитываем кандидатов
  const changeStatus = (lesson: Lesson, status: SettableStatus) => {
    patchLesson(lesson.id, { status })
      .then(() => reload())
      .catch((err: unknown) => setError(err instanceof Error ? err.message : 'ошибка смены статуса'))
  }

  const empty =
    data !== null &&
    data.duplicates.length === 0 &&
    data.stale_superseded.length === 0 &&
    data.unhealthy.length === 0

  return (
    <div className="min-h-0 flex-1 overflow-auto">
      <div className="flex items-center gap-2 border-b border-zinc-800 px-3 py-2 text-xs text-zinc-500">
        <span>superseded старше</span>
        <input
          type="number"
          min={1}
          value={days}
          onChange={(e) => setDays(Math.max(1, Number(e.target.value) || 1))}
          className="w-16 rounded-md border border-zinc-700 bg-zinc-950 px-1.5 py-0.5 text-xs text-zinc-200 focus:border-violet-500 focus:outline-none"
        />
        <span>дней — кандидаты на prune</span>
        <button type="button" onClick={reload} className="ml-auto text-violet-400 hover:underline">
          обновить
        </button>
      </div>
      {error && <p className="px-3 py-1.5 text-xs text-red-400">{error}</p>}
      {!data ? (
        !error && <p className="p-3 text-sm text-zinc-500">загрузка…</p>
      ) : empty ? (
        <p className="p-3 text-sm text-zinc-600">кандидатов на консолидацию нет</p>
      ) : (
        <>
          <p className="px-3 pt-2 text-xs font-semibold uppercase tracking-wide text-zinc-500">
            Возможные дубли ({data.duplicates.length})
          </p>
          {data.duplicates.length === 0 && <p className="px-3 py-1 text-xs text-zinc-700">нет</p>}
          <ul>
            {data.duplicates.map((pair) => (
              <ConsolidationRow
                key={`${pair.lesson.id}:${pair.similar_to.id}`}
                lesson={pair.lesson}
                hint={`похож на «${pair.similar_to.title}» (${pair.reason})`}
                onOpen={onOpen}
                onStatus={changeStatus}
              />
            ))}
          </ul>

          <p className="px-3 pt-3 text-xs font-semibold uppercase tracking-wide text-zinc-500">
            Вытесненные старше {days} дн. ({data.stale_superseded.length})
          </p>
          {data.stale_superseded.length === 0 && <p className="px-3 py-1 text-xs text-zinc-700">нет</p>}
          <ul>
            {data.stale_superseded.map((lesson) => (
              <ConsolidationRow
                key={lesson.id}
                lesson={lesson}
                hint={lesson.superseded_by ? `заменён на ${lesson.superseded_by}` : undefined}
                onOpen={onOpen}
                onStatus={changeStatus}
              />
            ))}
          </ul>

          <p className="px-3 pt-3 text-xs font-semibold uppercase tracking-wide text-zinc-500">
            Нездоровые (relapse ≥ applied) ({data.unhealthy.length})
          </p>
          {data.unhealthy.length === 0 && <p className="px-3 py-1 text-xs text-zinc-700">нет</p>}
          <ul className="pb-3">
            {data.unhealthy.map((lesson) => (
              <ConsolidationRow
                key={lesson.id}
                lesson={lesson}
                hint="урок инжектят, а область ломается снова"
                onOpen={onOpen}
                onStatus={changeStatus}
              />
            ))}
          </ul>
        </>
      )}
    </div>
  )
}

export function LessonsPanel() {
  const [statusFilter, setStatusFilter] = useState<Lesson['status'] | ''>('')
  const [scopeFilter, setScopeFilter] = useState<Lesson['scope'] | ''>('')
  const [kindFilter, setKindFilter] = useState<NonNullable<Lesson['kind']> | ''>('')
  const [attentionOnly, setAttentionOnly] = useState(false)
  const [view, setView] = useState<'list' | 'consolidation'>('list')
  const [lessons, setLessons] = useState<Lesson[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [selected, setSelected] = useState<Lesson | null>(null)

  const reload = useCallback(() => {
    setError(null)
    listLessons({
      ...(statusFilter ? { status: statusFilter } : {}),
      ...(scopeFilter ? { scope: scopeFilter } : {}),
      ...(kindFilter ? { kind: kindFilter } : {}),
      ...(attentionOnly ? { attention: true } : {}),
    })
      .then(setLessons)
      .catch((err: unknown) => setError(err instanceof Error ? err.message : 'ошибка загрузки уроков'))
  }, [statusFilter, scopeFilter, kindFilter, attentionOnly])

  useEffect(() => reload(), [reload])

  const changeStatus = (lesson: Lesson, status: SettableStatus) => {
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
      <div className="flex flex-wrap items-center gap-2 border-b border-zinc-800 px-3 py-2">
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
        <select
          value={kindFilter}
          onChange={(e) => setKindFilter(e.target.value as NonNullable<Lesson['kind']> | '')}
          className="rounded-md border border-zinc-700 bg-zinc-900 px-2 py-1 text-xs text-zinc-300 focus:border-violet-500 focus:outline-none"
        >
          {KIND_FILTERS.map((f) => (
            <option key={f.value} value={f.value}>{f.label}</option>
          ))}
        </select>
        <label
          className="flex items-center gap-1.5 text-xs text-zinc-400"
          title="устаревшие (версия вендора разошлась с lockfile) или relapse ≥ applied (урок не работает)"
        >
          <input type="checkbox" checked={attentionOnly} onChange={(e) => setAttentionOnly(e.target.checked)} />
          требуют внимания
        </label>
        <button
          type="button"
          onClick={() => setView(view === 'list' ? 'consolidation' : 'list')}
          className={`ml-auto rounded-md px-2 py-1 text-xs ${
            view === 'consolidation' ? 'bg-zinc-800 text-zinc-100' : 'text-zinc-500 hover:bg-zinc-800/60 hover:text-zinc-300'
          }`}
        >
          Консолидация
        </button>
        <span className="text-xs text-zinc-600">{lessons?.length ?? 0} уроков</span>
      </div>
      {error && <p className="px-3 py-1.5 text-xs text-red-400">{error}</p>}
      {view === 'consolidation' ? (
        <ConsolidationView onOpen={setSelected} />
      ) : (
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
                  <th className="pb-1.5 pr-3 font-medium">вид</th>
                  <th className="pb-1.5 pr-3 font-medium">scope</th>
                  <th className="pb-1.5 pr-3 font-medium">статус</th>
                  <th className="pb-1.5 pr-3 font-medium" title="успешные исходы / рецидивы (T-30)">здоровье</th>
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
                      {vendorMeta(lesson) !== '' && (
                        <span className="block max-w-md truncate text-zinc-600">{vendorMeta(lesson)}</span>
                      )}
                    </td>
                    <td className="py-1.5 pr-3">
                      <KindBadge kind={lesson.kind} />
                    </td>
                    <td className="py-1.5 pr-3 text-zinc-500">{lesson.scope}</td>
                    <td className="py-1.5 pr-3">
                      <StatusBadge status={lesson.status} />
                    </td>
                    <td className="py-1.5 pr-3">
                      <HealthCell lesson={lesson} />
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
      )}
    </div>
  )
}
