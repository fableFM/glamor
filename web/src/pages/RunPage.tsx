import { getRouteApi } from '@tanstack/react-router'

const routeApi = getRouteApi('/runs/$id')

/** Заглушка экрана рана (полноценный экран — T-16). */
export function RunPage() {
  const { id } = routeApi.useParams()
  return (
    <div>
      <h1 className="mb-1 text-xl font-semibold text-zinc-100">Ран {id}</h1>
      <p className="text-sm text-zinc-500">Роут /runs/$id — экран будет в T-16</p>
    </div>
  )
}
