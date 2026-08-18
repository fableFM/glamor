import { describe, expect, it } from 'vitest'
import type { Pipeline } from '../api/client'
import { groupPipelinesByName } from './pipelines'

function pipeline(id: number, name: string, version: number): Pipeline {
  return {
    id,
    project_id: 1,
    name,
    version,
    spec_json: '{}',
    created_at: `2026-08-17T10:0${version}:00Z`,
  }
}

describe('groupPipelinesByName', () => {
  it('группирует версии по имени, latest — максимальная версия', () => {
    const groups = groupPipelinesByName([
      pipeline(3, 'default', 2),
      pipeline(1, 'default', 1),
      pipeline(2, 'review', 1),
    ])
    expect(groups.map((g) => g.name)).toEqual(['default', 'review'])
    expect(groups[0].versions.map((p) => p.id)).toEqual([1, 3])
    expect(groups[0].latest.id).toBe(3)
    expect(groups[1].latest.id).toBe(2)
  })

  it('одна версия — группа из одного элемента', () => {
    const groups = groupPipelinesByName([pipeline(1, 'default', 1)])
    expect(groups).toHaveLength(1)
    expect(groups[0].versions).toHaveLength(1)
    expect(groups[0].latest.version).toBe(1)
  })

  it('пустой вход — пустой результат', () => {
    expect(groupPipelinesByName([])).toEqual([])
  })

  it('не мутирует исходный массив порядка версий между группами', () => {
    const input = [pipeline(1, 'a', 1), pipeline(2, 'b', 1), pipeline(3, 'a', 2)]
    const groups = groupPipelinesByName(input)
    expect(groups.map((g) => g.name)).toEqual(['a', 'b'])
    expect(groups[0].versions.map((p) => p.version)).toEqual([1, 2])
  })
})
