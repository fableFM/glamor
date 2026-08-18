import { KNOWN_PLACEHOLDERS } from './pipelineModel'

/*
  Автодополнение плейсхолдеров в редакторе промпта (T-20, fix-task-2 П.7).
  Триггер — ввод `{{` перед курсором; дальше список фильтруется по
  префиксу. Источник имён — KNOWN_PLACEHOLDERS (зеркало backend
  validate.go knownPlaceholders) + семейство artifact.<file>.
*/

export interface PlaceholderQuery {
  /** индекс начала `{{` в тексте */
  start: number
  /** набранный префикс после `{{` */
  query: string
}

/** незакрытый `{{<prefix>` непосредственно перед курсором */
const TRIGGER_RE = /\{\{([\w.]*)$/

/** Есть ли перед курсором незакрытый `{{...`; null — автодополнение скрыто. */
export function extractPlaceholderQuery(value: string, cursor: number): PlaceholderQuery | null {
  if (cursor < 0 || cursor > value.length) return null
  const match = TRIGGER_RE.exec(value.slice(0, cursor))
  if (!match) return null
  return { start: cursor - match[0].length, query: match[1] }
}

/** Кандидаты автодополнения: известные имена + префикс семейства artifact. */
export const PLACEHOLDER_SUGGESTIONS: readonly string[] = [...KNOWN_PLACEHOLDERS, 'artifact.']

/** Фильтрация кандидатов по префиксу (пустой префикс — весь список). */
export function filterPlaceholders(query: string): string[] {
  return PLACEHOLDER_SUGGESTIONS.filter((name) => name.startsWith(query))
}

/**
 * Вставка выбранного плейсхолдера: диапазон `{{<query>` заменяется на
 * `{{name}}`, курсор ставится сразу после вставленного.
 */
export function applyPlaceholder(
  value: string,
  target: PlaceholderQuery,
  name: string,
): { value: string; cursor: number } {
  const insert = `{{${name}}}`
  const next = value.slice(0, target.start) + insert + value.slice(target.start + 2 + target.query.length)
  return { value: next, cursor: target.start + insert.length }
}
