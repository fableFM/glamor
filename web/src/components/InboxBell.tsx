import { useEffect, useRef, useState } from 'react'
import { Link } from '@tanstack/react-router'
import { Bell } from 'lucide-react'
import { useInboxStore } from '../stores/inbox'
import { useRunsStore } from '../stores/runs'
import { useProjectsStore } from '../stores/projects'
import { formatAge, firstLine } from '../lib/format'
import type { Gate } from '../api/client'

const GATE_KIND_LABELS: Record<Gate['kind'], string> = {
  plan_approval: 'план',
  question: 'вопрос',
  escalation: 'эскалация',
  final_review: 'финал',
  lesson_review: 'урок',
}

/**
 * Глобальный инбокс гейтов (T-15): колокол в шапке с бейджем открытых
 * гейтов по всем проектам + dropdown со списком и переходами к ранам.
 * Данные — inbox-стор (WS run_id=*), проект резолвится run → project
 * через runs/projects сторы.
 */
export function InboxBell() {
  const open = useInboxStore((s) => s.open)
  const runsById = useRunsStore((s) => s.byId)
  const projects = useProjectsStore((s) => s.items)
  const [expanded, setExpanded] = useState(false)
  const rootRef = useRef<HTMLDivElement>(null)

  // закрытие dropdown по клику вне и по Escape
  useEffect(() => {
    if (!expanded) return
    const onPointerDown = (event: PointerEvent) => {
      if (rootRef.current && !rootRef.current.contains(event.target as Node)) setExpanded(false)
    }
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setExpanded(false)
    }
    document.addEventListener('pointerdown', onPointerDown)
    document.addEventListener('keydown', onKeyDown)
    return () => {
      document.removeEventListener('pointerdown', onPointerDown)
      document.removeEventListener('keydown', onKeyDown)
    }
  }, [expanded])

  const items = Object.values(open).sort((a, b) => a.openedAt.localeCompare(b.openedAt))
  const projectName = (runId: string): string => {
    const run = runsById[runId]
    const project = run ? projects.find((p) => p.id === run.project_id) : undefined
    return project?.name ?? '—'
  }

  return (
    <div ref={rootRef} className="relative">
      <button
        type="button"
        onClick={() => setExpanded((v) => !v)}
        className="relative rounded-md p-2 text-zinc-400 hover:bg-zinc-800 hover:text-zinc-100"
        aria-label="Открытые гейты"
        aria-expanded={expanded}
      >
        <Bell className="size-4" aria-hidden />
        {items.length > 0 && (
          <span className="absolute -right-0.5 -top-0.5 flex h-4 min-w-4 items-center justify-center rounded-full bg-amber-500 px-1 text-[10px] font-semibold text-zinc-950">
            {items.length}
          </span>
        )}
      </button>
      {expanded && (
        <div className="absolute right-0 top-full z-50 mt-1 w-96 rounded-lg border border-zinc-700 bg-zinc-900 shadow-xl">
          <div className="border-b border-zinc-800 px-3 py-2 text-xs font-semibold uppercase tracking-wide text-zinc-500">
            Открытые гейты ({items.length})
          </div>
          {items.length === 0 ? (
            <p className="px-3 py-4 text-sm text-zinc-500">открытых гейтов нет</p>
          ) : (
            <ul className="max-h-80 overflow-auto py-1">
              {items.map((item) => (
                <li key={item.gateId}>
                  <Link
                    to="/runs/$id"
                    params={{ id: item.runId }}
                    onClick={() => setExpanded(false)}
                    className="block px-3 py-2 hover:bg-zinc-800"
                  >
                    <div className="flex items-center gap-2 text-xs text-zinc-500">
                      <span className="font-medium text-zinc-300">{projectName(item.runId)}</span>
                      <span className="rounded bg-amber-500/15 px-1.5 py-0.5 text-amber-300">
                        {GATE_KIND_LABELS[item.kind]}
                      </span>
                      <span className="ml-auto">ждёт {formatAge(item.openedAt)}</span>
                    </div>
                    <div className="mt-0.5 truncate text-sm text-zinc-300">
                      {firstLine(item.question, 80) || '—'}
                    </div>
                  </Link>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </div>
  )
}
