import { useCallback, useEffect, useState } from 'react'
import { ArrowUpFromLine, History, Pencil, Save, X } from 'lucide-react'
import type { MemoryFileContent, MemoryHistoryEntry } from '../../api/client'
import { memoryHistory, memoryPromote, memoryReadFile, memoryWriteFile } from '../../api/client'
import { Markdown } from '../Markdown'
import { formatDateTime } from '../../lib/format'
import type { MemorySelection } from '../../pages/MemoryPage'

/*
  Просмотр файла vendor-памяти (T-23): markdown-рендер, ручная правка
  (textarea → PUT), история глобального файла (git log), кнопка
  «В глобальную» для проектной записи (POST /memory/promote).
*/

export function MemoryViewer({
  selection,
  onChanged,
}: {
  selection: MemorySelection
  onChanged: () => void
}) {
  const [file, setFile] = useState<MemoryFileContent | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState('')
  const [history, setHistory] = useState<MemoryHistoryEntry[] | null>(null)
  const [showHistory, setShowHistory] = useState(false)
  const [busy, setBusy] = useState(false)
  const [notice, setNotice] = useState<string | null>(null)

  const load = useCallback(() => {
    setError(null)
    memoryReadFile(
      selection.scope === 'global'
        ? { scope: 'global', vendor: selection.vendor }
        : { scope: 'project', vendor: selection.vendor, project_id: selection.projectId },
    )
      .then(setFile)
      .catch((err: unknown) => setError(err instanceof Error ? err.message : 'ошибка загрузки файла'))
  }, [selection])

  useEffect(() => {
    setEditing(false)
    setShowHistory(false)
    setHistory(null)
    setNotice(null)
    load()
  }, [load])

  const save = () => {
    if (busy) return
    setBusy(true)
    setError(null)
    memoryWriteFile({
      scope: selection.scope,
      vendor: selection.vendor,
      content: draft,
      ...(selection.scope === 'project' ? { project_id: selection.projectId } : {}),
    })
      .then((updated) => {
        setFile(updated)
        setEditing(false)
        onChanged() // size/updated_at в дереве изменились
      })
      .catch((err: unknown) => setError(err instanceof Error ? err.message : 'ошибка сохранения'))
      .finally(() => setBusy(false))
  }

  const toggleHistory = () => {
    if (showHistory) {
      setShowHistory(false)
      return
    }
    setShowHistory(true)
    memoryHistory({ vendor: selection.vendor })
      .then(setHistory)
      .catch(() => setHistory([]))
  }

  const promote = () => {
    if (selection.scope !== 'project' || selection.projectId === undefined || busy) return
    if (!window.confirm(`Продвинуть «${selection.vendor}» из проектной памяти в глобальную?`)) return
    setBusy(true)
    setError(null)
    memoryPromote({ vendor: selection.vendor, project_id: selection.projectId })
      .then((result) => {
        setNotice(`продвинуто в ${result.path ?? 'глобальную память'}`)
        onChanged()
      })
      .catch((err: unknown) => setError(err instanceof Error ? err.message : 'ошибка promote'))
      .finally(() => setBusy(false))
  }

  if (error && !file) return <p className="p-4 text-sm text-red-400">{error}</p>
  if (!file) return <p className="p-4 text-sm text-zinc-500">загрузка файла…</p>

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex items-center gap-2 border-b border-zinc-800 px-4 py-2">
        <span className="font-mono text-sm font-medium text-zinc-200">{file.vendor}</span>
        <span className="rounded bg-zinc-800 px-1.5 py-0.5 text-xs text-zinc-500">
          {selection.scope === 'global' ? 'глобальная' : 'проект'}
        </span>
        <span className="ml-auto flex items-center gap-1.5">
          {selection.scope === 'project' && (
            <button
              type="button"
              onClick={promote}
              disabled={busy}
              title="промоушн записи в глобальную память"
              className="flex items-center gap-1 rounded-md border border-zinc-700 px-2 py-1 text-xs text-zinc-400 hover:bg-zinc-800 hover:text-zinc-200 disabled:opacity-50"
            >
              <ArrowUpFromLine className="size-3.5" aria-hidden /> В глобальную
            </button>
          )}
          {selection.scope === 'global' && (
            <button
              type="button"
              onClick={toggleHistory}
              className="flex items-center gap-1 rounded-md border border-zinc-700 px-2 py-1 text-xs text-zinc-400 hover:bg-zinc-800 hover:text-zinc-200"
            >
              <History className="size-3.5" aria-hidden /> История
            </button>
          )}
          {editing ? (
            <>
              <button
                type="button"
                onClick={save}
                disabled={busy}
                className="flex items-center gap-1 rounded-md bg-violet-600 px-2 py-1 text-xs font-medium text-violet-50 hover:bg-violet-500 disabled:opacity-50"
              >
                <Save className="size-3.5" aria-hidden /> {busy ? 'сохранение…' : 'Сохранить'}
              </button>
              <button
                type="button"
                onClick={() => setEditing(false)}
                className="flex items-center gap-1 rounded-md border border-zinc-700 px-2 py-1 text-xs text-zinc-400 hover:bg-zinc-800"
              >
                <X className="size-3.5" aria-hidden /> Отмена
              </button>
            </>
          ) : (
            <button
              type="button"
              onClick={() => {
                setDraft(file.content)
                setEditing(true)
              }}
              className="flex items-center gap-1 rounded-md border border-zinc-700 px-2 py-1 text-xs text-zinc-400 hover:bg-zinc-800 hover:text-zinc-200"
            >
              <Pencil className="size-3.5" aria-hidden /> Править
            </button>
          )}
        </span>
      </div>

      {notice && <p className="border-b border-zinc-800 bg-emerald-500/10 px-4 py-1.5 text-xs text-emerald-300">{notice}</p>}
      {error && <p className="border-b border-zinc-800 bg-red-500/10 px-4 py-1.5 text-xs text-red-300">{error}</p>}

      {showHistory && (
        <div className="max-h-40 overflow-auto border-b border-zinc-800 px-4 py-2">
          {history === null ? (
            <p className="text-xs text-zinc-500">загрузка истории…</p>
          ) : history.length === 0 ? (
            <p className="text-xs text-zinc-600">история пуста</p>
          ) : (
            <ul className="space-y-1">
              {history.map((entry) => (
                <li key={entry.hash} className="flex items-baseline gap-2 text-xs">
                  <code className="shrink-0 text-violet-400">{entry.hash.slice(0, 7)}</code>
                  <span className="min-w-0 flex-1 truncate text-zinc-300">{entry.message}</span>
                  <span className="shrink-0 text-zinc-600">{formatDateTime(entry.date)}</span>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}

      <div className="min-h-0 flex-1 overflow-auto p-4">
        {editing ? (
          <textarea
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            spellCheck={false}
            className="h-full min-h-96 w-full resize-none rounded-md border border-zinc-700 bg-zinc-950 p-3 font-mono text-xs leading-5 text-zinc-200 focus:border-violet-500 focus:outline-none"
          />
        ) : file.content.trim().length > 0 ? (
          <Markdown>{file.content}</Markdown>
        ) : (
          <p className="text-sm text-zinc-600">файл пуст</p>
        )}
      </div>
    </div>
  )
}
