import { create } from 'zustand'
import type { Project } from '../api/client'

interface ProjectsState {
  items: Project[]
  loading: boolean
  /** сообщение ошибки первичной загрузки (демон недоступен и т.п.) */
  error: string | null
  setLoading: () => void
  setAll: (projects: Project[]) => void
  setError: (message: string) => void
  remove: (id: number) => void
  upsert: (project: Project) => void
}

export const useProjectsStore = create<ProjectsState>((set) => ({
  items: [],
  loading: false,
  error: null,
  setLoading: () => set({ loading: true, error: null }),
  setAll: (projects) => set({ items: projects, loading: false, error: null }),
  setError: (message) => set({ error: message, loading: false }),
  remove: (id) => set((state) => ({ items: state.items.filter((p) => p.id !== id) })),
  upsert: (project) =>
    set((state) => {
      const index = state.items.findIndex((p) => p.id === project.id)
      if (index === -1) return { items: [...state.items, project] }
      const items = state.items.slice()
      items[index] = project
      return { items }
    }),
}))
