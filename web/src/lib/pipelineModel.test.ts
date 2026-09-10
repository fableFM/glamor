import { describe, expect, it } from 'vitest'
import type { PipelineSpec, SpecStage } from './pipelineModel'
import { emptyStage, parseSpec, serializeSpec, validateSpec } from './pipelineModel'

/*
  F-05/F-08: валидация спеки пайплайна и round-trip parse/serialize.
  Правила зеркалят backend ValidateSpec (плейсхолдеры — точное совпадение,
  любой регистр/пробелы в {{...}} — ошибка) + обязательные поля llm-нод.
*/

function makeStage(overrides: Partial<SpecStage> = {}): SpecStage {
  return { ...emptyStage(0), model: 'kimi-k2', prompt_template: 'do {{task}}', ...overrides }
}

function makeSpec(overrides: Partial<PipelineSpec> = {}): PipelineSpec {
  return {
    stages: [makeStage({ key: 'plan' }), makeStage({ key: 'code' })],
    loop: null,
    final_gate: true,
    lessons: true,
    ...overrides,
  }
}

function messages(spec: PipelineSpec): string[] {
  return validateSpec(spec).map((e) => e.message)
}

describe('validateSpec', () => {
  it('валидный пайплайн — без ошибок', () => {
    expect(validateSpec(makeSpec())).toEqual([])
  })

  it('пустой пайплайн — ошибка', () => {
    expect(messages(makeSpec({ stages: [] }))).toContain('в пайплайне нет ни одного этапа')
  })

  it('пустой key и дубликат key', () => {
    const spec = makeSpec({ stages: [makeStage({ key: '' }), makeStage({ key: 'a' }), makeStage({ key: 'a' })] })
    const msgs = messages(spec)
    expect(msgs).toContain('пустой key этапа')
    expect(msgs).toContain('дубликат key «a»')
  })

  it('F-08: пустой prompt_template / model / harness — ошибка с привязкой к ноде', () => {
    const spec = makeSpec({
      stages: [
        makeStage({ key: 'p', prompt_template: '  ' }),
        makeStage({ key: 'm', model: '' }),
        makeStage({ key: 'h', harness: '' as SpecStage['harness'] }),
      ],
    })
    const errors = validateSpec(spec)
    expect(errors).toContainEqual({ stageKey: 'p', message: '«p»: пустой prompt_template' })
    expect(errors).toContainEqual({ stageKey: 'm', message: '«m»: пустой model' })
    expect(errors).toContainEqual({ stageKey: 'h', message: '«h»: пустой harness' })
  })

  it('F-08: обязательные поля проверяются только у llm-stage (human-gate без промпта валиден)', () => {
    const spec = makeSpec({
      stages: [makeStage({ key: 'gate', kind: 'human-gate', model: '', prompt_template: '' })],
    })
    expect(validateSpec(spec)).toEqual([])
  })

  it('required-артефакт с пустым path — ошибка', () => {
    const spec = makeSpec({ stages: [makeStage({ key: 'a', artifact: { path: '', required: true } })] })
    expect(messages(spec)).toContain('«a»: required-артефакт с пустым path')
  })

  it('неизвестный плейсхолдер — ошибка', () => {
    const spec = makeSpec({ stages: [makeStage({ key: 'a', prompt_template: '{{unknown}}' })] })
    expect(messages(spec)).toContain('«a»: неизвестный плейсхолдер {{unknown}}')
  })

  it('F-07: плейсхолдеры в верхнем регистре и с пробелами — ошибка (как backend)', () => {
    const upper = makeSpec({ stages: [makeStage({ key: 'a', prompt_template: '{{Task}}' })] })
    expect(messages(upper)).toContain('«a»: неизвестный плейсхолдер {{Task}}')
    const spaced = makeSpec({ stages: [makeStage({ key: 'a', prompt_template: '{{ task }}' })] })
    expect(messages(spaced)).toContain('«a»: неизвестный плейсхолдер {{ task }}')
  })

  it('artifact.<path> — валидное семейство плейсхолдеров; пустой суффикс — нет', () => {
    const ok = makeSpec({ stages: [makeStage({ key: 'a', prompt_template: '{{artifact.plan.md}} {{task}}' })] })
    expect(validateSpec(ok)).toEqual([])
    const bad = makeSpec({ stages: [makeStage({ key: 'a', prompt_template: '{{artifact.}}' })] })
    expect(messages(bad)).toContain('«a»: неизвестный плейсхолдер {{artifact.}}')
  })

  it('loop: несуществующие этапы и max_iters < 1 — ошибки', () => {
    const spec = makeSpec({ loop: { from: 'nope', to: 'code', max_iters: 1 } })
    expect(messages(spec)).toContain('loop.from: этап «nope» не существует')
    const spec2 = makeSpec({ loop: { from: 'plan', to: 'code', max_iters: 0 } })
    expect(messages(spec2)).toContain('loop.max_iters должен быть >= 1')
  })
})

describe('parseSpec/serializeSpec round-trip', () => {
  it('serialize → parse сохраняет модель', () => {
    const spec = makeSpec({
      stages: [
        makeStage({
          key: 'plan',
          model: 'kimi-k2',
          effort: 'max',
          prompt_template: 'plan {{task}} on {{base_branch}}',
          artifact: { path: 'plan.md', required: true },
          gate_after: 'plan_approval',
        }),
        makeStage({ key: 'code', harness: 'qwen', model: 'qwen3-coder', questions_path: 'questions.md' }),
      ],
      loop: { from: 'plan', to: 'code', max_iters: 3 },
      final_gate: true,
    })
    const parsed = parseSpec(serializeSpec(spec))
    expect(parsed).toEqual(spec)
  })

  it('parse → serialize → parse стабилен (идемпотентен)', () => {
    const json = JSON.stringify({
      stages: [
        {
          key: 'review',
          kind: 'llm-stage',
          harness: 'kimi',
          model: 'm',
          effort: 'low',
          prompt_template: '{{verdict}}',
          artifact: { path: 'verdict.md', required: true },
        },
      ],
      loop: { from: 'review', to: 'review', max_iters: 2 },
      final_gate: 'final_review',
    })
    const once = parseSpec(json)
    expect(once).not.toBeNull()
    expect(parseSpec(serializeSpec(once!))).toEqual(once)
  })

  it('битый JSON и не-спека → null', () => {
    expect(parseSpec('{')).toBeNull()
    expect(parseSpec('{}')).toBeNull()
    expect(parseSpec('{"stages": "nope"}')).toBeNull()
  })

  it('lessons (T-30): отсутствие ключа = on, "off" — мастер-выключатель, round-trip', () => {
    const base = { stages: [{ key: 'plan', kind: 'llm-stage', harness: 'kimi', model: 'm', prompt_template: '{{task}}' }] }
    // ключа нет → включено, в сериализации не появляется
    const on = parseSpec(JSON.stringify(base))
    expect(on?.lessons).toBe(true)
    expect(serializeSpec(on!)).not.toContain('lessons')
    // lessons: "off" → выключено и сериализуется обратно
    const off = parseSpec(JSON.stringify({ ...base, lessons: 'off' }))
    expect(off?.lessons).toBe(false)
    expect(parseSpec(serializeSpec(off!))?.lessons).toBe(false)
  })
})
