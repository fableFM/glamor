import { useEffect } from 'react'
import type { ReactNode } from 'react'
import { EventClient } from '../lib/events'
import { dispatchEvents } from '../lib/dispatch'
import { listProjects } from '../api/client'
import { useProjectsStore } from '../stores/projects'
import { useConnectionStore } from '../stores/connection'

/*
  Инициализация приложения (T-13):
  - первичная загрузка проектов через REST;
  - единая WS-подписка run_id=* на весь журнал событий.
*/
export function AppProvider({ children }: { children: ReactNode }) {
  useEffect(() => {
    const projects = useProjectsStore.getState()
    projects.setLoading()
    listProjects()
      .then((items) => useProjectsStore.getState().setAll(items))
      .catch((error: unknown) => {
        // демон может быть ещё не поднят — плашка соединения покажет это отдельно
        const message = error instanceof Error ? error.message : 'неизвестная ошибка'
        useProjectsStore.getState().setError(message)
      })

    const client = new EventClient({
      runId: '*',
      dispatch: dispatchEvents,
      onStatus: (status) => useConnectionStore.getState().setStatus(status),
    })
    client.start()

    return () => client.stop()
  }, [])

  return children
}
