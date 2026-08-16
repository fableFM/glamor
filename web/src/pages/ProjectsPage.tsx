import { useProjectsStore } from '../stores/projects'

/** Заглушка списка проектов (полноценный экран — T-14). */
export function ProjectsPage() {
  const items = useProjectsStore((s) => s.items)
  const loading = useProjectsStore((s) => s.loading)
  const error = useProjectsStore((s) => s.error)

  return (
    <div>
      <h1 className="mb-1 text-xl font-semibold text-zinc-100">Проекты</h1>
      <p className="mb-4 text-sm text-zinc-500">Роут / — экран будет в T-14</p>
      {loading && <p className="text-sm text-zinc-500">загрузка…</p>}
      {error && <p className="text-sm text-red-400">ошибка загрузки: {error}</p>}
      <ul className="space-y-1">
        {items.map((project) => (
          <li key={project.id} className="text-sm text-zinc-300">
            {project.name} <span className="text-zinc-600">{project.path}</span>
          </li>
        ))}
      </ul>
    </div>
  )
}
