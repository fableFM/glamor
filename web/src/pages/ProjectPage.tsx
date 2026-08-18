import { useCallback, useEffect, useMemo, useState } from 'react'
import { getRouteApi, Link } from '@tanstack/react-router'
import { Pencil, Plus } from 'lucide-react'
import type { Pipeline, Run } from '../api/client'
import { getProject, listProjectPipelines, listRuns } from '../api/client'
import { useProjectsStore } from '../stores/projects'
import { useRunsStore } from '../stores/runs'
import { RunStateBadge } from '../components/StateBadge'
import { NewRunForm } from '../components/projects/NewRunForm'
import { ProjectMetricsPanel } from '../components/projects/ProjectMetricsPanel'
import { ExportPipelineButton, ImportPipelineButton } from '../components/pipeline/PipelineImportExport'
import { durationBetween, firstLine, formatDateTime } from '../lib/format'
import { groupPipelinesByName } from '../lib/pipelines'

const routeApi = getRouteApi('/projects/$id')

const RUN_STATES: Run['state'][] = ['draft', 'running', 'waiting_gate', 'succeeded', 'failed', 'stopped']

/** localStorage-ключ последнего выбранного пайплайна (per project, T-16) */
const pipelineStorageKey = (projectId: number) => `glamor.pipeline.${projectId}`

