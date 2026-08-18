import { describe, expect, it } from 'vitest'
import {
  applyPlaceholder,
  extractPlaceholderQuery,
  filterPlaceholders,
  PLACEHOLDER_SUGGESTIONS,
} from './promptAutocomplete'
import { KNOWN_PLACEHOLDERS } from './pipelineModel'

describe('extractPlaceholderQuery', () => {
  it('срабатывает на {{ перед курсором', () => {
    expect(extractPlaceholderQuery('текст {{', 8)).toEqual({ start: 6, query: '' })
  })

  it('забирает набранный префикс', () => {
    expect(extractPlaceholderQuery('{{ta', 4)).toEqual({ start: 0, query: 'ta' })
    expect(extractPlaceholderQuery('x {{artifact.sp', 15)).toEqual({ start: 2, query: 'artifact.sp' })
  })

  it('не срабатывает вне триггера', () => {
    expect(extractPlaceholderQuery('просто текст', 6)).toBeNull()
    expect(extractPlaceholderQuery('{{task}}', 8)).toBeNull() // уже закрыт
    expect(extractPlaceholderQuery('{ один}', 3)).toBeNull()
    expect(extractPlaceholderQuery('{{ ta', 4)).toBeNull() // пробел — не префикс
  })

  it('учитывает позицию курсора, а не конец строки', () => {
    // курсор сразу после {{, хвост строки не влияет
    expect(extractPlaceholderQuery('{{}} tail', 2)).toEqual({ start: 0, query: '' })
  })
})

describe('filterPlaceholders', () => {
  it('пустой префикс — все кандидаты (known + artifact.)', () => {
    const all = filterPlaceholders('')
    expect(all).toHaveLength(KNOWN_PLACEHOLDERS.length + 1)
    expect(all).toContain('task')
    expect(all).toContain('artifact.')
  })

  it('фильтрует по префиксу', () => {
    expect(filterPlaceholders('ta')).toEqual(['task'])
    expect(filterPlaceholders('depth')).toEqual(['depth', 'depth_instructions'])
    expect(filterPlaceholders('zzz')).toEqual([])
  })

  it('список синхронизирован с KNOWN_PLACEHOLDERS', () => {
    expect(PLACEHOLDER_SUGGESTIONS.slice(0, KNOWN_PLACEHOLDERS.length)).toEqual([...KNOWN_PLACEHOLDERS])
  })
})

describe('applyPlaceholder', () => {
  it('заменяет {{<query> на полный плейсхолдер и двигает курсор', () => {
    const result = applyPlaceholder('делай {{ta тут', { start: 6, query: 'ta' }, 'task')
    expect(result.value).toBe('делай {{task}} тут')
    expect(result.cursor).toBe(6 + '{{task}}'.length)
  })

  it('вставка artifact. оставляет курсор для добивки имени файла', () => {
    const result = applyPlaceholder('{{', { start: 0, query: '' }, 'artifact.')
    expect(result.value).toBe('{{artifact.}}')
    expect(result.cursor).toBe('{{artifact.}}'.length)
  })
})
