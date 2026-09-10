import { useCallback, useEffect, useMemo, useState } from 'react'
import { getRouteApi, Link, useNavigate } from '@tanstack/react-router'
import { ArrowLeft, Plus, Save, Trash2 } from 'lucide-react'
import type { Pipeline } from '../api/client'
import { createPipeline, createPipelineVersion, getPipeline, listPipelineVersions } from '../api/client'
import type { PipelineSpec, SpecStage } from '../lib/pipelineModel'
import { emptyJanitor, emptyStage, GROUP_ON_FAILURE, parseSpec, serializeSpec, validateSpec } from '../lib/pipelineModel'
import { PipelineCanvas } from '../components/pipeline/PipelineCanvas'
import { StageInspector } from '../components/pipeline/StageInspector'
import { ExportPipelineButton } from '../components/pipeline/PipelineImportExport'
import { useProjectsStore } from '../stores/projects'

const routeApi = getRouteApi('/pipelines/$id/edit')

/*
  Редактор пайплайна (T-20): канва xyflow + инспектор + петля/final gate.
  Модель версий (T-21): версии неизменяемы, «Сохранить» = новая версия
  (POST /pipelines/{id}/versions от последней), старая версия открывается
  read-only, кнопка «взять за основу» копирует её spec в черновик.
*/

