/*
  Общие форматтеры UI (T-14/15/16). Все функции чистые, без стора.
*/

/** "3 мин 12 с", "1 ч 05 мин" — человекочитаемая длительность. */
export function formatDuration(ms: number): string {
  if (ms < 0) ms = 0
  const totalSeconds = Math.floor(ms / 1000)
  const hours = Math.floor(totalSeconds / 3600)
  const minutes = Math.floor((totalSeconds % 3600) / 60)
  const seconds = totalSeconds % 60
  if (hours > 0) return `${hours} ч ${String(minutes).padStart(2, '0')} мин`
  if (minutes > 0) return `${minutes} мин ${String(seconds).padStart(2, '0')} с`
  return `${seconds} с`
}

/** Длительность между двумя ISO-датами; конец null → «по сейчас». */
export function durationBetween(startIso: string | null | undefined, endIso: string | null | undefined): string | null {
  if (!startIso) return null
  const start = Date.parse(startIso)
  if (Number.isNaN(start)) return null
  const end = endIso ? Date.parse(endIso) : Date.now()
  return formatDuration(end - start)
}

/** "5 мин", "2 ч", "3 д" — возраст от ISO-даты до сейчас. */
export function formatAge(iso: string): string {
  const from = Date.parse(iso)
  if (Number.isNaN(from)) return ''
  const minutes = Math.max(0, Math.floor((Date.now() - from) / 60_000))
  if (minutes < 1) return 'только что'
  if (minutes < 60) return `${minutes} мин`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours} ч`
  return `${Math.floor(hours / 24)} д`
}

/** "16.08 20:15" — короткая метка времени. */
export function formatDateTime(iso: string | null | undefined): string {
  if (!iso) return '—'
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return '—'
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${pad(date.getDate())}.${pad(date.getMonth() + 1)} ${pad(date.getHours())}:${pad(date.getMinutes())}`
}

/** 1234567 → "1.2M", 1234 → "1.2k". */
export function formatTokens(count: number): string {
  if (count >= 1_000_000) return `${(count / 1_000_000).toFixed(1)}M`
  if (count >= 1000) return `${(count / 1000).toFixed(1)}k`
  return String(count)
}

/** Первая непустая строка текста задачи (для заголовков/списков). */
export function firstLine(text: string, maxLength = 120): string {
  const line = text.split('\n').find((l) => l.trim().length > 0)?.trim() ?? ''
  return line.length > maxLength ? `${line.slice(0, maxLength)}…` : line
}

/**
  Автоимя ветки glamor/<slug> (D-31): латиница/цифры из первых слов задачи.
  Кириллица в slug не идёт — если латиницы нет, сервер всё равно сгенерирует
  своё имя, поэтому preview здесь чисто информативный.
*/
export function slugify(text: string, maxLength = 40): string {
  const slug = text
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, maxLength)
    .replace(/-+$/g, '')
  return slug
}
