import { useMemo, useState } from 'react'
import { Virtuoso } from 'react-virtuoso'
import { AlertTriangle, ChevronDown, ChevronRight, Terminal, Wrench } from 'lucide-react'
import type { Event } from '../../api/client'
import { useStreamsStore } from '../../stores/streams'
import { Markdown } from '../Markdown'
import { formatDateTime } from '../../lib/format'

/*
  Стрим-виджет стадии (T-14): рендер нормализованных событий (D-41).
  - подряд идущие text/thinking/raw события склеиваются в один блок
    (чанки могут резать markdown посередине конструкции);
  - thinking — сворачиваемый блок (развёрнут, пока стадия running);
  - tool_call/tool_result — чипы с раскрытием (усечение длинных payload'ов);
  - error — красная плашка; raw — спойлер;
  - автoскролл с прилипанием: отмотал вверх — не дёргаем.
  Виртуализация — react-virtuoso (F-01): история не режется cap'ом,
  рендерятся только видимые блоки (acceptance T-14: 5000+ событий).
*/

const TOOL_PAYLOAD_MAX = 4000

function str(value: unknown): string | undefined {
  return typeof value === 'string' ? value : undefined
}

function truncate(text: string, max: number): string {
  return text.length > max ? `${text.slice(0, max)}\n… (усечено, ${text.length} символов)` : text
}

/** Склейка подряд идущих однотипных событий в блоки ленты. */
interface StreamBlock {
  key: string
  kind: 'text' | 'thinking' | 'raw' | 'tool' | 'error'
  text: string
  event?: Event
}

function buildBlocks(events: Event[]): StreamBlock[] {
  const blocks: StreamBlock[] = []
  for (const event of events) {
    const payload = event.payload ?? {}
    if (event.kind === 'stream.text' || event.kind === 'stream.thinking' || event.kind === 'stream.raw') {
      const kind = event.kind === 'stream.text' ? 'text' : event.kind === 'stream.thinking' ? 'thinking' : 'raw'
      const text = str(payload.text) ?? ''
      const last = blocks[blocks.length - 1]
      if (last && last.kind === kind) {
        last.text += text
      } else {
        blocks.push({ key: String(event.id), kind, text })
      }
      continue
    }
    if (event.kind === 'stream.tool_call' || event.kind === 'stream.tool_result') {
      blocks.push({ key: String(event.id), kind: 'tool', text: '', event })
      continue
    }
    if (event.kind === 'stream.error') {
      blocks.push({ key: String(event.id), kind: 'error', text: str(payload.message) ?? 'ошибка', event })
    }
  }
  return blocks
}

/** Краткая подпись tool_call: имя файла/команды из input, если узнаём. */
function toolSummary(payload: Record<string, unknown>): string {
  const input = payload.input
  if (input && typeof input === 'object') {
    const record = input as Record<string, unknown>
    for (const key of ['file_path', 'path', 'command', 'pattern', 'url']) {
      const value = record[key]
      if (typeof value === 'string' && value.length > 0) {
        return value.length > 80 ? `…${value.slice(-80)}` : value
      }
    }
  }
  return ''
}

function ToolChip({ event }: { event: Event }) {
  const [expanded, setExpanded] = useState(false)
  const payload = event.payload ?? {}
  const isCall = event.kind === 'stream.tool_call'
  const isError = payload.is_error === true
  const tool = str(payload.tool) ?? 'tool'

  let detail = ''
  if (isCall) {
    detail = payload.input !== undefined ? JSON.stringify(payload.input, null, 2) ?? '' : ''
  } else {
    detail = str(payload.output) ?? ''
  }

  return (
    <div className="my-1">
      <button
        type="button"
        onClick={() => setExpanded((v) => !v)}
        className={`inline-flex max-w-full items-center gap-1.5 rounded-md border px-2 py-1 text-xs ${
          isError
            ? 'border-red-500/40 bg-red-500/10 text-red-300'
            : 'border-zinc-700 bg-zinc-800/60 text-zinc-300 hover:bg-zinc-800'
        }`}
      >
        {expanded ? <ChevronDown className="size-3" aria-hidden /> : <ChevronRight className="size-3" aria-hidden />}
        {isCall ? <Wrench className="size-3" aria-hidden /> : <Terminal className="size-3" aria-hidden />}
        <span className="font-mono font-medium">{tool}</span>
        <span className="truncate text-zinc-500">{isCall ? toolSummary(payload) : isError ? 'ошибка' : 'результат'}</span>
      </button>
      {expanded && detail && (
        <pre className="mt-1 max-h-64 overflow-auto rounded-md border border-zinc-800 bg-zinc-900 p-2 text-xs text-zinc-400">
          {truncate(detail, TOOL_PAYLOAD_MAX)}
        </pre>
      )}
    </div>
  )
}

function ThinkingBlock({ text, defaultOpen }: { text: string; defaultOpen: boolean }) {
  return (
    <details open={defaultOpen} className="my-1 rounded-md border border-zinc-800 bg-zinc-900/40 px-3 py-1.5">
      <summary className="cursor-pointer select-none text-xs italic text-zinc-500">рассуждения</summary>
      <div className="pt-1 text-sm italic text-zinc-400">
        <Markdown>{text}</Markdown>
      </div>
    </details>
  )
}

export function StreamView({ stageId, stageRunning }: { stageId: number; stageRunning: boolean }) {
  const events = useStreamsStore((s) => s.byStageId[stageId])
  const blocks = useMemo(() => buildBlocks(events ?? []), [events])

  if (blocks.length === 0) {
    return <p className="p-4 text-sm text-zinc-600">стрим пуст — события появятся, когда этап начнёт писать</p>
  }

  return (
    <Virtuoso
      className="h-full px-4 py-2"
      // горизонтальный скролл гасим: длинные строки переносятся (.markdown-body),
      // код скроллится внутри собственного pre
      style={{ overflowX: 'hidden' }}
      data={blocks}
      // прилипание к низу: скроллим следом, только если пользователь у низа
      followOutput="auto"
      initialTopMostItemIndex={blocks.length - 1}
      itemContent={(_, block) => {
        switch (block.kind) {
          case 'text':
            return <Markdown>{block.text}</Markdown>
          case 'thinking':
            return <ThinkingBlock text={block.text} defaultOpen={stageRunning} />
          case 'raw':
            return (
              <details className="my-1">
                <summary className="cursor-pointer select-none text-xs text-zinc-600">raw output</summary>
                <pre className="mt-1 max-h-64 overflow-auto rounded-md border border-zinc-800 bg-zinc-900 p-2 text-xs text-zinc-500">
                  {block.text}
                </pre>
              </details>
            )
          case 'tool':
            return block.event ? <ToolChip event={block.event} /> : null
          case 'error':
            return (
              <div className="my-2 flex items-start gap-2 rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
                <AlertTriangle className="mt-0.5 size-4 shrink-0" aria-hidden />
                <div>
                  {block.text}
                  {block.event?.payload?.retriable === true && (
                    <span className="ml-2 text-xs text-red-400/70">(retriable)</span>
                  )}
                  {block.event && <span className="ml-2 text-xs text-zinc-600">{formatDateTime(block.event.ts)}</span>}
                </div>
              </div>
            )
          default:
            return null
        }
      }}
    />
  )
}
