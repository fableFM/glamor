import {
  createRootRoute,
  createRoute,
  createRouter,
} from '@tanstack/react-router'
import { Layout } from './components/Layout'
import { ProjectsPage } from './pages/ProjectsPage'
import { ProjectPage } from './pages/ProjectPage'
import { RunPage } from './pages/RunPage'

// Роуты-заглушки T-13; конкретные экраны — T-14 (проекты), T-15 (проект), T-16 (ран).

const rootRoute = createRootRoute({ component: Layout })

const projectsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/',
  component: ProjectsPage,
})

const projectRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/projects/$id',
  component: ProjectPage,
})

const runRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/runs/$id',
  component: RunPage,
})

const routeTree = rootRoute.addChildren([projectsRoute, projectRoute, runRoute])

export const router = createRouter({ routeTree })

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}
