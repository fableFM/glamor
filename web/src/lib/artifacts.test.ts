import { describe, expect, it } from 'vitest'
import type { Artifact } from '../api/client'
import {
  artifactFileName,
  artifactRenderKind,
  latestPromptArtifact,
  parsePromptIteration,
  promptArtifactsForStage,
} from './artifacts'

function artifact(id: number, stageId: number | null, path: string, kind = 'prompt'): Artifact {
  return { id, run_id: 'run-1', stage_id: stageId, path, kind, created_at: '2026-08-17T10:00:00Z' }
}

describe('parsePromptIteration', () => {
  it('парсит итерацию из имени prompt-<stage>-<iter>.md', () => {
    expect(parsePromptIteration('/home/u/.glamor/runs/r1/prompt-plan-2.md', 'plan')).toBe(2)
    expect(parsePromptIteration('prompt-code-10.md', 'code')).toBe(10)
  })

  it('не путает стадии с общим суффиксом имени', () => {
    expect(parsePromptIteration('prompt-fix-plan-3.md', 'plan')).toBeNull()
    expect(parsePromptIteration('prompt-plan-final.md', 'plan')).toBeNull()
    expect(parsePromptIteration('prompt-plan.md', 'plan')).toBeNull()
    expect(parsePromptIteration('spec.md', 'plan')).toBeNull()
  })
})

describe('promptArtifactsForStage', () => {
  const artifacts = [
    artifact(1, 7, '/r/prompt-plan-1.md'),
    artifact(2, 7, '/r/prompt-plan-2.md'),
    artifact(3, 8, '/r/prompt-code-1.md'), // другая стадия
    artifact(4, 7, '/r/spec.md', 'artifact'), // не промпт
    artifact(5, null, '/r/run-level.md'), // артефакт уровня рана
    artifact(6, 7, '/r/prompt-plan-3.md', 'artifact'), // kind не prompt, но имя совпадает
  ]

  it('отбирает промпты стадии по kind или имени, сортирует по итерации', () => {
    const list = promptArtifactsForStage(artifacts, 7, 'plan')
    expect(list.map((p) => p.artifact.id)).toEqual([1, 2, 6])
    expect(list.map((p) => p.iteration)).toEqual([1, 2, 3])
  })

  it('пусто для стадии без промптов', () => {
    expect(promptArtifactsForStage(artifacts, 9, 'review')).toEqual([])
  })
})

describe('latestPromptArtifact', () => {
  it('возвращает последнюю итерацию', () => {
    const artifacts = [artifact(1, 7, '/r/prompt-plan-2.md'), artifact(2, 7, '/r/prompt-plan-1.md')]
    expect(latestPromptArtifact(artifacts, 7, 'plan')?.artifact.id).toBe(1)
  })

  it('null, когда промпта нет', () => {
    expect(latestPromptArtifact([], 7, 'plan')).toBeNull()
  })
})

describe('artifactFileName', () => {
  it('отрезает каталоги', () => {
    expect(artifactFileName('/home/u/.glamor/runs/r1/spec.md')).toBe('spec.md')
    expect(artifactFileName('spec.md')).toBe('spec.md')
  })
})

describe('artifactRenderKind (F-02)', () => {
  it('.md/.markdown → markdown', () => {
    expect(artifactRenderKind('/x/prompt-plan-1.md')).toBe('markdown')
    expect(artifactRenderKind('notes.markdown')).toBe('markdown')
    expect(artifactRenderKind('README.MD')).toBe('markdown')
  })

  it('.diff/.patch → diff', () => {
    expect(artifactRenderKind('/x/changes.diff')).toBe('diff')
    expect(artifactRenderKind('fix.patch')).toBe('diff')
  })

  it('прочее → text', () => {
    expect(artifactRenderKind('/x/log.txt')).toBe('text')
    expect(artifactRenderKind('session.jsonl')).toBe('text')
    expect(artifactRenderKind('noext')).toBe('text')
  })
})
