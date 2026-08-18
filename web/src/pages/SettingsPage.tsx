import { useEffect, useState } from 'react'
import type { Settings } from '../api/client'
import { ApiError, getSettings, putSupervisorSettings, putTelegramSettings } from '../api/client'
import { useSoundStore } from '../lib/sound'

/*
  Экран настроек демона: Telegram (токен + вкл/выкл, бэкенд валидирует
  через getMe и hot-apply'ит) и Supervisor (4 числовых параметра,
  partial PUT — пустое поле не меняет значение).
*/

const inputCls =
  'w-full rounded-md border border-zinc-700 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-200 focus:border-violet-500 focus:outline-none'

function TelegramSection({ settings, onSaved }: { settings: Settings; onSaved: (s: Settings) => void }) {
  const [token, setToken] = useState('')
  const [enabled, setEnabled] = useState(settings.telegram.enabled)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)

  const save = () => {
    if (busy) return
    setBusy(true)
    setError(null)
    setNotice(null)
    // пустой token = оставить текущий (спека)
    putTelegramSettings({ enabled, ...(token.trim() ? { token: token.trim() } : {}) })
      .then((telegram) => {
        onSaved({ ...settings, telegram })
        setToken('')
        setNotice('сохранено')
      })
      .catch((err: unknown) => {
        // 400 validation «telegram rejected the token» — токен не прошёл getMe
        setError(err instanceof ApiError ? err.message : err instanceof Error ? err.message : 'ошибка сохранения')
      })
      .finally(() => setBusy(false))
  }

  return (
    <section className="rounded-lg border border-zinc-800 bg-zinc-900/60 p-4">
      <h2 className="mb-3 text-sm font-semibold text-zinc-100">Telegram</h2>
      <div className="space-y-3">
        <label className="block">
          <span className="mb-1 block text-xs text-zinc-500">
            токен бота {settings.telegram.has_token ? `(текущий: ${settings.telegram.token_masked ?? '•••'})` : '(не задан)'}
          </span>
          <input
            type="password"
            value={token}
            onChange={(e) => setToken(e.target.value)}
            placeholder="оставьте пустым, чтобы не менять"
            autoComplete="off"
            className={`${inputCls} font-mono`}
          />
        </label>
        {settings.telegram.bot_username && (
          <p className="text-xs text-zinc-500">
            бот: <span className="font-mono text-zinc-300">@{settings.telegram.bot_username}</span>
          </p>
        )}
        <label className="flex items-center gap-2 text-sm text-zinc-300">
          <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
          уведомления включены
        </label>
        {error && <p className="text-xs text-red-400">{error}</p>}
        {notice && <p className="text-xs text-emerald-400">{notice}</p>}
        <button
          type="button"
          onClick={save}
          disabled={busy}
          className="rounded-md bg-violet-600 px-3 py-1.5 text-xs font-medium text-violet-50 hover:bg-violet-500 disabled:opacity-50"
        >
          {busy ? 'сохранение…' : 'Сохранить'}
        </button>
      </div>
    </section>
  )
}

const SUPERVISOR_FIELDS = [
  { key: 'stall_timeout_sec', label: 'stall_timeout_sec', hint: 'таймаут «зависшего» этапа, сек' },
  { key: 'stage_timeout_min', label: 'stage_timeout_min', hint: 'максимум на этап, мин' },
  { key: 'max_parallel', label: 'max_parallel', hint: 'параллельных этапов' },
  { key: 'max_auto_resumes', label: 'max_auto_resumes', hint: 'авто-резюмов подряд' },
] as const

function SupervisorSection({ settings, onSaved }: { settings: Settings; onSaved: (s: Settings) => void }) {
  // строки: пустое поле = «не менять» (partial PUT)
  const [values, setValues] = useState<Record<string, string>>(() =>
    Object.fromEntries(SUPERVISOR_FIELDS.map((f) => [f.key, String(settings.supervisor[f.key])])),
  )
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)

  const save = () => {
    if (busy) return
    const body: Record<string, number> = {}
    for (const field of SUPERVISOR_FIELDS) {
      const raw = values[field.key].trim()
      if (!raw) continue
      const num = Number(raw)
      if (!Number.isFinite(num) || num < 0) {
        setError(`${field.label}: не число`)
        return
      }
      body[field.key] = num
    }
    setBusy(true)
    setError(null)
    setNotice(null)
    putSupervisorSettings(body)
      .then((supervisor) => {
        onSaved({ ...settings, supervisor })
        setNotice('сохранено')
      })
      .catch((err: unknown) => setError(err instanceof Error ? err.message : 'ошибка сохранения'))
      .finally(() => setBusy(false))
  }

  return (
    <section className="rounded-lg border border-zinc-800 bg-zinc-900/60 p-4">
      <h2 className="mb-3 text-sm font-semibold text-zinc-100">Supervisor</h2>
      <div className="grid grid-cols-2 gap-3">
        {SUPERVISOR_FIELDS.map((field) => (
          <label key={field.key} className="block">
            <span className="mb-1 block text-xs text-zinc-500">
              <span className="font-mono">{field.label}</span> — {field.hint}
            </span>
            <input
              type="number"
              min={0}
              value={values[field.key]}
              onChange={(e) => setValues((v) => ({ ...v, [field.key]: e.target.value }))}
              className={inputCls}
            />
          </label>
        ))}
      </div>
      {error && <p className="mt-2 text-xs text-red-400">{error}</p>}
      {notice && <p className="mt-2 text-xs text-emerald-400">{notice}</p>}
      <button
        type="button"
        onClick={save}
        disabled={busy}
        className="mt-3 rounded-md bg-violet-600 px-3 py-1.5 text-xs font-medium text-violet-50 hover:bg-violet-500 disabled:opacity-50"
      >
        {busy ? 'сохранение…' : 'Сохранить'}
      </button>
    </section>
  )
}

export function SettingsPage() {
  const [settings, setSettings] = useState<Settings | null>(null)
  const [error, setError] = useState<string | null>(null)
  const muted = useSoundStore((s) => s.muted)
  const setMuted = useSoundStore((s) => s.setMuted)

  useEffect(() => {
    getSettings()
      .then(setSettings)
      .catch((err: unknown) => setError(err instanceof Error ? err.message : 'ошибка загрузки'))
  }, [])

  if (error) return <p className="text-sm text-red-400">не удалось загрузить настройки: {error}</p>
  if (!settings) return <p className="text-sm text-zinc-500">загрузка…</p>

  return (
    <div className="mx-auto max-w-2xl space-y-4">
      <h1 className="text-xl font-semibold text-zinc-100">Настройки</h1>
      <TelegramSection key={settings.telegram.bot_username ?? ''} settings={settings} onSaved={setSettings} />
      <SupervisorSection settings={settings} onSaved={setSettings} />
      <section className="rounded-lg border border-zinc-800 bg-zinc-900/60 p-4">
        <h2 className="mb-3 text-sm font-semibold text-zinc-100">Интерфейс</h2>
        <label className="flex items-center gap-2 text-sm text-zinc-300">
          <input type="checkbox" checked={!muted} onChange={(e) => setMuted(!e.target.checked)} />
          звуковые уведомления (гейты, падения, успехи этапов)
        </label>
      </section>
    </div>
  )
}
