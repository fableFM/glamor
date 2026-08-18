import { describe, expect, it } from 'vitest'
import { slugify } from './format'

/* F-05: slug-preview автоимени ветки (glamor/<slug>, D-31). */

describe('slugify', () => {
  it('базовый текст → slug', () => {
    expect(slugify('Add OAuth login')).toBe('add-oauth-login')
  })

  it('спецсимволы и множественные пробелы схлопываются в один дефис', () => {
    expect(slugify('fix:  auth/token  refresh!!')).toBe('fix-auth-token-refresh')
  })

  it('лидирующие/висячие дефисы обрезаются', () => {
    expect(slugify(' --hello-- ')).toBe('hello')
  })

  it('длинный текст режется по maxLength без висячего дефиса', () => {
    const slug = slugify('a'.repeat(50) + ' ' + 'b'.repeat(10))
    expect(slug.length).toBeLessThanOrEqual(40)
    expect(slug).not.toMatch(/-$/)
  })

  it('кириллица не попадает в slug → пустая строка (preview покажет плейсхолдер)', () => {
    expect(slugify('сделай фичу')).toBe('')
  })

  it('смешанный текст: латиница остаётся, кириллица выкидывается', () => {
    expect(slugify('добавить metrics endpoint')).toBe('metrics-endpoint')
  })
})
