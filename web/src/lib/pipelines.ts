import type { Pipeline } from '../api/client'

/*
  Группировка пайплайнов по имени для табов экрана проекта (T-16,
  fix-task-2 П.5): listProjectPipelines возвращает каждую версию
  отдельной строкой, а таб должен быть один на пайплайн — активна
  последняя версия, старые доступны переключателем версии.
*/

export interface PipelineGroup {
  name: string
  /** версии по возрастанию version */
  versions: Pipeline[]
  /** последняя версия (активна по умолчанию) */
  latest: Pipeline
}

/**
 * Группировка версий по имени пайплайна. Порядок групп — порядок первого
 * появления имени в ответе API; версии внутри — по возрастанию version.
 */
export function groupPipelinesByName(pipelines: Pipeline[]): PipelineGroup[] {
  const byName = new Map<string, Pipeline[]>()
  for (const pipeline of pipelines) {
    const list = byName.get(pipeline.name)
    if (list) {
      list.push(pipeline)
    } else {
      byName.set(pipeline.name, [pipeline])
    }
  }
  const groups: PipelineGroup[] = []
  for (const [name, versions] of byName) {
    versions.sort((a, b) => a.version - b.version)
    groups.push({ name, versions, latest: versions[versions.length - 1] })
  }
  return groups
}
