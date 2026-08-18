import { useRef, useState } from 'react'
import { Download, Upload } from 'lucide-react'
import type { Pipeline } from '../../api/client'
import { ApiError, exportPipelineVersion, importPipeline } from '../../api/client'

/*
  Import/Export пайплайнов (T-21 UI): Export — скачивание YAML версии
  (<name>-v<N>.yaml через blob-ссылку), Import — загрузка .yaml →
  POST /pipelines/import {yaml, on_conflict: "new"}. Ошибки — человекочитаемо.
*/

export function ExportPipelineButton({ pipeline, className }: { pipeline: Pipeline; className?: string }) {
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const doExport = () => {
    if (busy) return
    setBusy(true)
    setError(null)
    exportPipelineVersion(pipeline.id)
      .then((yaml) => {
        const blob = new Blob([yaml], { type: 'text/yaml' })
        const url = URL.createObjectURL(blob)
        const link = document.createElement('a')
        link.href = url
        link.download = `${pipeline.name}-v${pipeline.version}.yaml`
        link.click()
        URL.revokeObjectURL(url)
      })
      .catch((err: unknown) => setError(err instanceof Error ? err.message : 'ошибка экспорта'))
      .finally(() => setBusy(false))
  }

  return (
    <span className={className}>
      <button
        type="button"
        onClick={doExport}
        disabled={busy}
        title={`скачать ${pipeline.name}-v${pipeline.version}.yaml`}
        className="flex items-center gap-1 rounded-md border border-zinc-700 px-2 py-1 text-xs text-zinc-400 hover:bg-zinc-800 hover:text-zinc-200 disabled:opacity-50"
      >
        <Download className="size-3.5" aria-hidden /> Export
      </button>
      {error && <span className="ml-2 text-xs text-red-400">{error}</span>}
    </span>
  )
}

export function ImportPipelineButton({ onImported, className }: { onImported: () => void; className?: string }) {
  const fileRef = useRef<HTMLInputElement>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const onFile = (file: File) => {
    if (busy) return
    setBusy(true)
    setError(null)
    file
      .text()
      .then((yaml) => importPipeline({ yaml, on_conflict: 'new' }))
      .then(() => onImported())
      .catch((err: unknown) => {
        // Error{code,message} бэкенда — человекочитаемо (битый yaml, неизвестная версия формата)
        setError(err instanceof ApiError ? `${err.code}: ${err.message}` : err instanceof Error ? err.message : 'ошибка импорта')
      })
      .finally(() => {
        setBusy(false)
        if (fileRef.current) fileRef.current.value = '' // тот же файл можно выбрать повторно
      })
  }

  return (
    <span className={className}>
      <input
        ref={fileRef}
        type="file"
        accept=".yaml,.yml"
        className="hidden"
        onChange={(e) => {
          const file = e.target.files?.[0]
          if (file) onFile(file)
        }}
      />
      <button
        type="button"
        onClick={() => fileRef.current?.click()}
        disabled={busy}
        className="flex items-center gap-1 rounded-md border border-zinc-700 px-2 py-1 text-xs text-zinc-400 hover:bg-zinc-800 hover:text-zinc-200 disabled:opacity-50"
      >
        <Upload className="size-3.5" aria-hidden /> {busy ? 'импорт…' : 'Import'}
      </button>
      {error && <span className="ml-2 text-xs text-red-400">{error}</span>}
    </span>
  )
}
