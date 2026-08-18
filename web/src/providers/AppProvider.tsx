import { useEffect } from 'react'
import type { ReactNode } from 'react'
import { EventClient } from '../lib/events'
import { dispatchEvents } from '../lib/dispatch'
import { listProjects, listRuns } from '../api/client'
import { useProjectsStore } from '../stores/projects'
import { useRunsStore } from '../stores/runs'
import { useConnectionStore } from '../stores/connection'

/*
  Инициализация приложения (T-13/T-16):
  - первичная загрузка проектов и ранов через REST (раны нужны списку
    проектов и инбоксу для маппинга run → project);
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

    // раны — мягкая загрузка: ошибка не блокирует UI, списки подтянутся на своих экранах
    listRuns({ limit: 200 })
      .then((items) => useRunsStore.getState().upsertMany(items))
      .catch(() => undefined)

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
