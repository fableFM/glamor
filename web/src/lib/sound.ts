import { create } from 'zustand'

/*
  Звуковые уведомления UI: синтез через Web Audio (OscillatorNode),
  без ассетов. Разные события — разные звуки (маппинг в dispatch.ts):
    success — этап succeeded: восходящее двухтональное «динь»;
    error   — этап failed / stream.error / run.branch_mismatch: низкий бузз;
    gate    — gate.opened (нужен ответ пользователя): двойной колокольчик;
    warning — stage.interrupted/resumed: мягкий средний сигнал.

  Автоплей-политика: AudioContext создаётся/возобновляется после первого
  жеста пользователя (one-shot listener). Mute — в localStorage,
  реактивность кнопки/чекбокса — через zustand-стор.
*/

const MUTED_STORAGE_KEY = 'glamor.muted'

function readMuted(): boolean {
  try {
    return globalThis.localStorage?.getItem(MUTED_STORAGE_KEY) === '1'
  } catch {
    return false
  }
}

interface SoundState {
  muted: boolean
  toggleMuted: () => void
  setMuted: (muted: boolean) => void
}

export const useSoundStore = create<SoundState>((set) => ({
  muted: readMuted(),
  toggleMuted: () =>
    set((state) => {
      const muted = !state.muted
      try {
        globalThis.localStorage?.setItem(MUTED_STORAGE_KEY, muted ? '1' : '0')
      } catch {
        // localStorage недоступен — живём с in-memory флагом
      }
      return { muted }
    }),
  setMuted: (muted) => {
    try {
      globalThis.localStorage?.setItem(MUTED_STORAGE_KEY, muted ? '1' : '0')
    } catch {
      // см. выше
    }
    set({ muted })
  },
}))

export type SoundKind = 'success' | 'error' | 'gate' | 'warning'

let audioContext: AudioContext | null = null
/** жест пользователя был → контекст разрешено создавать */
let unlocked = false

function ensureContext(): AudioContext | null {
  if (typeof AudioContext === 'undefined') return null // тесты/node, старые браузеры
  if (!unlocked) return null
  if (!audioContext) audioContext = new AudioContext()
  if (audioContext.state === 'suspended') void audioContext.resume()
  return audioContext
}

// разблокировка после первого жеста (autoplay policy) — ровно один раз
if (typeof document !== 'undefined') {
  const unlock = () => {
    unlocked = true
    ensureContext()
  }
  document.addEventListener('pointerdown', unlock, { once: true })
  document.addEventListener('keydown', unlock, { once: true })
}

/** один тон с экспоненциальным затуханием */
function tone(
  ctx: AudioContext,
  freq: number,
  startAt: number,
  durationSec: number,
  type: OscillatorType,
  peakGain: number,
): void {
  const osc = ctx.createOscillator()
  const gain = ctx.createGain()
  osc.type = type
  osc.frequency.value = freq
  const t = ctx.currentTime + startAt
  gain.gain.setValueAtTime(0.0001, t)
  gain.gain.exponentialRampToValueAtTime(peakGain, t + 0.015)
  gain.gain.exponentialRampToValueAtTime(0.0001, t + durationSec)
  osc.connect(gain).connect(ctx.destination)
  osc.start(t)
  osc.stop(t + durationSec + 0.05)
}

/** Точка входа из dispatch: тихая, безопасная (mute/locked/no-AudioContext — no-op). */
export function playSound(kind: SoundKind): void {
  if (useSoundStore.getState().muted) return
  const ctx = ensureContext()
  if (!ctx) return

  switch (kind) {
    case 'success':
      // «динь» вверх: 660 → 880 Hz
      tone(ctx, 660, 0, 0.08, 'sine', 0.12)
      tone(ctx, 880, 0.08, 0.12, 'sine', 0.12)
      break
    case 'error':
      // низкий бузз
      tone(ctx, 180, 0, 0.3, 'sawtooth', 0.08)
      break
    case 'gate':
      // колокольчик, два удара
      tone(ctx, 1200, 0, 0.09, 'sine', 0.14)
      tone(ctx, 1200, 0.18, 0.12, 'sine', 0.12)
      break
    case 'warning':
      tone(ctx, 440, 0, 0.2, 'sine', 0.1)
      break
  }
}
