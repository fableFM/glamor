import { useCallback, useEffect, useState } from 'react'
import { ArrowUp, FolderGit2, Folder, X } from 'lucide-react'
import type { FsBrowseResult } from '../../api/client'
import { fsBrowse } from '../../api/client'

/*
  Выбор папки проекта: в Tauri — нативный диалог (plugin-dialog),
  в браузере — серверный пикер через GET /fs/browse (навигация по
  подкаталогам, «..» вверх через parent, git-репозитории подсвечены).
*/

/** нативный диалог Tauri; null — выбор отменён; undefined — не Tauri */
async function pickFolderNative(): Promise<string | null | undefined> {
  if (!('__TAURI__' in window)) return undefined
  try {
    const { open } = await import('@tauri-apps/plugin-dialog')
    const selected = await open({ directory: true })
    return typeof selected === 'string' ? selected : null
  } catch {
    return undefined // плагин недоступен — уходим в серверный фолбэк
  }
}

function ServerPicker({ onSelect, onClose }: { onSelect: (path: string) => void; onClose: () => void }) {
  const [current, setCurrent] = useState<FsBrowseResult | null>(null)
  const [error, setError] = useState<string | null>(null)

  const browse = useCallback((path?: string) => {
    setError(null)
    fsBrowse(path ? { path } : {})
      .then(setCurrent)
      .catch((err: unknown) => setError(err instanceof Error ? err.message : 'ошибка'))
  }, [])

  useEffect(() => browse(), [browse])

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-zinc-950/70" onClick={onClose}>
      <div
        className="flex max-h-[70vh] w-[32rem] flex-col rounded-lg border border-zinc-700 bg-zinc-900 shadow-2xl"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center gap-2 border-b border-zinc-800 px-3 py-2">
          <span className="min-w-0 flex-1 truncate font-mono text-xs text-zinc-400">{current?.path ?? '…'}</span>
          <button type="button" onClick={onClose} className="rounded p-1 text-zinc-500 hover:bg-zinc-800" aria-label="Закрыть">
            <X className="size-4" aria-hidden />
          </button>
        </div>
        <div className="min-h-0 flex-1 overflow-auto p-1.5">
          {error && <p className="px-2 py-1 text-xs text-red-400">{error}</p>}
          {!current && !error && <p className="px-2 py-1 text-xs text-zinc-500">загрузка…</p>}
          {current?.parent != null && (
            <button
              type="button"
              onClick={() => browse(current.parent ?? undefined)}
              className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm text-zinc-400 hover:bg-zinc-800"
            >
              <ArrowUp className="size-4" aria-hidden /> ..
            </button>
          )}
          {current?.dirs.map((dir) => (
            <button
              key={dir.path}
              type="button"
              onClick={() => browse(dir.path)}
              className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm hover:bg-zinc-800"
            >
              {dir.is_git_repo ? (
                <FolderGit2 className="size-4 shrink-0 text-violet-400" aria-hidden />
              ) : (
                <Folder className="size-4 shrink-0 text-zinc-500" aria-hidden />
              )}
              <span className={dir.is_git_repo ? 'font-medium text-zinc-100' : 'text-zinc-300'}>{dir.name}</span>
              {dir.is_git_repo && <span className="text-xs text-violet-400/70">git</span>}
            </button>
          ))}
          {current && current.dirs.length === 0 && <p className="px-2 py-1 text-xs text-zinc-600">подкаталогов нет</p>}
        </div>
        <div className="flex justify-end gap-2 border-t border-zinc-800 px-3 py-2">
          <button type="button" onClick={onClose} className="rounded-md px-3 py-1.5 text-xs text-zinc-400 hover:bg-zinc-800">
            Отмена
          </button>
          <button
            type="button"
            disabled={!current}
            onClick={() => current && onSelect(current.path)}
            className="rounded-md bg-violet-600 px-3 py-1.5 text-xs font-medium text-violet-50 hover:bg-violet-500 disabled:opacity-50"
          >
            Выбрать эту папку
          </button>
        </div>
      </div>
    </div>
  )
}

/** Кнопка «Выбрать папку»: Tauri-диалог при наличии, иначе серверный пикер-модалка. */
export function FolderPickerButton({ onSelect }: { onSelect: (path: string) => void }) {
  const [pickerOpen, setPickerOpen] = useState(false)

  const onClick = () => {
    void pickFolderNative().then((path) => {
      if (path === undefined) setPickerOpen(true) // не Tauri — серверный пикер
      else if (path !== null) onSelect(path)
    })
  }

  return (
    <>
      <button
        type="button"
        onClick={onClick}
        className="shrink-0 rounded-md border border-zinc-700 px-2 py-1.5 text-xs text-zinc-400 hover:bg-zinc-800 hover:text-zinc-200"
      >
        Выбрать папку
      </button>
      {pickerOpen && (
        <ServerPicker
          onSelect={(path) => {
            onSelect(path)
            setPickerOpen(false)
          }}
          onClose={() => setPickerOpen(false)}
        />
      )}
    </>
  )
}
