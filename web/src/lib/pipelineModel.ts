/*
  Модель spec_json пайплайна (M1, T-20/T-21): линейная цепочка этапов
  (порядок массива = порядок выполнения), одна loop-петля, один final_gate.
  Разбор защитный: неизвестные/лишние поля игнорируем, отсутствующие —
  дефолтим. Сериализация — обратно в spec_json для POST /pipelines и
  POST /pipelines/{id}/versions.
*/

export type StageKind = 'llm-stage' | 'janitor' | 'human-gate'
export type Harness = 'kimi' | 'qwen'
export type Effort = 'low' | 'high' | 'max'
export type GateAfter = '' | 'plan_approval' | 'question' | 'escalation' | 'final_review'
/** политика падения janitor-команды (T-22) */
export type JanitorOnFail = 'fail_stage' | 'warn'
/** политика падения члена параллельной группы (T-28, ADR-003) */
export type GroupOnFailure = 'fail_fast' | 'wait_all'

export interface SpecStage {
  key: string
  kind: StageKind
  harness: Harness
  model: string
  effort: Effort
  prompt_template: string
  artifact: { path: string; required: boolean }
  questions_path: string
  gate_after: GateAfter
  // janitor (T-22): команды по одной на строку, политика падения, таймаут
  commands: string[]
  on_fail: JanitorOnFail
  command_timeout_sec: number
  // параллельные ветки (T-28): членство в группе + обязательный read_only
  parallel_group: string
  read_only: boolean
}

export interface ParallelGroupSpec {
  name: string
  on_failure: GroupOnFailure
}

export interface SpecLoop {
  from: string
  to: string
  max_iters: number
}

export interface PipelineSpec {
  stages: SpecStage[]
  loop: SpecLoop | null
  final_gate: boolean
  /** опционально: без групп ключ в JSON не сериализуется (round-trip стабилен) */
  parallel_groups?: ParallelGroupSpec[]
}

export const HARNESSES: Harness[] = ['kimi', 'qwen']
export const EFFORTS: Effort[] = ['low', 'high', 'max']
export const JANITOR_ON_FAIL: JanitorOnFail[] = ['fail_stage', 'warn']
export const GROUP_ON_FAILURE: GroupOnFailure[] = ['fail_fast', 'wait_all']
export const GATE_AFTER_OPTIONS: { value: GateAfter; label: string }[] = [
  { value: '', label: '— без гейта —' },
  { value: 'plan_approval', label: 'plan_approval' },
  { value: 'question', label: 'question' },
  { value: 'escalation', label: 'escalation' },
  { value: 'final_review', label: 'final_review' },
]

/*
  Известные плейсхолдеры промпт-шаблонов (движок T-17).
  artifact.<file> — семейство: любой суффикс после artifact. валиден.
*/
export const KNOWN_PLACEHOLDERS = [
  'task',
  'base_branch',
  'branch',
  'run_dir',
  'depth',
  'depth_instructions',
  'queue_notes',
  'vendor_memory_paths',
  'verdict',
  'iteration',
  'max_iterations',
] as const

/*
  Regex плейсхолдеров — зеркало backend (F-07/F-08, validate.go placeholderRe):
  ловит ЛЮБОЙ {{...}}, включая верхний регистр, пробелы и мусор; имя должно
  точно совпасть с известным ({{ task }} / {{Task}} — ошибка, как на backend).
*/
const PLACEHOLDER_RE = /\{\{([^{}]*)\}\}/g

function isKnownPlaceholder(name: string): boolean {
  if (name.startsWith('artifact.')) return name.length > 'artifact.'.length
  return (KNOWN_PLACEHOLDERS as readonly string[]).includes(name)
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null
}

function str(value: unknown, fallback = ''): string {
  return typeof value === 'string' ? value : fallback
}

function oneOf<T extends string>(value: unknown, options: readonly T[], fallback: T): T {
  return typeof value === 'string' && (options as readonly string[]).includes(value) ? (value as T) : fallback
}

export function emptyStage(index: number): SpecStage {
  return {
    key: `stage-${index}`,
    kind: 'llm-stage',
    harness: 'kimi',
    model: '',
    effort: 'high',
    prompt_template: '',
    artifact: { path: '', required: false },
    questions_path: '',
    gate_after: '',
    commands: [],
    on_fail: 'fail_stage',
    command_timeout_sec: 60,
    parallel_group: '',
    read_only: false,
  }
}

