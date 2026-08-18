import type { ReactNode } from 'react'
import type { Run, Stage } from '../api/client'

/*
  Бейджи состояний рана/стадии — единая палитра на всех экранах.
  running/waiting_gate подсвечены пульсом (живой процесс).
*/

const RUN_STATE_LABELS: Record<Run['state'], string> = {
  draft: 'черновик',
  running: 'running',
  waiting_gate: 'ждёт гейт',
  succeeded: 'succeeded',
  failed: 'failed',
  stopped: 'stopped',
}

const RUN_STATE_CLASSES: Record<Run['state'], string> = {
  draft: 'bg-zinc-700/40 text-zinc-300',
  running: 'bg-blue-500/15 text-blue-300',
  waiting_gate: 'bg-amber-500/15 text-amber-300',
  succeeded: 'bg-emerald-500/15 text-emerald-300',
  failed: 'bg-red-500/15 text-red-300',
  stopped: 'bg-zinc-500/15 text-zinc-400',
}

const STAGE_STATE_CLASSES: Record<Stage['state'], string> = {
  pending: 'bg-zinc-700/40 text-zinc-400',
  running: 'bg-blue-500/15 text-blue-300',
  succeeded: 'bg-emerald-500/15 text-emerald-300',
  failed: 'bg-red-500/15 text-red-300',
  interrupted: 'bg-amber-500/15 text-amber-300',
  skipped: 'bg-zinc-500/15 text-zinc-500',
}

function Pill({ className, pulse, children }: { className: string; pulse?: boolean; children: ReactNode }) {
  return (
    <span className={`inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-xs font-medium ${className}`}>
      {pulse && <span className="size-1.5 animate-pulse rounded-full bg-current" aria-hidden />}
      {children}
    </span>
  )
}

export function RunStateBadge({ state }: { state: Run['state'] }) {
  return (
    <Pill className={RUN_STATE_CLASSES[state]} pulse={state === 'running' || state === 'waiting_gate'}>
      {RUN_STATE_LABELS[state]}
    </Pill>
  )
}

export function StageStateBadge({ state }: { state: Stage['state'] }) {
  return (
    <Pill className={STAGE_STATE_CLASSES[state]} pulse={state === 'running'}>
      {state}
    </Pill>
  )
}