function LoopPanel({
  spec,
  readOnly,
  onChange,
}: {
  spec: PipelineSpec
  readOnly: boolean
  onChange: (spec: PipelineSpec) => void
}) {
  const keys = spec.stages.map((s) => s.key)
  const loop = spec.loop

  return (
    <div className="space-y-2 p-3">
      <div className="flex items-center justify-between">
        <span className="text-xs font-semibold uppercase tracking-wide text-zinc-500">Loop-петля</span>
        {loop ? (
          <button type="button" disabled={readOnly} onClick={() => onChange({ ...spec, loop: null })}
            className="text-xs text-red-400 hover:underline disabled:opacity-50">
            убрать
          </button>
        ) : (
          <button
            type="button"
            disabled={readOnly || keys.length < 2}
            onClick={() => onChange({ ...spec, loop: { from: keys[keys.length - 1] ?? '', to: keys[0] ?? '', max_iters: 4 } })}
            className="text-xs text-violet-400 hover:underline disabled:opacity-50"
          >
            добавить
          </button>
        )}
      </div>
      {loop && (
        <div className="grid grid-cols-3 gap-2">
          {(['from', 'to'] as const).map((field) => (
            <label key={field} className="block">
              <span className="mb-1 block text-xs text-zinc-500">{field}</span>
              <select
                value={loop[field]}
                disabled={readOnly}
                onChange={(e) => onChange({ ...spec, loop: { ...loop, [field]: e.target.value } })}
                className="w-full rounded-md border border-zinc-700 bg-zinc-950 px-1.5 py-1 font-mono text-xs text-zinc-200 focus:border-violet-500 focus:outline-none disabled:opacity-50"
              >
                <option value="">—</option>
                {keys.map((key) => (
                  <option key={key} value={key}>{key}</option>
                ))}
              </select>
            </label>
          ))}
          <label className="block">
            <span className="mb-1 block text-xs text-zinc-500">max_iters</span>
            <input
              type="number"
              min={1}
              value={loop.max_iters}
              disabled={readOnly}
              onChange={(e) => onChange({ ...spec, loop: { ...loop, max_iters: Number(e.target.value) } })}
              className="w-full rounded-md border border-zinc-700 bg-zinc-950 px-1.5 py-1 text-xs text-zinc-200 focus:border-violet-500 focus:outline-none disabled:opacity-50"
            />
          </label>
        </div>
      )}
      <label className="flex items-center gap-2 pt-1 text-xs text-zinc-400">
        <input
          type="checkbox"
          checked={spec.final_gate}
          disabled={readOnly}
          onChange={(e) => onChange({ ...spec, final_gate: e.target.checked })}
        />
        финальный гейт (final_review) в конце пайплайна
      </label>
      {/* гарантия формирования уроков (T-30, D-81): настройка spec, а не нода графа;
          при включении ядро гарантирует distill-этап, даже если его удалили из пайплайна */}
      <label
        className="flex items-center gap-2 pt-1 text-xs text-zinc-400"
        title="при включении supervisor гарантирует distill-этап и гейт lesson_review; выключение — мастер-выключатель (distill пропускается, трейс не собирается)"
      >
        <input
          type="checkbox"
          checked={spec.lessons}
          disabled={readOnly}
          onChange={(e) => onChange({ ...spec, lessons: e.target.checked })}
        />
        формировать уроки (distill + гейт lesson_review)
      </label>

      {/* параллельные группы (T-28): объявление + политика on_failure */}
      <div className="border-t border-zinc-800 pt-2">
        <div className="flex items-center justify-between">
          <span className="text-xs font-semibold uppercase tracking-wide text-zinc-500">Параллельные группы</span>
          <button
            type="button"
            disabled={readOnly}
            onClick={() =>
              onChange({
                ...spec,
                parallel_groups: [
                  ...(spec.parallel_groups ?? []),
                  { name: `fan-${(spec.parallel_groups ?? []).length + 1}`, on_failure: 'fail_fast' },
                ],
              })
            }
            className="text-xs text-violet-400 hover:underline disabled:opacity-50"
          >
            добавить
          </button>
        </div>
        {(spec.parallel_groups ?? []).map((group, index) => {
          const members = spec.stages.filter((s) => s.parallel_group === group.name).length
          const setGroup = (patch: Partial<typeof group>) => {
            const groups = (spec.parallel_groups ?? []).slice()
            groups[index] = { ...groups[index], ...patch }
            // переименование группы — обновляем ссылки на неё у этапов
            const stages =
              patch.name !== undefined && patch.name !== group.name
                ? spec.stages.map((s) => (s.parallel_group === group.name ? { ...s, parallel_group: patch.name ?? '' } : s))
                : spec.stages
            onChange({ ...spec, parallel_groups: groups, stages })
          }
          return (
            <div key={index} className="mt-1.5 flex items-center gap-1.5">
              <input
                value={group.name}
                disabled={readOnly}
                onChange={(e) => setGroup({ name: e.target.value })}
                className="min-w-0 flex-1 rounded-md border border-zinc-700 bg-zinc-950 px-1.5 py-1 font-mono text-xs text-zinc-200 focus:border-violet-500 focus:outline-none disabled:opacity-50"
              />
              <select
                value={group.on_failure}
                disabled={readOnly}
                onChange={(e) => setGroup({ on_failure: e.target.value as typeof group.on_failure })}
                className="rounded-md border border-zinc-700 bg-zinc-950 px-1 py-1 text-xs text-zinc-300 focus:border-violet-500 focus:outline-none disabled:opacity-50"
              >
                {GROUP_ON_FAILURE.map((policy) => (
                  <option key={policy} value={policy}>{policy}</option>
                ))}
              </select>
              <span className="shrink-0 text-xs text-zinc-600" title="этапов в группе">{members} этап.</span>
              <button
                type="button"
                disabled={readOnly}
                title="удалить группу (этапы остаются, членство сбрасывается)"
                onClick={() =>
                  onChange({
                    ...spec,
                    parallel_groups: (spec.parallel_groups ?? []).filter((_, i) => i !== index),
                    stages: spec.stages.map((s) =>
                      s.parallel_group === group.name ? { ...s, parallel_group: '', read_only: false } : s,
                    ),
                  })
                }
                className="rounded p-1 text-zinc-500 hover:bg-red-500/20 hover:text-red-300 disabled:opacity-30"
              >
                <Trash2 className="size-3.5" aria-hidden />
              </button>
            </div>
          )
        })}
        {(spec.parallel_groups ?? []).length === 0 && (
          <p className="mt-1 text-xs text-zinc-600">групп нет — членство назначается в инспекторе этапа</p>
        )}
      </div>
    </div>
  )
}

