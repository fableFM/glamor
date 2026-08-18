import type { Artifact } from '../api/client'

/*
  Чистая логика табов «Промпт»/«Артефакты» панели этапа (T-14,
  fix-task-2 П.1). Промпт попытки сохраняется supervisor'ом артефактом
  kind="prompt" с именем prompt-<stage>-<iter>.md (stageproc.go).
  Содержимое читается через GET /runs/{id}/artifacts/{aid}/content
  (F-02, fix-task-4) — здесь только выбор способа рендера по расширению.
*/

/** Итерация из имени prompt-артефакта: prompt-<stageKey>-<iter>.md → iter. */
export function parsePromptIteration(path: string, stageKey: string): number | null {
  // stage_key — [a-z0-9-] по спеке, но экранируем на всякий случай
  const escaped = stageKey.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  const match = new RegExp(`prompt-${escaped}-(\\d+)\\.md$`).exec(path)
  if (!match) return null
  const iteration = Number(match[1])
  return Number.isSafeInteger(iteration) ? iteration : null
}

export interface PromptArtifact {
  artifact: Artifact
  iteration: number | null
}

/**
 * Prompt-артефакты стадии (все итерации), по возрастанию итерации.
 * Критерий: kind === 'prompt' ИЛИ имя файла prompt-<stageKey>-<iter>.md
 * (kind надёжнее, имя — страховка для старых записей).
 */
export function promptArtifactsForStage(
  artifacts: Artifact[],
  stageId: number,
  stageKey: string,
): PromptArtifact[] {
  return artifacts
    .filter((a) => a.stage_id === stageId)
    .filter((a) => a.kind === 'prompt' || parsePromptIteration(a.path, stageKey) !== null)
    .map((artifact) => ({ artifact, iteration: parsePromptIteration(artifact.path, stageKey) }))
    .sort((a, b) => (a.iteration ?? 0) - (b.iteration ?? 0) || a.artifact.id - b.artifact.id)
}

/** Последняя (актуальная) попытка промпта стадии. */
export function latestPromptArtifact(
  artifacts: Artifact[],
  stageId: number,
  stageKey: string,
): PromptArtifact | null {
  const list = promptArtifactsForStage(artifacts, stageId, stageKey)
  return list.length > 0 ? list[list.length - 1] : null
}

/** Короткое имя файла из полного пути артефакта (для списка). */
export function artifactFileName(path: string): string {
  const normalized = path.replace(/\\/g, '/')
  const name = normalized.slice(normalized.lastIndexOf('/') + 1)
  return name.length > 0 ? name : path
}

export type ArtifactRenderKind = 'markdown' | 'diff' | 'text'

/** Способ рендера содержимого артефакта по расширению имени файла (F-02). */
export function artifactRenderKind(path: string): ArtifactRenderKind {
  const name = artifactFileName(path).toLowerCase()
  if (name.endsWith('.md') || name.endsWith('.markdown')) return 'markdown'
  if (name.endsWith('.diff') || name.endsWith('.patch')) return 'diff'
  return 'text'
}
