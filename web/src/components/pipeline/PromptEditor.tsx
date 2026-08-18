import { useEffect, useMemo, useRef, useState } from 'react'
import { KNOWN_PLACEHOLDERS } from '../../lib/pipelineModel'
import {
  applyPlaceholder,
  extractPlaceholderQuery,
  filterPlaceholders,
  type PlaceholderQuery,
} from '../../lib/promptAutocomplete'

/*
  Редактор промпт-шаблона (T-20): textarea с overlay-подсветкой
  плейсхолдеров {{...}} — известные фиолетовые, неизвестные красные.
  Overlay — <pre> под прозрачным textarea с точно теми же метриками
  шрифта/отступов; скролл синхронизируется. Ниже — легенда доступных
  плейсхолдеров движка T-17.
  Автодополнение (fix-task-2 П.7): ввод `{{` открывает попап со списком
  известных плейсхолдеров (KNOWN_PLACEHOLDERS + artifact.<file>),
  фильтрация по префиксу; ↑/↓ — выбор, Enter/Tab — вставка, Esc — закрыть.
*/

const PLACEHOLDER_RE = /(\{\{\s*[\w.]+\s*\}\})/g
const PLACEHOLDER_NAME_RE = /\{\{\s*([\w.]+)\s*\}\}/

function escapeHtml(text: string): string {
  return text.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
}

function isKnown(token: string): boolean {
  const name = PLACEHOLDER_NAME_RE.exec(token)?.[1] ?? ''
  if (name.startsWith('artifact.')) return name.length > 'artifact.'.length
  return (KNOWN_PLACEHOLDERS as readonly string[]).includes(name)
}

/** общий стиль textarea и overlay — метрики обязаны совпадать */
const TEXT_METRICS =
  'm-0 box-border h-full w-full resize-none overflow-auto whitespace-pre-wrap break-words rounded-md border p-2 font-mono text-xs leading-5'

export function PromptEditor({
  value,
  onChange,
  readOnly,
}: {
  value: string
  onChange?: (value: string) => void
  readOnly?: boolean
}) {
  const overlayRef = useRef<HTMLPreElement>(null)
  const textareaRef = useRef<HTMLTextAreaElement>(null)
  // автодополнение плейсхолдеров
  const [query, setQuery] = useState<PlaceholderQuery | null>(null)
  const [activeIndex, setActiveIndex] = useState(0)
  const pendingCursorRef = useRef<number | null>(null)

  const suggestions = useMemo(() => (query ? filterPlaceholders(query.query) : []), [query])
  const open = !readOnly && query !== null && suggestions.length > 0

  // после вставки плейсхолдера возвращаем курсор за вставленный текст
  useEffect(() => {
    const textarea = textareaRef.current
    if (textarea && pendingCursorRef.current !== null) {
      textarea.focus()
      textarea.setSelectionRange(pendingCursorRef.current, pendingCursorRef.current)
      pendingCursorRef.current = null
    }
  }, [value])

  const highlighted = useMemo(() => {
    const parts = value.split(PLACEHOLDER_RE)
    return parts
      .map((part) => {
        if (!PLACEHOLDER_NAME_RE.test(part)) return escapeHtml(part)
        const cls = isKnown(part)
          ? 'rounded bg-violet-500/25 text-violet-300'
          : 'rounded bg-red-500/25 text-red-300'
        return `<mark class="${cls}">${escapeHtml(part)}</mark>`
      })
      .join('')
  }, [value])

  const syncScroll = (event: React.UIEvent<HTMLTextAreaElement>) => {
    const overlay = overlayRef.current
    if (!overlay) return
    overlay.scrollTop = event.currentTarget.scrollTop
    overlay.scrollLeft = event.currentTarget.scrollLeft
  }

  const handleChange = (event: React.ChangeEvent<HTMLTextAreaElement>) => {
    onChange?.(event.target.value)
    setQuery(extractPlaceholderQuery(event.target.value, event.target.selectionStart))
    setActiveIndex(0)
  }

  const applySuggestion = (name: string) => {
    if (!query) return
    const result = applyPlaceholder(value, query, name)
    pendingCursorRef.current = result.cursor
    onChange?.(result.value)
    setQuery(null)
  }

  const handleKeyDown = (event: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (!open) return
    if (event.key === 'ArrowDown') {
      event.preventDefault()
      setActiveIndex((i) => (i + 1) % suggestions.length)
    } else if (event.key === 'ArrowUp') {
      event.preventDefault()
      setActiveIndex((i) => (i - 1 + suggestions.length) % suggestions.length)
    } else if (event.key === 'Enter' || event.key === 'Tab') {
      event.preventDefault()
      applySuggestion(suggestions[activeIndex])
    } else if (event.key === 'Escape') {
      setQuery(null)
    }
  }

  return (
    <div>
      <div className="relative h-56">
        <pre
          ref={overlayRef}
          aria-hidden
          className={`${TEXT_METRICS} pointer-events-none absolute inset-0 border-transparent bg-zinc-950 text-zinc-200`}
        >
          {/* trailing \n: иначе последняя пустая строка не совпадёт по высоте с textarea */}
          <span dangerouslySetInnerHTML={{ __html: `${highlighted}\n` }} />
        </pre>
        <textarea
          ref={textareaRef}
          value={value}
          readOnly={readOnly}
          onChange={handleChange}
          onKeyDown={handleKeyDown}
          onScroll={syncScroll}
          onBlur={() => setQuery(null)}
          spellCheck={false}
          className={`${TEXT_METRICS} relative border-zinc-700 bg-transparent text-transparent caret-violet-400 selection:bg-violet-500/30 selection:text-transparent focus:border-violet-500 focus:outline-none`}
        />
        {open && (
          <ul className="absolute inset-x-1 bottom-1 z-10 max-h-36 overflow-auto rounded-md border border-zinc-700 bg-zinc-900 py-1 shadow-lg">
            {suggestions.map((name, index) => (
              <li key={name}>
                <button
                  type="button"
                  // mousedown+preventDefault: выбор до blur textarea
                  onMouseDown={(e) => {
                    e.preventDefault()
                    applySuggestion(name)
                  }}
                  onMouseEnter={() => setActiveIndex(index)}
                  className={`block w-full px-2 py-1 text-left font-mono text-xs ${
                    index === activeIndex ? 'bg-violet-600/30 text-violet-200' : 'text-zinc-400'
                  }`}
                >
                  {`{{${name}}}`}
                </button>
              </li>
            ))}
          </ul>
        )}
      </div>
      <details className="mt-1.5">
        <summary className="cursor-pointer text-xs text-zinc-600">
          доступные плейсхолдеры (автодополнение — ввод {'{{'})
        </summary>
        <div className="mt-1 flex flex-wrap gap-1">
          {KNOWN_PLACEHOLDERS.map((name) => (
            <code key={name} className="rounded bg-zinc-800 px-1.5 py-0.5 text-[10px] text-zinc-400">
              {`{{${name}}}`}
            </code>
          ))}
          <code className="rounded bg-zinc-800 px-1.5 py-0.5 text-[10px] text-zinc-400">{'{{artifact.<file>}}'}</code>
        </div>
      </details>
    </div>
  )
}