/** janitor-нода из палитры (T-22): llm-поля игнорируются движком, команды пусты. */
export function emptyJanitor(index: number): SpecStage {
  return { ...emptyStage(index), key: `janitor-${index}`, kind: 'janitor' }
}

/** Разбор spec_json; битый JSON/неизвестный формат → null (UI покажет ошибку). */
export function parseSpec(specJson: string): PipelineSpec | null {
  let parsed: unknown
  try {
    parsed = JSON.parse(specJson)
  } catch {
    return null
  }
  if (!isRecord(parsed) || !Array.isArray(parsed.stages)) return null

  const stages: SpecStage[] = []
  for (const raw of parsed.stages) {
    if (!isRecord(raw)) return null
    const artifact = isRecord(raw.artifact) ? raw.artifact : {}
    stages.push({
      key: str(raw.key),
      kind: oneOf(raw.kind, ['llm-stage', 'janitor', 'human-gate'] as const, 'llm-stage'),
      harness: oneOf(raw.harness, HARNESSES, 'kimi'),
      model: str(raw.model),
      effort: oneOf(raw.effort, EFFORTS, 'high'),
      prompt_template: str(raw.prompt_template),
      artifact: { path: str(artifact.path), required: artifact.required === true },
      questions_path: str(raw.questions_path),
      gate_after: oneOf(raw.gate_after, GATE_AFTER_OPTIONS.map((o) => o.value), ''),
      commands: Array.isArray(raw.commands) ? raw.commands.filter((c): c is string => typeof c === 'string') : [],
      on_fail: oneOf(raw.on_fail, JANITOR_ON_FAIL, 'fail_stage'),
      command_timeout_sec:
        typeof raw.command_timeout_sec === 'number' && raw.command_timeout_sec > 0 ? raw.command_timeout_sec : 60,
      parallel_group: str(raw.parallel_group),
      read_only: raw.read_only === true,
    })
  }

  let loop: SpecLoop | null = null
  if (isRecord(parsed.loop)) {
    loop = {
      from: str(parsed.loop.from),
      to: str(parsed.loop.to),
      max_iters: typeof parsed.loop.max_iters === 'number' ? parsed.loop.max_iters : 1,
    }
  }

  const result: PipelineSpec = { stages, loop, final_gate: parsed.final_gate != null && parsed.final_gate !== false }
  // ключ parallel_groups появляется в модели, только если есть в JSON (round-trip)
  if (Array.isArray(parsed.parallel_groups)) {
    result.parallel_groups = parsed.parallel_groups.filter(isRecord).map((g) => ({
      name: str(g.name),
      on_failure: oneOf(g.on_failure, GROUP_ON_FAILURE, 'fail_fast'),
    }))
  }
  return result
}

/** Сериализация обратно в spec_json (стабильный порядок ключей — для diff'ов). */
export function serializeSpec(spec: PipelineSpec): string {
  const out: Record<string, unknown> = {
    stages: spec.stages.map((s) => ({
      key: s.key,
      kind: s.kind,
      harness: s.harness,
      model: s.model,
      effort: s.effort,
      prompt_template: s.prompt_template,
      artifact: { path: s.artifact.path, required: s.artifact.required },
      ...(s.questions_path ? { questions_path: s.questions_path } : {}),
      ...(s.gate_after ? { gate_after: s.gate_after } : {}),
      // janitor-поля — только у janitor-нод (T-22)
      ...(s.kind === 'janitor'
        ? { commands: s.commands, on_fail: s.on_fail, command_timeout_sec: s.command_timeout_sec }
        : {}),
      // параллельные группы (T-28) — только когда заданы
      ...(s.parallel_group ? { parallel_group: s.parallel_group } : {}),
      ...(s.read_only ? { read_only: s.read_only } : {}),
    })),
  }
  if (spec.loop) out.loop = { from: spec.loop.from, to: spec.loop.to, max_iters: spec.loop.max_iters }
  if (spec.final_gate) out.final_gate = 'final_review'
  if (spec.parallel_groups && spec.parallel_groups.length > 0) {
    out.parallel_groups = spec.parallel_groups.map((g) => ({ name: g.name, on_failure: g.on_failure }))
  }
  return JSON.stringify(out, null, 2)
}

