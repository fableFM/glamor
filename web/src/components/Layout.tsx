import { Link, Outlet } from '@tanstack/react-router'
import { FolderGit2, Sparkles } from 'lucide-react'
import { ConnectionBanner } from './ConnectionBanner'

/**
 * Каркас приложения: сайдбар + контент. Конкретные экраны — T-14/15/16,
 * здесь только навигация по роутам-заглушкам.
 */
export function Layout() {
  return (
    <div className="flex h-full">
      <aside className="flex w-56 shrink-0 flex-col border-r border-zinc-800 bg-zinc-900">
        <div className="flex items-center gap-2 px-4 py-4 text-zinc-100">
          <Sparkles className="size-5 text-violet-400" aria-hidden />
          <span className="text-lg font-semibold tracking-tight">glamor</span>
        </div>
        <nav className="flex flex-col gap-1 px-2">
          <Link
            to="/"
            className="flex items-center gap-2 rounded-md px-3 py-2 text-sm text-zinc-400 hover:bg-zinc-800 hover:text-zinc-100"
            activeProps={{ className: 'bg-zinc-800 text-zinc-100' }}
            activeOptions={{ exact: true }}
          >
            <FolderGit2 className="size-4" aria-hidden />
            Проекты
          </Link>
        </nav>
      </aside>
      <div className="flex min-w-0 flex-1 flex-col">
        <ConnectionBanner />
        <main className="min-h-0 flex-1 overflow-auto p-6">
          <Outlet />
        </main>
      </div>
    </div>
  )
}
