/*
  Разбор текста гейта question (содержимое questions.md) на карточки
  вопросов (гейт-вью в панели этапа). Формат — нумерованный список
  («1. », «2) », допускается markdown-заголовок «### 1. »).
  Фолбэк: разбор не удался → одна карточка со всем текстом (не ломаемся).
*/

export interface GateQuestion {
  /** заголовок карточки (первая строка пункта, без номера) */
  title: string
  /** полный текст пункта (markdown) */
  body: string
  /** ответ по умолчанию, выдранный из текста; пусто, если не найден */
  defaultAnswer: string
}

const ITEM_START_RE = /^(?:#{1,4}\s*)?(\d+)[.)]\s+/
// «Ответ по умолчанию: …» / «по умолчанию — …» (обратные кавычки/кавычки срезаем)
const DEFAULT_RES = [
  /ответ\s+по\s+умолчанию\s*[::—–-]\s*`?"?(.+?)"?`?\s*$/im,
  /по\s+умолчанию\s*[::—–-]\s*`?"?(.+?)"?`?\s*$/im,
]

/** ответ по умолчанию из текста вопроса; '' — если не нашли */
export function extractDefaultAnswer(body: string): string {
  for (const re of DEFAULT_RES) {
    const match = re.exec(body)
    if (match && match[1].trim().length > 0) return match[1].trim()
  }
  return ''
}

function titleOf(body: string): string {
  const first = body.split('\n').find((l) => l.trim().length > 0) ?? ''
  // срезаем номер/заголовок-маркеры, обрезаем до разумной длины
  const cleaned = first.replace(/^(?:#{1,4}\s*)?\d+[.)]\s+/, '').replace(/^#+\s*/, '').trim()
  return cleaned.length > 90 ? `${cleaned.slice(0, 90)}…` : cleaned
}

export function parseGateQuestions(text: string): GateQuestion[] {
  const trimmed = text.trim()
  if (!trimmed) return []

  const lines = trimmed.split('\n')
  const starts: number[] = []
  lines.forEach((line, index) => {
    if (ITEM_START_RE.test(line)) starts.push(index)
  })

  // фолбэк: нумерованного списка нет — одна карточка со всем текстом
  if (starts.length === 0) {
    return [{ title: titleOf(trimmed), body: trimmed, defaultAnswer: extractDefaultAnswer(trimmed) }]
  }

  const questions: GateQuestion[] = []
  for (let k = 0; k < starts.length; k++) {
    const from = starts[k]
    const to = k + 1 < starts.length ? starts[k + 1] : lines.length
    // преамбулу (текст до «1.») приклеиваем к первой карточке — там контекст
    const body = (k === 0 ? lines.slice(0, to) : lines.slice(from, to)).join('\n').trim()
    questions.push({ title: titleOf(lines[from]), body, defaultAnswer: extractDefaultAnswer(body) })
  }
  return questions
}

// --- черновики ответов (localStorage): переживают перезагрузку ---

const draftKey = (gateId: string, index: number) => `glamor.gate-draft.${gateId}.${index}`

export function loadGateDraft(gateId: string, index: number): string | null {
  try {
    return globalThis.localStorage?.getItem(draftKey(gateId, index)) ?? null
  } catch {
    return null
  }
}

export function saveGateDraft(gateId: string, index: number, text: string): void {
  try {
    globalThis.localStorage?.setItem(draftKey(gateId, index), text)
  } catch {
    // localStorage недоступен — черновик живёт только в памяти компонента
  }
}

/** очистка всех черновиков гейта после отправки ответа */
export function clearGateDrafts(gateId: string, count: number): void {
  try {
    for (let i = 0; i < count; i++) globalThis.localStorage?.removeItem(draftKey(gateId, i))
  } catch {
    // см. выше
  }
}
