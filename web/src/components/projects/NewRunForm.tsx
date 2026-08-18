import { useMemo, useState } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { AlertTriangle } from 'lucide-react'
import type { Pipeline, Project } from '../../api/client'
import { ApiError, createRun } from '../../api/client'
import { slugify } from '../../lib/format'
import { parsePipelineStages } from '../../lib/pipelineSpec'

/*
  Форма «Новая задача» (T-16): текст задачи, глубина, ветка, notify_tg,
  read-only превью пайплайна. Сабмит — POST /runs с Idempotency-Key
  (double-click/ретрай безопасны). Ошибки preflight:
  - dirty_checkout → список мешающих файлов + «всё равно запустить» (force);
  - run_locked → показать занятую ветку.
*/

const DEPTH_OPTIONS = [
  { value: 0, label: 'quick', hint: 'быстрый план и код, минимум итераций ревью' },
  { value: 1, label: 'standard', hint: 'стандартный цикл plan → code → review → fix' },
  { value: 2, label: 'deep', hint: 'детальный план, расширенное ревью, больше итераций' },
] as const

type BranchMode = 'auto' | 'named' | 'existing'

/** details.files из Error — массив строк, если сервер его прислал */
function errorFiles(details: Record<string, unknown> | undefined): string[] {
  const files = details?.files
  if (!Array.isArray(files)) return []
  return files.filter((f): f is string => typeof f === 'string')
}

function errorBranch(details: Record<string, unknown> | undefined): string | null {
  const branch = details?.branch
  return typeof branch === 'string' ? branch : null
}

/** details.run_id из Error — id рана, держащего ветку (run_locked, F-02) */
function errorRunId(details: Record<string, unknown> | undefined): string | null {
  const runId = details?.run_id
  return typeof runId === 'string' ? runId : null
}