export function PipelineEditorPage() {
  const { id } = routeApi.useParams()
  const navigate = useNavigate()

  const [pipeline, setPipeline] = useState<Pipeline | null>(null)
  const [versions, setVersions] = useState<Pipeline[]>([])
  const [spec, setSpec] = useState<PipelineSpec | null>(null)
  const [selectedIndex, setSelectedIndex] = useState<number | null>(null)
  const [viewingVersionId, setViewingVersionId] = useState<number | null>(null)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [saveAttempted, setSaveAttempted] = useState(false)
  const [busy, setBusy] = useState(false)
  const [newName, setNewName] = useState<string | null>(null) // null = форма скрыта

  // последняя версия — база для сохранения (родитель следующей)
  const latest = useMemo(
    () => versions.reduce<Pipeline | null>((max, v) => (max === null || v.version > max.version ? v : max), null),
    [versions],
  )
  // имя проекта для крошек (проекты грузятся AppProvider'ом при старте)
  const projectName = useProjectsStore((s) =>
    pipeline?.project_id != null ? (s.items.find((p) => p.id === pipeline.project_id)?.name ?? null) : null,
  )
  // read-only, если смотрим не последнюю версию
  const readOnly = viewingVersionId !== null && latest !== null && viewingVersionId !== latest.id

  const load = useCallback((versionId: number) => {
    setLoadError(null)
    getPipeline(versionId)
      .then(async (detail) => {
        setPipeline(detail)
        // Бэкенд может вернуть Pipeline без поля versions (наблюдалось на
        // версии, созданной через POST /versions) — защита + догрузка
        // списка версий отдельным эндпоинтом.
        let all = Array.isArray(detail.versions) ? detail.versions : []
        if (all.length === 0) {
          all = await listPipelineVersions(versionId).catch(() => [])
        }
        if (all.length === 0) all = [detail]
        setVersions(all)
        const latestVersion = all.reduce<Pipeline | null>(
          (max, v) => (max === null || v.version > max.version ? v : max),
          null,
        ) ?? detail
        setViewingVersionId(latestVersion.id)
        setSpec(parseSpec(latestVersion.spec_json))
        setSelectedIndex(null)
        setSaveAttempted(false)
      })
      .catch((err: unknown) => setLoadError(err instanceof Error ? err.message : 'ошибка загрузки'))
  }, [])

  useEffect(() => load(Number(id)), [id, load])

  // live-подсветка ошибок на канве; список показываем после первой попытки сохранения.
  // warning'и (level='warning') подсвечиваем в списке, но сохранение не блокируют.
  const errors = useMemo(() => (spec ? validateSpec(spec) : []), [spec])
  const blockingErrors = useMemo(() => errors.filter((e) => e.level !== 'warning'), [errors])
  const errorKeys = useMemo(
    () => new Set(blockingErrors.flatMap((e) => (e.stageKey !== undefined ? [e.stageKey] : []))),
    [blockingErrors],
  )

  const openVersion = (version: Pipeline) => {
    setViewingVersionId(version.id)
    setSpec(parseSpec(version.spec_json))
    setSelectedIndex(null)
    setSaveAttempted(false)
    setSaveError(null)
  }

  /** не-последняя версия → черновик для правки (родителем всё равно станет последняя) */
  const takeAsBase = () => {
    if (latest) setViewingVersionId(latest.id)
  }

  const updateStage = (index: number, stage: SpecStage) => {
    if (!spec) return
    const stages = spec.stages.slice()
    stages[index] = stage
    setSpec({ ...spec, stages })
  }

  const moveStage = (index: number, delta: -1 | 1) => {
    if (!spec) return
    const target = index + delta
    if (target < 0 || target >= spec.stages.length) return
    const stages = spec.stages.slice()
    ;[stages[index], stages[target]] = [stages[target], stages[index]]
    setSpec({ ...spec, stages })
    setSelectedIndex(target)
  }

  const duplicateStage = (index: number) => {
    if (!spec) return
    const copy: SpecStage = { ...spec.stages[index], key: `${spec.stages[index].key}-copy`, artifact: { ...spec.stages[index].artifact } }
    const stages = spec.stages.slice()
    stages.splice(index + 1, 0, copy)
    setSpec({ ...spec, stages })
    setSelectedIndex(index + 1)
  }

  const deleteStage = (index: number) => {
    if (!spec) return
    const stages = spec.stages.filter((_, i) => i !== index)
    setSpec({ ...spec, stages })
    setSelectedIndex(null)
  }

  const saveNewVersion = () => {
    if (!spec || !latest || busy) return
    setSaveAttempted(true)
    if (blockingErrors.length > 0) return
    setBusy(true)
    setSaveError(null)
    createPipelineVersion(latest.id, { spec_json: serializeSpec(spec) })
      .then(() => load(latest.id))
      .catch((err: unknown) => setSaveError(err instanceof Error ? err.message : 'ошибка сохранения'))
      .finally(() => setBusy(false))
  }

  const saveAsNewPipeline = () => {
    if (!spec || !pipeline || !newName?.trim() || busy) return
    setSaveAttempted(true)
    if (blockingErrors.length > 0) return
    setBusy(true)
    setSaveError(null)
    createPipeline({ name: newName.trim(), project_id: pipeline.project_id, spec_json: serializeSpec(spec) })
      .then((created) => void navigate({ to: '/pipelines/$id/edit', params: { id: String(created.id) } }))
      .catch((err: unknown) => setSaveError(err instanceof Error ? err.message : 'ошибка создания'))
      .finally(() => setBusy(false))
  }

  if (loadError) return <p className="text-sm text-red-400">не удалось загрузить пайплайн: {loadError}</p>
  if (!pipeline || !spec) return <p className="text-sm text-zinc-500">загрузка пайплайна…</p>

  const selectedStage = selectedIndex !== null ? (spec.stages[selectedIndex] ?? null) : null

  return (
    <div className="flex h-full min-h-0 flex-col gap-3">
      <header className="flex flex-wrap items-center gap-2 rounded-lg border border-zinc-800 bg-zinc-900/60 px-4 py-2">
        {/* назад: к проекту, а для глобального пайплайна — к списку проектов */}
        {pipeline.project_id != null ? (
          <Link to="/projects/$id" params={{ id: String(pipeline.project_id) }}
            className="flex items-center gap-1 rounded-md border border-zinc-800 px-2 py-1 text-xs text-zinc-500 hover:bg-zinc-800 hover:text-zinc-200">
            <ArrowLeft className="size-3.5" aria-hidden /> К проекту
          </Link>
        ) : (
          <Link to="/"
            className="flex items-center gap-1 rounded-md border border-zinc-800 px-2 py-1 text-xs text-zinc-500 hover:bg-zinc-800 hover:text-zinc-200">
            <ArrowLeft className="size-3.5" aria-hidden /> К проектам
          </Link>
        )}
        {/* крошки: Проекты / <проект> / Редактор <пайплайн> */}
        <nav className="flex items-center gap-1.5 text-xs text-zinc-500">
          <Link to="/" className="hover:text-zinc-300">Проекты</Link>
          <span>/</span>
          {pipeline.project_id != null ? (
            <>
              <Link to="/projects/$id" params={{ id: String(pipeline.project_id) }} className="hover:text-zinc-300">
                {projectName ?? `проект #${pipeline.project_id}`}
              </Link>
              <span>/</span>
            </>
          ) : (
            <>
              <span className="text-zinc-600">глобальный</span>
              <span>/</span>
            </>
          )}
          <span className="text-zinc-400">редактор {pipeline.name}</span>
        </nav>
        <h1 className="text-sm font-semibold text-zinc-100">{pipeline.name}</h1>
        <select
          value={viewingVersionId ?? ''}
          onChange={(e) => {
            const version = versions.find((v) => v.id === Number(e.target.value))
            if (version) openVersion(version)
          }}
          className="rounded-md border border-zinc-700 bg-zinc-950 px-1.5 py-1 text-xs text-zinc-300 focus:border-violet-500 focus:outline-none"
        >
          {versions.map((version) => (
            <option key={version.id} value={version.id}>
              v{version.version}
              {latest && version.id === latest.id ? ' (последняя)' : ''}
            </option>
          ))}
        </select>
        {readOnly && (
          <>
            <span className="rounded bg-amber-500/15 px-2 py-0.5 text-xs text-amber-300">старая версия — только чтение</span>
            <button type="button" onClick={takeAsBase} className="text-xs text-violet-400 hover:underline">
              взять за основу
            </button>
          </>
        )}
        <span className="ml-auto flex items-center gap-2">
          {latest && <ExportPipelineButton pipeline={readOnly && viewingVersionId ? (versions.find((v) => v.id === viewingVersionId) ?? latest) : latest} />}
          <button
            type="button"
            disabled={busy || readOnly}
            onClick={saveNewVersion}
            className="flex items-center gap-1 rounded-md bg-violet-600 px-2.5 py-1 text-xs font-medium text-violet-50 hover:bg-violet-500 disabled:opacity-50"
          >
            <Save className="size-3.5" aria-hidden /> {busy ? 'сохранение…' : 'Сохранить (новая версия)'}
          </button>
          <button
            type="button"
            disabled={busy || readOnly}
            onClick={() => setNewName(newName === null ? `${pipeline.name}-copy` : null)}
            className="rounded-md border border-zinc-700 px-2.5 py-1 text-xs text-zinc-400 hover:bg-zinc-800 hover:text-zinc-200 disabled:opacity-50"
          >
            как новый пайплайн
          </button>
        </span>
      </header>

      {newName !== null && (
        <div className="flex items-center gap-2 rounded-lg border border-violet-500/40 bg-zinc-900 px-4 py-2">
          <span className="text-xs text-zinc-500">имя нового пайплайна:</span>
          <input
            value={newName}
            onChange={(e) => setNewName(e.target.value)}
            autoFocus
            className="rounded-md border border-zinc-700 bg-zinc-950 px-2 py-1 text-xs text-zinc-200 focus:border-violet-500 focus:outline-none"
          />
          <button type="button" disabled={busy || !newName.trim()} onClick={saveAsNewPipeline}
            className="rounded-md bg-violet-600 px-2.5 py-1 text-xs font-medium text-violet-50 hover:bg-violet-500 disabled:opacity-50">
            Создать
          </button>
          <button type="button" onClick={() => setNewName(null)} className="text-xs text-zinc-500 hover:text-zinc-300">
            отмена
          </button>
        </div>
      )}

      {saveAttempted && errors.length > 0 && (
        <ul
          className={`space-y-0.5 rounded-lg border px-4 py-2 ${
            blockingErrors.length > 0 ? 'border-red-500/40 bg-red-500/10' : 'border-amber-500/40 bg-amber-500/10'
          }`}
        >
          {errors.map((error) => (
            <li key={error.message} className={`text-xs ${error.level === 'warning' ? 'text-amber-300' : 'text-red-300'}`}>
              {error.message}
            </li>
          ))}
        </ul>
      )}
      {saveError && <p className="rounded-lg border border-red-500/40 bg-red-500/10 px-4 py-2 text-xs text-red-300">{saveError}</p>}

      <div className="flex min-h-0 flex-1 gap-3">
        <div className="min-w-0 flex-1 overflow-hidden rounded-lg border border-zinc-800">
          <PipelineCanvas spec={spec} errorKeys={errorKeys} selectedIndex={selectedIndex} readOnly={readOnly} onSelect={setSelectedIndex} />
        </div>
        <div className="flex w-96 shrink-0 flex-col gap-3 overflow-hidden">
          {/* палитра: llm-stage и janitor (T-22); human-gate — позже */}
          <div className="flex gap-1.5 rounded-lg border border-zinc-800 bg-zinc-900/60 px-3 py-2">
            <button
              type="button"
              disabled={readOnly}
              onClick={() => {
                setSpec({ ...spec, stages: [...spec.stages, emptyStage(spec.stages.length + 1)] })
                setSelectedIndex(spec.stages.length)
              }}
              className="flex items-center gap-1 rounded-md bg-zinc-800 px-2 py-1 text-xs text-zinc-300 hover:bg-zinc-700 disabled:opacity-50"
            >
              <Plus className="size-3.5" aria-hidden /> llm-stage
            </button>
            <button
              type="button"
              disabled={readOnly}
              title="janitor — команды-скрипты (T-22)"
              onClick={() => {
                setSpec({ ...spec, stages: [...spec.stages, emptyJanitor(spec.stages.length + 1)] })
                setSelectedIndex(spec.stages.length)
              }}
              className="flex items-center gap-1 rounded-md bg-zinc-800 px-2 py-1 text-xs text-zinc-300 hover:bg-zinc-700 disabled:opacity-50"
            >
              <Plus className="size-3.5" aria-hidden /> janitor
            </button>
            <button type="button" disabled title="human-gate — позже"
              className="flex items-center gap-1 rounded-md bg-zinc-800/50 px-2 py-1 text-xs text-zinc-600">
              <Plus className="size-3.5" aria-hidden /> human-gate
            </button>
          </div>
          <div className="min-h-0 flex-1 rounded-lg border border-zinc-800 bg-zinc-900/40">
            {selectedStage && selectedIndex !== null ? (
              <StageInspector
                stage={selectedStage}
                errors={errors.filter((e) => e.stageKey === selectedStage.key).map((e) => e.message)}
                readOnly={readOnly}
                canMoveUp={selectedIndex > 0}
                canMoveDown={selectedIndex < spec.stages.length - 1}
                groupNames={(spec.parallel_groups ?? []).map((g) => g.name)}
                onChange={(stage) => updateStage(selectedIndex, stage)}
                onMoveUp={() => moveStage(selectedIndex, -1)}
                onMoveDown={() => moveStage(selectedIndex, 1)}
                onDuplicate={() => duplicateStage(selectedIndex)}
                onDelete={() => deleteStage(selectedIndex)}
              />
            ) : (
              <LoopPanel spec={spec} readOnly={readOnly} onChange={setSpec} />
            )}
          </div>
        </div>
      </div>
    </div>
  )
}
