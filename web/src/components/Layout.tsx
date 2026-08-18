import { Link, Outlet } from '@tanstack/react-router'
import { Brain, FolderGit2, Settings, Sparkles, Volume2, VolumeX } from 'lucide-react'
import { ConnectionBanner } from './ConnectionBanner'
import { InboxBell } from './InboxBell'
import { useSoundStore } from '../lib/sound'

/** Mute звуковых уведомлений (lib/sound): иконка в шапке, состояние в localStorage. */
function MuteButton() {
  const muted = useSoundStore((s) => s.muted)
  const toggleMuted = useSoundStore((s) => s.toggleMuted)
  return (
    <button
      type="button"
      onClick={toggleMuted}
      title={muted ? 'включить звуковые уведомления' : 'выключить звук'}
      aria-label={muted ? 'Звук выключен' : 'Звук включён'}
      className="rounded-md p-2 text-zinc-400 hover:bg-zinc-800 hover:text-zinc-100"
    >
      {muted ? <VolumeX className="size-4" aria-hidden /> : <Volume2 className="size-4" aria-hidden />}
    </button>
  )
}

/**
 * Каркас приложения: сайдбар + шапка с инбоксом гейтов (T-15) + контент.
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
          <Link
            to="/memory"
            className="flex items-center gap-2 rounded-md px-3 py-2 text-sm text-zinc-400 hover:bg-zinc-800 hover:text-zinc-100"
            activeProps={{ className: 'bg-zinc-800 text-zinc-100' }}
          >
            <Brain className="size-4" aria-hidden />
            Память
          </Link>
          <Link
            to="/settings"
            className="flex items-center gap-2 rounded-md px-3 py-2 text-sm text-zinc-400 hover:bg-zinc-800 hover:text-zinc-100"
            activeProps={{ className: 'bg-zinc-800 text-zinc-100' }}
          >
            <Settings className="size-4" aria-hidden />
            Настройки
          </Link>
        </nav>
      </aside>
      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex items-center justify-end gap-1 border-b border-zinc-800 bg-zinc-900/60 px-4 py-1.5">
          <MuteButton />
          <InboxBell />
        </header>
        <ConnectionBanner />
        <main className="min-h-0 flex-1 overflow-auto p-6">
          <Outlet />
        </main>
      </div>
    </div>
  )
}
