import type { Pipeline } from '../api/client'

/*
  Защитный разбор spec_json пайплайна для read-only превью (T-16).
  Точный формат спеки — T-03/T-17; здесь не валидируем, только достаём
  этапы (key + harness/model/effort), если они узнаются. Не узнали —
  честно возвращаем пустой список, UI покажет «без превью».
*/

export interface StagePreview {
  key: string
  harness?: string
  model?: string
  effort?: string
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null
}

function str(value: unknown): string | undefined {
  return typeof value === 'string' && value.length > 0 ? value : undefined
}

export function parsePipelineStages(pipeline: Pipeline | undefined): StagePreview[] {
  if (!pipeline) return []
  let parsed: unknown
  try {
    parsed = JSON.parse(pipeline.spec_json)
  } catch {
    return []
  }
  if (!isRecord(parsed)) return []
  // допускаем оба именования: stages (T-03) и steps
  const rawStages = Array.isArray(parsed.stages) ? parsed.stages : Array.isArray(parsed.steps) ? parsed.steps : null
  if (!rawStages) return []
  const result: StagePreview[] = []
  for (const raw of rawStages) {
    if (!isRecord(raw)) continue
    const key = str(raw.key) ?? str(raw.name) ?? str(raw.id)
    if (!key) continue
    result.push({ key, harness: str(raw.harness), model: str(raw.model), effort: str(raw.effort) })
  }
  return result
}
