import { useState } from 'react'
import { Link } from '@tanstack/react-router'
import { FolderPlus, GitBranch, Settings2, Trash2 } from 'lucide-react'
import type { Project } from '../api/client'
import { ApiError, createProject, deleteProject, patchProject } from '../api/client'
import { useProjectsStore } from '../stores/projects'
import { useRunsStore } from '../stores/runs'
import { useInboxStore } from '../stores/inbox'
import { RunStateBadge } from '../components/StateBadge'
import { FolderPickerButton } from '../components/projects/FolderPicker'
import { firstLine } from '../lib/format'

/*
  Экран «Проекты» (T-16): список с live-состоянием (активный ран + пульс,
  бейдж открытых гейтов), добавление проекта, настройки (PATCH).
  Live — через runs/inbox сторы (WS run_id=*), без ручного рефреша.
*/

function AddProjectForm({ onClose }: { onClose: () => void }) {
  const [path, setPath] = useState('')
  const [name, setName] = useState('')
  const [defaultBranch, setDefaultBranch] = useState('')
  const [ideCommand, setIdeCommand] = useState('goland') // дефолт — goland (как на бэкенде)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const submit = () => {
    if (!path.trim() || !name.trim() || busy) return
    setBusy(true)
    setError(null)
    createProject({
      path: path.trim(),
      name: name.trim(),
      default_branch: defaultBranch.trim() || undefined,
      ide_command: ideCommand.trim() || undefined,
    })
      .then((project) => {
        useProjectsStore.getState().upsert(project)
        onClose()
      })
      .catch((err: unknown) => setError(err instanceof Error ? err.message : 'ошибка'))
      .finally(() => setBusy(false))
  }

  return (
    <div className="mb-4 rounded-lg border border-violet-500/40 bg-zinc-900 p-4">
      <h2 className="mb-3 text-sm font-semibold text-zinc-100">Добавить проект</h2>
      <div className="grid grid-cols-2 gap-3">
        <div className="block">
          <span className="mb-1 block text-xs text-zinc-500">Путь к git-репозиторию *</span>
          <div className="flex gap-1.5">
            <input
              value={path}
              onChange={(e) => setPath(e.target.value)}
              placeholder="/Users/me/go/myrepo"
              autoFocus
              className="w-full min-w-0 rounded-md border border-zinc-700 bg-zinc-950 px-2 py-1.5 font-mono text-xs text-zinc-200 placeholder:text-zinc-600 focus:border-violet-500 focus:outline-none"
            />
            <FolderPickerButton
              onSelect={(selected) => {
                setPath(selected)
                // имя по умолчанию — базовое имя каталога
                if (!name.trim()) setName(selected.replace(/\/+$/, '').split('/').pop() ?? '')
              }}
            />
          </div>
        </div>
        <label className="block">
          <span className="mb-1 block text-xs text-zinc-500">Имя *</span>
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="myrepo"
            className="w-full rounded-md border border-zinc-700 bg-zinc-950 px-2 py-1.5 text-sm text-zinc-200 placeholder:text-zinc-600 focus:border-violet-500 focus:outline-none"
          />
        </label>
        <label className="block">
          <span className="mb-1 block text-xs text-zinc-500">Ветка по умолчанию</span>
          <input
            value={defaultBranch}
            onChange={(e) => setDefaultBranch(e.target.value)}
            placeholder="main"
            className="w-full rounded-md border border-zinc-700 bg-zinc-950 px-2 py-1.5 font-mono text-xs text-zinc-200 placeholder:text-zinc-600 focus:border-violet-500 focus:outline-none"
          />
        </label>
        <label className="block">
          <span className="mb-1 block text-xs text-zinc-500">Команда IDE</span>
          <input
            value={ideCommand}
            onChange={(e) => setIdeCommand(e.target.value)}
            placeholder="goland / code / cursor"
            className="w-full rounded-md border border-zinc-700 bg-zinc-950 px-2 py-1.5 font-mono text-xs text-zinc-200 placeholder:text-zinc-600 focus:border-violet-500 focus:outline-none"
          />
        </label>
      </div>
      {error && <p className="mt-2 text-xs text-red-400">{error}</p>}
      <div className="mt-3 flex justify-end gap-2">
        <button type="button" onClick={onClose} className="rounded-md px-3 py-1.5 text-sm text-zinc-400 hover:bg-zinc-800">
          Отмена
        </button>
        <button
          type="button"
          disabled={busy || !path.trim() || !name.trim()}
          onClick={submit}
          className="rounded-md bg-violet-600 px-3 py-1.5 text-sm font-medium text-violet-50 hover:bg-violet-500 disabled:opacity-50"
        >
          {busy ? 'добавление…' : 'Добавить'}
        </button>
      </div>
    </div>
  )
}

