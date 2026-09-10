/*
  Парсер операций distill v2 (T-30) — зеркало backend
  backend/internal/service/lessons/operations.go (ParseOperations + splitCards
  из lessons.go). Индексы возвращаемых операций совпадают с backend-разбором:
  на них ссылается ResolveGateRequest.lesson_ops (per-card резолв гейта
  lesson_review). Правила пропуска битых карточек повторяем 1-в-1, иначе
  индексы разъедутся и пользователь примет/отклонит не ту операцию.
*/

export type LessonOpKind = 'new' | 'refine' | 'supersede' | 'link' | 'question'

export interface LessonOperation {
  op: LessonOpKind
  title: string
  triggers: string[]
  body: string
  /** refine/supersede/link: первый id */
  targetId: string
  /** link: второй id */
  target2Id: string
  kind: string // '' | behavior | vendor
  vendor: string
  vendorVersion: string
  area: string
}

interface RawCard {
  fm: Record<string, string>
  body: string
}

/** splitCards: frontmatter-карточки, разделённые строками `---`. */
function splitCards(content: string): RawCard[] {
  const cards: RawCard[] = []
  // 0 = вне карточки, 1 = frontmatter, 2 = тело
  let state = 0
  let fmLines: string[] = []
  let bodyLines: string[] = []

  const finishCard = () => {
    const fm: Record<string, string> = {}
    for (const line of fmLines) {
      const colon = line.indexOf(':')
      if (colon < 0) continue
      fm[line.slice(0, colon).trim()] = line.slice(colon + 1).trim()
    }
    const body = bodyLines.join('\n').trim()
    if (Object.keys(fm).length > 0 || body !== '') cards.push({ fm, body })
    fmLines = []
    bodyLines = []
  }

  for (const line of content.split('\n')) {
    if (line.trim() === '---') {
      if (state === 0) state = 1
      else if (state === 1) state = 2
      else {
        finishCard()
        state = 1
      }
      continue
    }
    if (state === 1) fmLines.push(line)
    else if (state === 2) bodyLines.push(line)
  }
  // EOF: карточка завершается только из тела (зеркало backend splitCards) —
  // оборванный внутри frontmatter файл (LLM-транкейт) карточки не даёт,
  // иначе индексы операций lesson_ops разъезжаются с backend
  if (state === 2) finishCard()
  return cards
}

/** parseList: «[a, b]» → ['a','b'] (зеркало backend parseList). */
function parseList(value: string): string[] {
  return value
    .trim()
    .replace(/^\[|\]$/g, '')
    .split(',')
    .map((item) => item.trim())
    .filter((item) => item.length > 0)
}

/**
 * Разбор lessons.md в операции. Карточка без `op:` — new (обратная
 * совместимость T-29); «ВОПРОС:» в теле — question. NO_LESSONS и мусор
 * дают пустой список; неполные операции (нет target у refine и т.п.)
 * пропускаются — как на backend.
 */
export function parseLessonOperations(content: string): LessonOperation[] {
  const ops: LessonOperation[] = []
  for (const raw of splitCards(content)) {
    const op: LessonOperation = {
      op: 'new',
      title: raw.fm.title ?? '',
      triggers: parseList(raw.fm.triggers ?? ''),
      body: raw.body,
      targetId: raw.fm.target ?? '',
      target2Id: raw.fm.target2 ?? '',
      kind: (raw.fm.kind ?? '').toLowerCase(),
      vendor: raw.fm.vendor ?? '',
      vendorVersion: raw.fm.vendor_version ?? '',
      area: raw.fm.area ?? '',
    }

    const rawOp = (raw.fm.op ?? '').toLowerCase()
    if (rawOp === 'refine' || rawOp === 'supersede' || rawOp === 'link' || rawOp === 'question') {
      op.op = rawOp
    } else {
      op.op = 'new'
      // конвенция T-29: вопрос — карточка с «ВОПРОС:» в Причине
      if (raw.body.includes('ВОПРОС:')) op.op = 'question'
    }

    // валидация полноты операции (зеркало backend: битая не долетает до apply)
    switch (op.op) {
      case 'new':
      case 'question':
        if (op.title === '' || op.body === '') continue
        break
      case 'refine':
      case 'supersede':
        if (op.targetId === '' || op.title === '' || op.body === '') continue
        break
      case 'link':
        if (op.targetId === '' || op.target2Id === '' || op.targetId === op.target2Id) continue
        break
    }
    ops.push(op)
  }
  return ops
}

/**
 * Тело карточки из полного содержимого файла урока (без frontmatter) —
 * «старая» сторона diff'а для refine/supersede (источник — getLesson
 * content, файл целиком).
 */
export function lessonBody(content: string): string {
  const cards = splitCards(content)
  return cards.length > 0 ? cards[0].body : content.trim()
}

// --- простой line-diff (без зависимостей): LCS по строкам ---

export interface DiffLine {
  type: 'same' | 'add' | 'del'
  text: string
}

/** lineDiff — построчный diff oldText → newText (LCS, тексты уроков малы). */
export function lineDiff(oldText: string, newText: string): DiffLine[] {
  const a = oldText.split('\n')
  const b = newText.split('\n')
  // dp[i][j] — длина LCS суффиксов a[i:], b[j:]
  const dp: number[][] = Array.from({ length: a.length + 1 }, () => new Array<number>(b.length + 1).fill(0))
  for (let i = a.length - 1; i >= 0; i--) {
    for (let j = b.length - 1; j >= 0; j--) {
      dp[i][j] = a[i] === b[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1])
    }
  }
  const out: DiffLine[] = []
  let i = 0
  let j = 0
  while (i < a.length && j < b.length) {
    if (a[i] === b[j]) {
      out.push({ type: 'same', text: a[i] })
      i++
      j++
    } else if (dp[i + 1][j] >= dp[i][j + 1]) {
      out.push({ type: 'del', text: a[i] })
      i++
    } else {
      out.push({ type: 'add', text: b[j] })
      j++
    }
  }
  while (i < a.length) out.push({ type: 'del', text: a[i++] })
  while (j < b.length) out.push({ type: 'add', text: b[j++] })
  return out
}