export function NewRunForm({
  project,
  pipeline,
  onClose,
}: {
  project: Project
  pipeline: Pipeline
  onClose: () => void
}) {
  const navigate = useNavigate()
  const [taskText, setTaskText] = useState('')
  const [depth, setDepth] = useState<number>(1)
  const [branchMode, setBranchMode] = useState<BranchMode>('auto')
  const [branchName, setBranchName] = useState('')
  const [baseBranch, setBaseBranch] = useState(project.default_branch)
  // дефолт notify_tg — из настроек проекта (F-04, notify_tg_default)
  const [notifyTg, setNotifyTg] = useState(project.notify_tg_default)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<{
    code: string
    message: string
    files: string[]
    branch: string | null
    runId: string | null
  } | null>(null)

  const stages = useMemo(() => parsePipelineStages(pipeline), [pipeline])
  const autoBranch = useMemo(() => {
    const slug = slugify(taskText)
    return slug ? `glamor/${slug}` : 'glamor/<slug из задачи>'
  }, [taskText])

  const submit = (force: boolean) => {
    const text = taskText.trim()
    if (!text || busy) return
    setBusy(true)
    setError(null)
    createRun(
      {
        project_id: project.id,
        pipeline_version_id: pipeline.id,
        task_text: text,
        base_branch: baseBranch.trim() || undefined,
        branch: branchMode === 'auto' ? undefined : branchName.trim() || undefined,
        depth,
        notify_tg: notifyTg,
        force,
      },
      // каждая попытка — свой ключ; force-ретрай не должен попасть в кэш идемпотентности
      crypto.randomUUID(),
    )
      .then((run) => {
        void navigate({ to: '/runs/$id', params: { id: run.id } })
      })
      .catch((err: unknown) => {
        if (err instanceof ApiError) {
          setError({
            code: err.code,
            message: err.message,
            files: errorFiles(err.details),
            branch: errorBranch(err.details),
            runId: errorRunId(err.details),
          })
        } else {
          setError({
            code: 'internal',
            message: err instanceof Error ? err.message : 'ошибка',
            files: [],
            branch: null,
            runId: null,
          })
        }
      })
      .finally(() => setBusy(false))
  }

  const dirtyCheckout = error?.code === 'dirty_checkout'

  return (
    <div className="rounded-lg border border-violet-500/40 bg-zinc-900 p-4">
      <h2 className="mb-3 text-sm font-semibold text-zinc-100">
        Новая задача · {pipeline.name} v{pipeline.version}
      </h2>

      <label className="mb-3 block">
        <span className="mb-1 block text-xs text-zinc-500">Текст задачи (markdown ок)</span>
        <textarea
          value={taskText}
          onChange={(e) => setTaskText(e.target.value)}
          rows={5}
          autoFocus
          placeholder="Что нужно сделать…"
          className="w-full resize-y rounded-md border border-zinc-700 bg-zinc-950 px-2.5 py-2 text-sm text-zinc-200 placeholder:text-zinc-600 focus:border-violet-500 focus:outline-none"
        />
      </label>

      <div className="mb-3">
        <span className="mb-1 block text-xs text-zinc-500">Глубина</span>
        <div className="flex gap-2">
          {DEPTH_OPTIONS.map((option) => (
            <label
              key={option.value}
              className={`flex-1 cursor-pointer rounded-md border px-3 py-2 ${
                depth === option.value ? 'border-violet-500/60 bg-violet-500/10' : 'border-zinc-700 hover:border-zinc-600'
              }`}
            >
              <input
                type="radio"
                name="depth"
                className="sr-only"
                checked={depth === option.value}
                onChange={() => setDepth(option.value)}
              />
              <div className="text-sm font-medium text-zinc-200">{option.label}</div>
              <div className="mt-0.5 text-xs text-zinc-500">{option.hint}</div>
            </label>
          ))}
        </div>
      </div>

      <div className="mb-3 grid grid-cols-2 gap-3">
        <div>
          <span className="mb-1 block text-xs text-zinc-500">Ветка</span>
          <div className="space-y-1.5 text-sm">
            <label className="flex items-start gap-2">
              <input type="radio" className="mt-1" checked={branchMode === 'auto'} onChange={() => setBranchMode('auto')} />
              <span className="text-zinc-300">
                новая от базовой
                <span className="block font-mono text-xs text-zinc-500">{autoBranch}</span>
              </span>
            </label>
            <label className="flex items-center gap-2">
              <input type="radio" checked={branchMode === 'named'} onChange={() => setBranchMode('named')} />
              <span className="text-zinc-300">новая с именем</span>
            </label>
            <label className="flex items-center gap-2">
              <input type="radio" checked={branchMode === 'existing'} onChange={() => setBranchMode('existing')} />
              <span className="text-zinc-300">существующая</span>
            </label>
            {branchMode !== 'auto' && (
              <input
                value={branchName}
                onChange={(e) => setBranchName(e.target.value)}
                placeholder={branchMode === 'named' ? 'glamor/my-feature' : 'имя существующей ветки'}
                className="w-full rounded-md border border-zinc-700 bg-zinc-950 px-2 py-1.5 font-mono text-xs text-zinc-200 placeholder:text-zinc-600 focus:border-violet-500 focus:outline-none"
              />
            )}
          </div>
        </div>
        <div>
          <label className="mb-1 block">
            <span className="mb-1 block text-xs text-zinc-500">Базовая ветка</span>
            <input
              value={baseBranch}
              onChange={(e) => setBaseBranch(e.target.value)}
              className="w-full rounded-md border border-zinc-700 bg-zinc-950 px-2 py-1.5 font-mono text-xs text-zinc-200 focus:border-violet-500 focus:outline-none"
            />
          </label>
          <label className="mt-2 flex items-center gap-2 text-sm text-zinc-300">
            <input type="checkbox" checked={notifyTg} onChange={(e) => setNotifyTg(e.target.checked)} />
            уведомлять в TG
          </label>
        </div>
      </div>

      {/* read-only превью пайплайна (M1) */}
      <div className="mb-3 rounded-md border border-zinc-800 bg-zinc-950/60 px-3 py-2">
        <span className="text-xs text-zinc-500">Этапы пайплайна:</span>
        {stages.length > 0 ? (
          <div className="mt-1 flex flex-wrap gap-1.5">
            {stages.map((stage) => (
              <span key={stage.key} className="rounded bg-zinc-800 px-2 py-0.5 font-mono text-xs text-zinc-300">
                {stage.key}
                <span className="text-zinc-600">
                  {[stage.harness, stage.model, stage.effort].filter(Boolean).join('/')}
                </span>
              </span>
            ))}
          </div>
        ) : (
          <span className="ml-2 text-xs text-zinc-600">превью недоступно (формат spec не распознан)</span>
        )}
      </div>

      {error && (
        <div className="mb-3 rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
          <div className="flex items-start gap-2">
            <AlertTriangle className="mt-0.5 size-4 shrink-0" aria-hidden />
            <div className="min-w-0 flex-1">
              <span className="font-medium">{error.code}:</span> {error.message}
              {error.files.length > 0 && (
                <ul className="mt-1.5 max-h-32 overflow-auto font-mono text-xs text-red-200/80">
                  {error.files.map((file) => (
                    <li key={file}>{file}</li>
                  ))}
                </ul>
              )}
              {error.branch && (
                <p className="mt-1 text-xs">
                  занятая ветка: <span className="font-mono">{error.branch}</span>
                  {error.runId && (
                    <>
                      {' — активный ран: '}
                      <Link to="/runs/$id" params={{ id: error.runId }} className="font-mono text-red-200 underline">
                        {error.runId.slice(0, 8)}…
                      </Link>
                    </>
                  )}
                </p>
              )}
              {dirtyCheckout && (
                <button
                  type="button"
                  disabled={busy}
                  onClick={() => submit(true)}
                  className="mt-2 rounded-md bg-red-600/80 px-2.5 py-1 text-xs font-medium text-red-50 hover:bg-red-600 disabled:opacity-50"
                >
                  всё равно запустить (force)
                </button>
              )}
            </div>
          </div>
        </div>
      )}

      <div className="flex justify-end gap-2">
        <button
          type="button"
          onClick={onClose}
          className="rounded-md px-3 py-1.5 text-sm text-zinc-400 hover:bg-zinc-800"
        >
          Отмена
        </button>
        <button
          type="button"
          disabled={busy || taskText.trim().length === 0}
          onClick={() => submit(false)}
          className="rounded-md bg-violet-600 px-3 py-1.5 text-sm font-medium text-violet-50 hover:bg-violet-500 disabled:opacity-50"
        >
          {busy ? 'запуск…' : 'Запустить'}
        </button>
      </div>
    </div>
  )
}