function ProjectSettings({ project, onClose }: { project: Project; onClose: () => void }) {
  const [defaultBranch, setDefaultBranch] = useState(project.default_branch)
  const [ideCommand, setIdeCommand] = useState(project.ide_command)
  const [notifyTgDefault, setNotifyTgDefault] = useState(project.notify_tg_default)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const submit = () => {
    if (busy) return
    setBusy(true)
    setError(null)
    patchProject(project.id, {
      default_branch: defaultBranch.trim() || undefined,
      ide_command: ideCommand.trim() || undefined,
      notify_tg_default: notifyTgDefault,
    })
      .then((updated) => {
        useProjectsStore.getState().upsert(updated)
        onClose()
      })
      .catch((err: unknown) => setError(err instanceof Error ? err.message : 'ошибка'))
      .finally(() => setBusy(false))
  }

  return (
    <div className="mt-2 flex flex-wrap items-end gap-3 rounded-md border border-zinc-800 bg-zinc-950/60 px-3 py-2">
      <label className="block">
        <span className="mb-1 block text-xs text-zinc-500">default_branch</span>
        <input
          value={defaultBranch}
          onChange={(e) => setDefaultBranch(e.target.value)}
          className="rounded-md border border-zinc-700 bg-zinc-950 px-2 py-1 font-mono text-xs text-zinc-200 focus:border-violet-500 focus:outline-none"
        />
      </label>
      <label className="block">
        <span className="mb-1 block text-xs text-zinc-500">ide_command</span>
        <input
          value={ideCommand}
          onChange={(e) => setIdeCommand(e.target.value)}
          className="rounded-md border border-zinc-700 bg-zinc-950 px-2 py-1 font-mono text-xs text-zinc-200 focus:border-violet-500 focus:outline-none"
        />
      </label>
      <label className="flex items-center gap-2 pb-1 text-xs text-zinc-300">
        <input type="checkbox" checked={notifyTgDefault} onChange={(e) => setNotifyTgDefault(e.target.checked)} />
        notify_tg по умолчанию для новых ранов
      </label>
      <button
        type="button"
        disabled={busy}
        onClick={submit}
        className="rounded-md bg-zinc-800 px-2.5 py-1 text-xs text-zinc-200 hover:bg-zinc-700 disabled:opacity-50"
      >
        Сохранить
      </button>
      <DeleteProjectButton project={project} onDeleted={onClose} />
      <button type="button" onClick={onClose} className="rounded-md px-2.5 py-1 text-xs text-zinc-500 hover:bg-zinc-800">
        Закрыть
      </button>
      {error && <span className="text-xs text-red-400">{error}</span>}
    </div>
  )
}

/** Удаление проекта: confirm → DELETE /projects/{id}; 400 «активные раны» — человекочитаемо. */
function DeleteProjectButton({ project, onDeleted }: { project: Project; onDeleted: () => void }) {
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const doDelete = () => {
    if (busy) return
    if (
      !window.confirm(
        `Удалить проект «${project.name}»? Это удалит всю историю ранов, гейты и артефакты проекта. Действие необратимо.`,
      )
    ) {
      return
    }
    setBusy(true)
    setError(null)
    deleteProject(project.id)
      .then(() => {
        useProjectsStore.getState().remove(project.id)
        onDeleted()
      })
      .catch((err: unknown) => {
        // 400 validation — например, по проекту есть активные раны
        setError(err instanceof ApiError ? err.message : err instanceof Error ? err.message : 'ошибка удаления')
      })
      .finally(() => setBusy(false))
  }

  return (
    <span className="flex items-center gap-2">
      <button
        type="button"
        disabled={busy}
        onClick={doDelete}
        className="flex items-center gap-1 rounded-md border border-red-500/40 px-2.5 py-1 text-xs text-red-400 hover:bg-red-500/10 disabled:opacity-50"
      >
        <Trash2 className="size-3.5" aria-hidden /> {busy ? 'удаление…' : 'Удалить проект'}
      </button>
      {error && <span className="text-xs text-red-400">{error}</span>}
    </span>
  )
}

