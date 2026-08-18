import { useEffect, useState } from 'react'
import type { Artifact } from '../../api/client'
import { ApiError, apiBaseUrl, apiToken, getArtifactContent } from '../../api/client'
import { artifactFileName, artifactRenderKind } from '../../lib/artifacts'
import { Markdown } from '../Markdown'

/*
  Просмотр содержимого артефакта (F-02, fix-task-4): загрузка через
  GET /runs/{id}/artifacts/{aid}/content при раскрытии строки.
  Рендер по расширению: .md — Markdown, .diff/.patch — раскраска +/- строк,
  прочее — plain <pre>. 413 (файл > 5 МБ) — плашка + кнопка «скачать».
  Кэша нет: контент живёт в локальном стейте, повторное раскрытие —
  повторный fetch (KISS, файлы артефактов малы).
*/

type LoadState =
  | { status: 'loading' }
  | { status: 'ok'; text: string }
  | { status: 'error'; message: string; tooLarge: boolean }

/** Скачивание с Bearer-авторизацией: <a href> токен не пронесёт, поэтому fetch→blob→objectURL. */
async function downloadArtifact(artifact: Artifact): Promise<void> {
  const url = `${apiBaseUrl()}/runs/${artifact.run_id}/artifacts/${artifact.id}/content`
  const headers = new Headers()
  const token = apiToken()
  if (token) headers.set('Authorization', `Bearer ${token}`)
  const response = await fetch(url, { headers })
  if (!response.ok) throw new ApiError(response.status, 'internal', `HTTP ${response.status}`)
  const blob = await response.blob()
  const objectUrl = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = objectUrl
  link.download = artifactFileName(artifact.path)
  link.click()
  URL.revokeObjectURL(objectUrl)
}

/** Моноширинный блок diff с раскраской +/- строк (без зависимостей). */
function DiffView({ text }: { text: string }) {
  return (
    <pre className="overflow-x-auto font-mono text-xs leading-5">
      {text.split('\n').map((line, index) => {
        let className = 'text-zinc-400'
        if (line.startsWith('+') && !line.startsWith('+++')) className = 'bg-emerald-500/10 text-emerald-300'
        else if (line.startsWith('-') && !line.startsWith('---')) className = 'bg-red-500/10 text-red-300'
        else if (line.startsWith('@@')) className = 'text-sky-300'
        return (
          <div key={index} className={className}>
            {line.length > 0 ? line : ' '}
          </div>
        )
      })}
    </pre>
  )
}

export function ArtifactContent({ artifact }: { artifact: Artifact }) {
  const [state, setState] = useState<LoadState>({ status: 'loading' })
  const [downloadError, setDownloadError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    getArtifactContent(artifact.run_id, artifact.id)
      .then((text) => {
        if (!cancelled) setState({ status: 'ok', text })
      })
      .catch((error: unknown) => {
        if (cancelled) return
        if (error instanceof ApiError && error.status === 413) {
          setState({ status: 'error', message: error.message, tooLarge: true })
        } else {
          const message = error instanceof Error ? error.message : 'не удалось загрузить содержимое'
          setState({ status: 'error', message, tooLarge: false })
        }
      })
    return () => {
      cancelled = true
    }
  }, [artifact.run_id, artifact.id])

  if (state.status === 'loading') {
    return <p className="px-1 py-2 text-xs text-zinc-500">загрузка содержимого…</p>
  }

  if (state.status === 'error') {
    return (
      <div className="space-y-2">
        <div className="rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-xs text-red-300">
          {state.tooLarge ? `файл слишком большой для просмотра: ${state.message}` : state.message}
        </div>
        {state.tooLarge && (
          <button
            type="button"
            onClick={() => {
              setDownloadError(null)
              downloadArtifact(artifact).catch((error: unknown) => {
                setDownloadError(error instanceof Error ? error.message : 'не удалось скачать файл')
              })
            }}
            className="rounded-md border border-zinc-700 px-2.5 py-1 text-xs text-zinc-300 hover:bg-zinc-800"
          >
            скачать {artifactFileName(artifact.path)}
          </button>
        )}
        {downloadError !== null && <p className="text-xs text-red-300">{downloadError}</p>}
      </div>
    )
  }

  const kind = artifactRenderKind(artifact.path)
  if (kind === 'markdown') return <Markdown>{state.text}</Markdown>
  if (kind === 'diff') return <DiffView text={state.text} />
  return <pre className="overflow-x-auto font-mono text-xs leading-5 text-zinc-300">{state.text}</pre>
}
