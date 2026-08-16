import { getRouteApi } from '@tanstack/react-router'

const routeApi = getRouteApi('/projects/$id')

/** Заглушка экрана проекта (полноценный экран — T-15). */
export function ProjectPage() {
  const { id } = routeApi.useParams()
  return (
    <div>
      <h1 className="mb-1 text-xl font-semibold text-zinc-100">Проект #{id}</h1>
      <p className="text-sm text-zinc-500">Роут /projects/$id — экран будет в T-15</p>
    </div>
  )
}
