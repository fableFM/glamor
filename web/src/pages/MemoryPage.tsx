import { useCallback, useEffect, useState } from 'react'
import { Brain, FolderGit2, Globe, GraduationCap } from 'lucide-react'
import type { MemoryFileEntry, MemoryTree } from '../api/client'
import { memoryTree } from '../api/client'
import { MemoryViewer } from '../components/memory/MemoryViewer'
import { LessonsPanel } from '../components/memory/LessonsPanel'
import { formatDateTime } from '../lib/format'

/*
  Раздел «Память» (T-23 + T-29): вкладка «Память» — дерево vendor-файлов
  (глобальная + по проектам) с просмотром/правкой/историей/promote;
  вкладка «Уроки» — causal memory (LessonsPanel).
*/

/** выбранный файл памяти: scope + vendor (+ project_id для project-scope) */
export interface MemorySelection {
  scope: 'global' | 'project'
  vendor: string
  projectId?: number
}

function FileRow({
  entry,
  selected,
  onSelect,
}: {
  entry: MemoryFileEntry
  selected: boolean
  onSelect: () => void
}) {
  return (
    <button
      type="button"
      onClick={onSelect}
      className={`flex w-full items-center gap-2 rounded-md px-2 py-1 text-left text-xs ${
        selected ? 'bg-violet-500/15 text-violet-200' : 'text-zinc-400 hover:bg-zinc-800 hover:text-zinc-200'
      }`}
    >
      <span className="min-w-0 flex-1 truncate font-mono">{entry.vendor}</span>
      <span className="shrink-0 text-zinc-600">{(entry.size / 1024).toFixed(1)}k</span>
      <span className="shrink-0 text-zinc-600">{formatDateTime(entry.updated_at)}</span>
    </button>
  )
}

export function MemoryPage() {
  const [tree, setTree] = useState<MemoryTree | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [selection, setSelection] = useState<MemorySelection | null>(null)
  /** вкладка раздела: файлы памяти или уроки (T-29) */
  const [tab, setTab] = useState<'memory' | 'lessons'>('memory')

  const reload = useCallback(() => {
    memoryTree()
      .then(setTree)
      .catch((err: unknown) => setError(err instanceof Error ? err.message : 'ошибка загрузки'))
  }, [])

  useEffect(() => reload(), [reload])

  const tabsHeader = (
    <div className="mb-3 flex gap-1">
      <button
        type="button"
        onClick={() => setTab('memory')}
        className={`flex items-center gap-1.5 rounded-md px-3 py-1.5 text-sm ${
          tab === 'memory' ? 'bg-zinc-800 text-zinc-100' : 'text-zinc-500 hover:text-zinc-300'
        }`}
      >
        <Brain className="size-4" aria-hidden /> Память
      </button>
      <button
        type="button"
        onClick={() => setTab('lessons')}
        className={`flex items-center gap-1.5 rounded-md px-3 py-1.5 text-sm ${
          tab === 'lessons' ? 'bg-zinc-800 text-zinc-100' : 'text-zinc-500 hover:text-zinc-300'
        }`}
      >
        <GraduationCap className="size-4" aria-hidden /> Уроки
      </button>
    </div>
  )

  if (tab === 'lessons') {
    return (
      <div className="flex h-full min-h-0 flex-col">
        {tabsHeader}
        <div className="min-h-0 flex-1 rounded-lg border border-zinc-800 bg-zinc-900/40">
          <LessonsPanel />
        </div>
      </div>
    )
  }

  if (error) return <p className="text-sm text-red-400">не удалось загрузить память: {error}</p>
  if (!tree) return <p className="text-sm text-zinc-500">загрузка…</p>

  return (
    <div className="flex h-full min-h-0 flex-col">
      {tabsHeader}
      <div className="flex min-h-0 flex-1 gap-3">
      {/* дерево памяти */}
      <div className="w-72 shrink-0 overflow-auto rounded-lg border border-zinc-800 bg-zinc-900/40 p-2">
        <div className="flex items-center gap-1.5 px-2 py-1.5 text-xs font-semibold uppercase tracking-wide text-zinc-500">
          <Globe className="size-3.5" aria-hidden /> Глобальная
        </div>
        {tree.global.length === 0 && <p className="px-2 py-1 text-xs text-zinc-600">пусто</p>}
        {tree.global.map((entry) => (
          <FileRow
            key={entry.path}
            entry={entry}
            selected={selection?.scope === 'global' && selection.vendor === entry.vendor}
            onSelect={() => setSelection({ scope: 'global', vendor: entry.vendor })}
          />
        ))}

        {tree.projects.map((project) => (
          <div key={project.project_id} className="mt-3">
            <div className="flex items-center gap-1.5 px-2 py-1.5 text-xs font-semibold uppercase tracking-wide text-zinc-500">
              <FolderGit2 className="size-3.5" aria-hidden /> {project.project_name}
            </div>
            {project.files.map((entry) => (
              <FileRow
                key={entry.path}
                entry={entry}
                selected={
                  selection?.scope === 'project' &&
                  selection.vendor === entry.vendor &&
                  selection.projectId === project.project_id
                }
                onSelect={() =>
                  setSelection({ scope: 'project', vendor: entry.vendor, projectId: project.project_id })
                }
              />
            ))}
          </div>
        ))}
      </div>

      {/* просмотр/правка */}
      <div className="min-w-0 flex-1 rounded-lg border border-zinc-800 bg-zinc-900/40">
        {selection ? (
          <MemoryViewer
            key={`${selection.scope}:${selection.projectId ?? ''}:${selection.vendor}`}
            selection={selection}
            onChanged={reload}
          />
        ) : (
          <div className="flex h-full flex-col items-center justify-center gap-2 text-zinc-600">
            <Brain className="size-8" aria-hidden />
            <p className="text-sm">выберите файл памяти слева</p>
          </div>
        )}
      </div>
      </div>
    </div>
  )
}
