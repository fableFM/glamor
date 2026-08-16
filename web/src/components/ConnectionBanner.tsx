import { useConnectionStore } from '../stores/connection'
import { RefreshCw } from 'lucide-react'

/** Глобальная плашка: демон недоступен, идёт переподключение (T-13). */
export function ConnectionBanner() {
  const status = useConnectionStore((s) => s.status)
  if (status === 'online') return null

  return (
    <div className="flex items-center gap-2 bg-amber-500/10 px-4 py-2 text-sm text-amber-300">
      <RefreshCw className="size-4 animate-spin" aria-hidden />
      {status === 'reconnecting'
        ? 'демон недоступен, переподключение…'
        : 'демон недоступен'}
    </div>
  )
}