function ProjectRow({ project }: { project: Project }) {
  const [settingsOpen, setSettingsOpen] = useState(false)
  const runsById = useRunsStore((s) => s.byId)
  const openGates = useInboxStore((s) => s.open)

  // активный ран проекта: running/waiting_gate, самый свежий
  const activeRun = Object.values(runsById)
    .filter((r) => r.project_id === project.id && (r.state === 'running' || r.state === 'waiting_gate'))
    .sort((a, b) => b.created_at.localeCompare(a.created_at))[0]

  // открытые гейты проекта — через run → project маппинг
  const gatesCount = Object.values(openGates).filter((item) => {
    const run = runsById[item.runId]
    return run?.project_id === project.id
  }).length

  return (
    <li className="rounded-lg border border-zinc-800 bg-zinc-900/60 px-4 py-3">
      <div className="flex items-center gap-3">
        <div className="min-w-0 flex-1">
          <Link
            to="/projects/$id"
            params={{ id: String(project.id) }}
            className="text-sm font-medium text-zinc-100 hover:text-violet-300"
          >
            {project.name}
          </Link>
          <div className="mt-0.5 flex items-center gap-3 text-xs text-zinc-500">
            <span className="truncate font-mono">{project.path}</span>
            <span className="flex shrink-0 items-center gap-1">
              <GitBranch className="size-3" aria-hidden />
              {project.default_branch}
            </span>
          </div>
        </div>

        {activeRun && (
          <Link
            to="/runs/$id"
            params={{ id: activeRun.id }}
            className="flex max-w-56 items-center gap-2 rounded-md border border-zinc-800 bg-zinc-950/60 px-2 py-1 hover:border-zinc-700"
            title={activeRun.task_text}
          >
            <span className="size-1.5 shrink-0 animate-pulse rounded-full bg-blue-400" aria-hidden />
            <span className="truncate text-xs text-zinc-400">{firstLine(activeRun.task_text, 40)}</span>
            <RunStateBadge state={activeRun.state} />
          </Link>
        )}

        {gatesCount > 0 && (
          <span className="rounded-full bg-amber-500/15 px-2 py-0.5 text-xs font-medium text-amber-300">
            {gatesCount} гейт{gatesCount > 1 ? 'а' : ''}
          </span>
        )}

        <button
          type="button"
          onClick={() => setSettingsOpen((v) => !v)}
          className="rounded-md p-1.5 text-zinc-500 hover:bg-zinc-800 hover:text-zinc-200"
          aria-label="Настройки проекта"
        >
          <Settings2 className="size-4" aria-hidden />
        </button>
      </div>
      {settingsOpen && <ProjectSettings project={project} onClose={() => setSettingsOpen(false)} />}
    </li>
  )
}

export function ProjectsPage() {
  const items = useProjectsStore((s) => s.items)
  const loading = useProjectsStore((s) => s.loading)
  const error = useProjectsStore((s) => s.error)
  const [addOpen, setAddOpen] = useState(false)

  return (
    <div className="mx-auto max-w-3xl">
      <div className="mb-4 flex items-center justify-between">
        <h1 className="text-xl font-semibold text-zinc-100">Проекты</h1>
        <button
          type="button"
          onClick={() => setAddOpen((v) => !v)}
          className="flex items-center gap-1.5 rounded-md bg-violet-600 px-3 py-1.5 text-sm font-medium text-violet-50 hover:bg-violet-500"
        >
          <FolderPlus className="size-4" aria-hidden />
          Добавить проект
        </button>
      </div>

      {addOpen && <AddProjectForm onClose={() => setAddOpen(false)} />}

      {loading && <p className="text-sm text-zinc-500">загрузка…</p>}
      {error && <p className="text-sm text-red-400">ошибка загрузки: {error}</p>}
      {!loading && !error && items.length === 0 && (
        <p className="text-sm text-zinc-500">проектов пока нет — добавьте первый</p>
      )}
      <ul className="space-y-2">
        {items.map((project) => (
          <ProjectRow key={project.id} project={project} />
        ))}
      </ul>
    </div>
  )
}