/** Экран «Проект» (T-16): селектор пайплайна + раны выбранного пайплайна + «Новая задача». */
export function ProjectPage() {
  const { id } = routeApi.useParams()
  const projectId = Number(id)
  const project = useProjectsStore((s) => s.items.find((p) => p.id === projectId))
  const runsById = useRunsStore((s) => s.byId)

  const [pipelines, setPipelines] = useState<Pipeline[] | null>(null)
  const [selectedPipelineId, setSelectedPipelineId] = useState<number | null>(null)
  const [stateFilter, setStateFilter] = useState<Run['state'] | ''>('')
  const [formOpen, setFormOpen] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [runsError, setRunsError] = useState<string | null>(null)
  /** вкладка контента: список ранов или статистика проекта (T-24) */
  const [view, setView] = useState<'runs' | 'metrics'>('runs')

  // таб = пайплайн (имя), внутри — версии; активна последняя (fix-task-2 П.5)
  const pipelineGroups = useMemo(() => groupPipelinesByName(pipelines ?? []), [pipelines])
  const activeGroup = pipelineGroups.find((g) => g.versions.some((v) => v.id === selectedPipelineId)) ?? null

  // проект мог быть не в списке (прямой заход по ссылке) — догружаем
  useEffect(() => {
    if (project) return
    getProject(projectId)
      .then((detail) => useProjectsStore.getState().upsert(detail))
      .catch(() => undefined)
  }, [projectId, project])

  // пайплайны проекта + восстановление последнего выбранного
  const reloadPipelines = useCallback(() => {
    listProjectPipelines(projectId)
      .then((items) => {
        setPipelines(items)
        setSelectedPipelineId((current) => {
          if (current !== null && items.some((p) => p.id === current)) return current
          const saved = Number(globalThis.localStorage?.getItem(pipelineStorageKey(projectId)))
          const initial = items.find((p) => p.id === saved) ?? items[0]
          return initial?.id ?? null
        })
      })
      .catch((err: unknown) => setError(err instanceof Error ? err.message : 'ошибка загрузки пайплайнов'))
  }, [projectId])

  useEffect(() => {
    setPipelines(null)
    setSelectedPipelineId(null)
    setFormOpen(false)
    reloadPipelines()
  }, [reloadPipelines])

  // раны выбранной версии пайплайна (REST-снапшот; дальше состояния живут на WS)
  useEffect(() => {
    if (selectedPipelineId === null) return
    globalThis.localStorage?.setItem(pipelineStorageKey(projectId), String(selectedPipelineId))
    setRunsError(null)
    listRuns({ project_id: projectId, pipeline_id: selectedPipelineId, limit: 100 })
      .then((items) => useRunsStore.getState().upsertMany(items))
      .catch((err: unknown) => {
        // фоновая подгрузка списка: не ломаем экран, но и не глотаем молча
        console.error('listRuns failed', err)
        setRunsError(err instanceof Error ? err.message : 'ошибка загрузки ранов')
      })
  }, [projectId, selectedPipelineId])

  const selectedPipeline = pipelines?.find((p) => p.id === selectedPipelineId) ?? null

  const runs = useMemo(
    () =>
      Object.values(runsById)
        .filter((r) => r.project_id === projectId && r.pipeline_version_id === selectedPipelineId)
        .filter((r) => stateFilter === '' || r.state === stateFilter)
        .sort((a, b) => b.created_at.localeCompare(a.created_at)),
    [runsById, projectId, selectedPipelineId, stateFilter],
  )

  if (error) return <p className="text-sm text-red-400">{error}</p>

  return (
    <div className="mx-auto max-w-4xl">
      <div className="mb-4">
        <h1 className="text-xl font-semibold text-zinc-100">{project?.name ?? `Проект #${id}`}</h1>
        {project && (
          <p className="mt-0.5 text-xs text-zinc-500">
            <span className="font-mono">{project.path}</span> · ветка {project.default_branch}
          </p>
        )}
      </div>

      {/* селектор пайплайна (таб = имя, версии — переключателем) + редактор + import/export (T-20/T-21) */}
      {pipelines && pipelines.length > 0 && (
        <div className="mb-4 flex flex-wrap items-center gap-1 border-b border-zinc-800 pb-0">
          {pipelineGroups.map((group) => (
            <button
              key={group.name}
              type="button"
              onClick={() => setSelectedPipelineId(group.latest.id)}
              className={`rounded-t-md px-3 py-1.5 text-sm ${
                group.name === activeGroup?.name
                  ? 'border-b-2 border-violet-500 bg-zinc-900 font-medium text-zinc-100'
                  : 'text-zinc-500 hover:text-zinc-300'
              }`}
            >
              {group.name}
            </button>
          ))}
          {/* переключатель версии внутри таба — только если версий больше одной */}
          {activeGroup && activeGroup.versions.length > 1 && (
            <select
              value={selectedPipelineId ?? undefined}
              onChange={(e) => setSelectedPipelineId(Number(e.target.value))}
              className="mb-1 ml-1 rounded-md border border-zinc-700 bg-zinc-900 px-1.5 py-0.5 text-xs text-zinc-400 focus:border-violet-500 focus:outline-none"
              title="версия пайплайна (раны фильтруются по версии)"
            >
              {activeGroup.versions.map((v) => (
                <option key={v.id} value={v.id}>
                  v{v.version}
                  {v.id === activeGroup.latest.id ? ' (последняя)' : ''}
                </option>
              ))}
            </select>
          )}
          <span className="mb-1 ml-auto flex items-center gap-1.5">
            {selectedPipeline && (
              <>
                <Link
                  to="/pipelines/$id/edit"
                  params={{ id: String(selectedPipeline.id) }}
                  className="flex items-center gap-1 rounded-md border border-zinc-700 px-2 py-1 text-xs text-zinc-400 hover:bg-zinc-800 hover:text-zinc-200"
                >
                  <Pencil className="size-3.5" aria-hidden /> Редактор
                </Link>
                <ExportPipelineButton pipeline={selectedPipeline} />
              </>
            )}
            <ImportPipelineButton onImported={reloadPipelines} />
          </span>
        </div>
      )}
      {pipelines && pipelines.length === 0 && (
        <div className="mb-4 flex items-center gap-3">
          <p className="text-sm text-zinc-500">пайплайнов нет — импортируйте YAML или дождитесь сидинга T-17</p>
          <ImportPipelineButton onImported={reloadPipelines} />
        </div>
      )}

      <div className="mb-3 flex items-center gap-3">
        {/* переключатель контента: раны / метрики проекта (T-24) */}
        <div className="flex gap-1 rounded-md border border-zinc-800 p-0.5">
          {(
            [
              ['runs', 'Раны'],
              ['metrics', 'Метрики'],
            ] as const
          ).map(([value, label]) => (
            <button
              key={value}
              type="button"
              onClick={() => setView(value)}
              className={`rounded px-2.5 py-1 text-xs ${
                view === value ? 'bg-zinc-800 text-zinc-100' : 'text-zinc-500 hover:text-zinc-300'
              }`}
            >
              {label}
            </button>
          ))}
        </div>
        {view === 'runs' && (
          <>
            <select
              value={stateFilter}
              onChange={(e) => setStateFilter(e.target.value as Run['state'] | '')}
              className="rounded-md border border-zinc-700 bg-zinc-900 px-2 py-1 text-xs text-zinc-300 focus:border-violet-500 focus:outline-none"
            >
              <option value="">все состояния</option>
              {RUN_STATES.map((state) => (
                <option key={state} value={state}>
                  {state}
                </option>
              ))}
            </select>
            <span className="text-xs text-zinc-600">{runs.length} ранов</span>
          </>
        )}
        {project && selectedPipeline && (
          <button
            type="button"
            onClick={() => setFormOpen((v) => !v)}
            className="ml-auto flex items-center gap-1.5 rounded-md bg-violet-600 px-3 py-1.5 text-sm font-medium text-violet-50 hover:bg-violet-500"
          >
            <Plus className="size-4" aria-hidden />
            Новая задача
          </button>
        )}
      </div>

      {formOpen && project && selectedPipeline && (
        <div className="mb-4">
          <NewRunForm project={project} pipeline={selectedPipeline} onClose={() => setFormOpen(false)} />
        </div>
      )}

      {view === 'metrics' && <ProjectMetricsPanel projectId={projectId} />}

      {/* раны выбранной версии пайплайна */}
      {view === 'runs' && runsError && <p className="mb-3 text-xs text-red-400">не удалось загрузить раны: {runsError}</p>}
      {view === 'runs' &&
        (runs.length === 0 ? (
          <p className="text-sm text-zinc-500">ранов нет</p>
        ) : (
        <ul className="space-y-1.5">
          {runs.map((run) => (
            <li key={run.id}>
              <Link
                to="/runs/$id"
                params={{ id: run.id }}
                className="flex items-center gap-3 rounded-lg border border-zinc-800 bg-zinc-900/60 px-4 py-2.5 hover:border-zinc-700"
              >
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-sm text-zinc-200">{firstLine(run.task_text, 90)}</span>
                  <span className="mt-0.5 block font-mono text-xs text-zinc-600">{run.branch}</span>
                </span>
                <RunStateBadge state={run.state} />
                <span className="w-24 shrink-0 text-right text-xs text-zinc-500">
                  {durationBetween(run.created_at, run.finished_at) ?? '—'}
                </span>
                <span className="w-20 shrink-0 text-right text-xs text-zinc-600">{formatDateTime(run.created_at)}</span>
              </Link>
            </li>
          ))}
        </ul>
        ))}
    </div>
  )
}
