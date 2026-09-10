import { describe, expect, it } from 'vitest'
import { lessonBody, lineDiff, parseLessonOperations } from './lessonOps'

/*
  Парсер операций distill (T-30) — зеркало backend ParseOperations:
  индексы операций используются в lesson_ops per-card резолва гейта,
  поэтому правила пропуска битых карточек проверяем явно.
*/

const NEW_CARD = `---
title: Проверять миграции goose
triggers: [goose, миграции]
---

## Ситуация
Что-то случилось.
`

describe('parseLessonOperations', () => {
  it('карточка без op: — new (обратная совместимость T-29)', () => {
    const ops = parseLessonOperations(NEW_CARD)
    expect(ops).toHaveLength(1)
    expect(ops[0].op).toBe('new')
    expect(ops[0].title).toBe('Проверять миграции goose')
    expect(ops[0].triggers).toEqual(['goose', 'миграции'])
    expect(ops[0].body).toContain('## Ситуация')
  })

  it('refine/supersede с target, link с двумя id', () => {
    const ops = parseLessonOperations(`---
op: refine
target: lesson-1
title: Уточнение
---

новое тело
---
op: supersede
target: lesson-2
title: Замена
---

тело замены
---
op: link
target: lesson-1
target2: lesson-2
---
`)
    expect(ops.map((o) => o.op)).toEqual(['refine', 'supersede', 'link'])
    expect(ops[0].targetId).toBe('lesson-1')
    expect(ops[2].target2Id).toBe('lesson-2')
  })

  it('«ВОПРОС:» в теле карточки без op: — question (конвенция T-29)', () => {
    const ops = parseLessonOperations(`---
title: Уточнить
---

## Причина
ВОПРОС: куда сохранять?
`)
    expect(ops[0].op).toBe('question')
  })

  it('битые операции пропускаются, индексы валидных совпадают с backend', () => {
    const ops = parseLessonOperations(`---
op: refine
title: Без target — пропуск
---

тело
${NEW_CARD}---
op: link
target: lesson-1
target2: lesson-1
---
---
title:
---

нет title — пропуск
`)
    // refine без target, self-link и new без title пропущены — остаётся new
    expect(ops.map((o) => o.op)).toEqual(['new'])
  })

  it('NO_LESSONS и мусор → пустой список', () => {
    expect(parseLessonOperations('NO_LESSONS')).toEqual([])
    expect(parseLessonOperations('')).toEqual([])
  })

  it('оборванный frontmatter (LLM-транкейт) карточки не даёт — как backend', () => {
    // файл оборвался внутри frontmatter валидной LINK-карточки:
    // backend splitCards завершает карточку по EOF только из тела
    expect(
      parseLessonOperations('---\nop: link\ntarget: lesson-1\ntarget2: lesson-2'),
    ).toEqual([])
    // валидная карточка до обрыва сохраняет свой индекс (0)
    const ops = parseLessonOperations(`${NEW_CARD}---\nop: refine\ntarget: lesson-1`)
    expect(ops).toHaveLength(1)
    expect(ops[0].op).toBe('new')
  })

  it('vendor-поля карточки разбираются', () => {
    const ops = parseLessonOperations(`---
op: new
kind: vendor
vendor: goose
vendor_version: v3.24.1
area: миграции
title: AddMigrationContext обязателен
---

тело
`)
    expect(ops[0].kind).toBe('vendor')
    expect(ops[0].vendor).toBe('goose')
    expect(ops[0].vendorVersion).toBe('v3.24.1')
    expect(ops[0].area).toBe('миграции')
  })
})

describe('lessonBody', () => {
  it('возвращает тело без frontmatter', () => {
    expect(lessonBody(NEW_CARD)).toBe('## Ситуация\nЧто-то случилось.')
  })

  it('без frontmatter — весь текст', () => {
    expect(lessonBody('просто текст')).toBe('просто текст')
  })
})

describe('lineDiff', () => {
  it('одинаковые тексты — все same', () => {
    expect(lineDiff('a\nb', 'a\nb')).toEqual([
      { type: 'same', text: 'a' },
      { type: 'same', text: 'b' },
    ])
  })

  it('замена строки — del + add', () => {
    expect(lineDiff('a\nb\nc', 'a\nx\nc')).toEqual([
      { type: 'same', text: 'a' },
      { type: 'del', text: 'b' },
      { type: 'add', text: 'x' },
      { type: 'same', text: 'c' },
    ])
  })

  it('добавление в конец', () => {
    expect(lineDiff('a', 'a\nb')).toEqual([
      { type: 'same', text: 'a' },
      { type: 'add', text: 'b' },
    ])
  })
})
