import { describe, expect, it } from 'vitest'
import { extractDefaultAnswer, parseGateQuestions } from './gateQuestions'

/*
  Разбор questions.md гейта question на карточки (гейт-вью):
  нумерованный список, преамбула в первой карточке, дефолты, фолбэк.
*/

describe('parseGateQuestions', () => {
  it('нумерованный список → карточки, преамбула — в первой', () => {
    const text = 'Контекст задачи.\n\n1. Первый вопрос?\n2. Второй вопрос?\n   уточнение'
    const questions = parseGateQuestions(text)
    expect(questions).toHaveLength(2)
    expect(questions[0].title).toBe('Первый вопрос?')
    expect(questions[0].body).toContain('Контекст задачи.')
    expect(questions[1].body).toContain('уточнение')
  })

  it('заголовки «### 1. » и формат «2) » тоже разбираются', () => {
    const questions = parseGateQuestions('### 1. Раз\n2) Два')
    expect(questions).toHaveLength(2)
    expect(questions[0].title).toBe('Раз')
  })

  it('фолбэк без нумерации — одна карточка со всем текстом', () => {
    const text = 'Просто вопрос без списка?'
    const questions = parseGateQuestions(text)
    expect(questions).toHaveLength(1)
    expect(questions[0].body).toBe(text)
  })

  it('пустой текст — ни одной карточки', () => {
    expect(parseGateQuestions('  ')).toEqual([])
  })
})

describe('extractDefaultAnswer', () => {
  it('«Ответ по умолчанию: …»', () => {
    expect(extractDefaultAnswer('Вопрос?\nОтвет по умолчанию: да, берём Redis')).toBe('да, берём Redis')
  })

  it('«по умолчанию — …» с бэктиками', () => {
    expect(extractDefaultAnswer('Вопрос?\nпо умолчанию — `main`')).toBe('main')
  })

  it('нет дефолта — пустая строка', () => {
    expect(extractDefaultAnswer('Вопрос без подсказки')).toBe('')
  })
})