export interface SpecError {
  /** ключ стадии, если ошибка привязана к ноде (для подсветки на канве) */
  stageKey?: string
  message: string
  /** warning не блокирует сохранение (поле отсутствует у блокирующих ошибок) */
  level?: 'warning'
}

/** Блокирующая валидация перед сохранением (T-20). */
export function validateSpec(spec: PipelineSpec): SpecError[] {
  const errors: SpecError[] = []
  const keys = new Set<string>()

  if (spec.stages.length === 0) errors.push({ message: 'в пайплайне нет ни одного этапа' })

  for (const stage of spec.stages) {
    const key = stage.key.trim()
    if (!key) {
      errors.push({ stageKey: stage.key, message: 'пустой key этапа' })
    } else if (keys.has(key)) {
      errors.push({ stageKey: key, message: `дубликат key «${key}»` })
    }
    keys.add(key)

    // обязательные поля llm-ноды (F-08, ТЗ T-20 «обязательные поля нод заполнены»);
    // с 2026-08-17 backend ValidateSpec проверяет то же самое (fix-task-3 п.5) —
    // правила выровнены, фронт-валидация дублирует их, чтобы ошибка всплыла
    // в редакторе, а не в ране
    if (stage.kind === 'llm-stage') {
      if (!stage.harness.trim()) {
        errors.push({ stageKey: stage.key, message: `«${stage.key}»: пустой harness` })
      }
      if (!stage.model.trim()) {
        errors.push({ stageKey: stage.key, message: `«${stage.key}»: пустой model` })
      }
      if (!stage.prompt_template.trim()) {
        errors.push({ stageKey: stage.key, message: `«${stage.key}»: пустой prompt_template` })
      }
    }

    if (stage.artifact.required && !stage.artifact.path.trim()) {
      errors.push({ stageKey: stage.key, message: `«${stage.key}»: required-артефакт с пустым path` })
    }

    // janitor (T-22): хотя бы одна непустая команда
    if (stage.kind === 'janitor' && !stage.commands.some((c) => c.trim().length > 0)) {
      errors.push({ stageKey: stage.key, message: `«${stage.key}»: janitor без команд` })
    }

    // плейсхолдеры — только известные (точное совпадение, как на backend)
    for (const match of stage.prompt_template.matchAll(PLACEHOLDER_RE)) {
      if (!isKnownPlaceholder(match[1])) {
        errors.push({ stageKey: stage.key, message: `«${stage.key}»: неизвестный плейсхолдер {{${match[1]}}}` })
      }
    }
  }

  // параллельные группы (T-28, ADR-003 — зеркало backend validate.go)
  const declaredGroups = new Set((spec.parallel_groups ?? []).map((g) => g.name))
  for (const stage of spec.stages) {
    if (!stage.parallel_group) continue
    if (!declaredGroups.has(stage.parallel_group)) {
      errors.push({ stageKey: stage.key, message: `«${stage.key}»: необъявленная parallel_group «${stage.parallel_group}»` })
    }
    if (!stage.read_only) {
      errors.push({ stageKey: stage.key, message: `«${stage.key}»: этап в параллельной группе обязан быть read_only` })
    }
  }
  // группа из одного этапа — бессмысленная параллель; warning, не блокирует
  for (const group of spec.parallel_groups ?? []) {
    const members = spec.stages.filter((s) => s.parallel_group === group.name).length
    if (members < 2) {
      errors.push({ level: 'warning', message: `группа «${group.name}»: меньше двух этапов — параллелить нечего` })
    }
  }

  if (spec.loop) {
    if (!keys.has(spec.loop.from)) errors.push({ message: `loop.from: этап «${spec.loop.from}» не существует` })
    if (!keys.has(spec.loop.to)) errors.push({ message: `loop.to: этап «${spec.loop.to}» не существует` })
    if (spec.loop.max_iters < 1) errors.push({ message: 'loop.max_iters должен быть >= 1' })
  }
  return errors
}
