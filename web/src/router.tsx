import {
  createRootRoute,
  createRoute,
  createRouter,
  lazyRouteComponent,
} from '@tanstack/react-router'
import { Layout } from './components/Layout'
import { ProjectsPage } from './pages/ProjectsPage'
import { ProjectPage } from './pages/ProjectPage'
import { MemoryPage } from './pages/MemoryPage'
import { SettingsPage } from './pages/SettingsPage'

// Роуты-заглушки T-13; экраны — T-14/15/16, редактор пайплайнов — T-20.

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
  // лениво: react-virtuoso/streamdown тяжёлые, первый экран (списки) не должен их тянуть
  component: lazyRouteComponent(() => import('./pages/RunPage'), 'RunPage'),
})

const pipelineEditorRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/pipelines/$id/edit',
  // лениво: xyflow тяжёлый, основной бандл (экраны ранов) не должен его тянуть
  component: lazyRouteComponent(() => import('./pages/PipelineEditorPage'), 'PipelineEditorPage'),
})

const memoryRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/memory',
  component: MemoryPage,
})

const settingsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/settings',
  component: SettingsPage,
})

const routeTree = rootRoute.addChildren([
  projectsRoute,
  projectRoute,
  runRoute,
  pipelineEditorRoute,
  memoryRoute,
  settingsRoute,
])

export const router = createRouter({ routeTree })

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}
