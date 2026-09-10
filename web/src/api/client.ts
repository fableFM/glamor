import type { components, operations } from './schema'

/*
  Тонкий typed-клиент поверх сгенерированных типов openapi-typescript
  (src/api/schema.d.ts регенерится `npm run gen:api`, не править руками).
  any в этом слое запрещён: всё неизвестное — unknown + type guards.
*/

export type Project = components['schemas']['Project']
export type ProjectDetail = components['schemas']['ProjectDetail']
export type Pipeline = components['schemas']['Pipeline']
export type PipelineDetail = components['schemas']['PipelineDetail']
export type Run = components['schemas']['Run']
export type RunDetail = components['schemas']['RunDetail']
export type Stage = components['schemas']['Stage']
export type Gate = components['schemas']['Gate']
export type Artifact = components['schemas']['Artifact']
export type Note = components['schemas']['Note']
export type Event = components['schemas']['Event']
export type EventKind = components['schemas']['EventKind']
export type SyncedMessage = components['schemas']['SyncedMessage']
export type VersionInfo = components['schemas']['VersionInfo']
export type CreateProjectRequest = components['schemas']['CreateProjectRequest']
export type PatchProjectRequest = components['schemas']['PatchProjectRequest']
export type CreateRunRequest = components['schemas']['CreateRunRequest']
export type CreateNoteRequest = components['schemas']['CreateNoteRequest']
export type ResolveGateRequest = components['schemas']['ResolveGateRequest']
export type ResolveGateResponse = components['schemas']['ResolveGateResponse']
export type InterruptStageRequest = components['schemas']['InterruptStageRequest']

type ErrorBody = components['schemas']['Error']

/** Ответ 200 операции из сгенерированной спеки. */
type OkResponse<Op extends keyof operations> = operations[Op] extends {
  responses: { 200: { content: { 'application/json': infer Body } } }
}
  ? Body
  : never

/** Ответ 201 (createProject/createRun/createNote). */
type CreatedResponse<Op extends keyof operations> = operations[Op] extends {
  responses: { 201: { content: { 'application/json': infer Body } } }
}
  ? Body
  : never

const DEFAULT_BASE_URL = 'http://127.0.0.1:7380'
const TOKEN_STORAGE_KEY = 'glamor.token'

export function apiBaseUrl(): string {
  const fromEnv = import.meta.env.VITE_GLAMOR_API
  return (fromEnv && fromEnv.length > 0 ? fromEnv : DEFAULT_BASE_URL).replace(/\/$/, '')
}

/** Токен: localStorage (дев-режим/Tauri override) или env при сборке. */
export function apiToken(): string | undefined {
  try {
    const stored = globalThis.localStorage?.getItem(TOKEN_STORAGE_KEY)
    if (stored) return stored
  } catch {
    // localStorage может быть недоступен (SSR/тесты) — токен берём из env.
  }
  const fromEnv = import.meta.env.VITE_GLAMOR_TOKEN
  return fromEnv && fromEnv.length > 0 ? fromEnv : undefined
}

/** Ошибка API в едином формате бека {code, message, details}. */
export class ApiError extends Error {
  readonly status: number
  readonly code: string
  readonly details?: Record<string, unknown>

  constructor(status: number, code: string, message: string, details?: Record<string, unknown>) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.details = details
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null
}

function isErrorBody(value: unknown): value is ErrorBody {
  return (
    isRecord(value) && typeof value.code === 'string' && typeof value.message === 'string'
  )
}

function isDetails(value: unknown): value is Record<string, unknown> {
  return isRecord(value)
}

export type QueryParams = Record<string, string | number | boolean | undefined>

interface RequestOptions {
  method?: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE'
  query?: QueryParams
  body?: unknown
  idempotencyKey?: string
  signal?: AbortSignal
}

async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const url = new URL(apiBaseUrl() + path)
  if (options.query) {
    for (const [key, value] of Object.entries(options.query)) {
      if (value !== undefined) url.searchParams.set(key, String(value))
    }
  }

  const headers = new Headers()
  const token = apiToken()
  if (token) headers.set('Authorization', `Bearer ${token}`)
  if (options.idempotencyKey) headers.set('Idempotency-Key', options.idempotencyKey)
  let body: string | undefined
  if (options.body !== undefined) {
    headers.set('Content-Type', 'application/json')
    body = JSON.stringify(options.body)
  }

  let response: Response
  try {
    response = await fetch(url, {
      method: options.method ?? 'GET',
      headers,
      body,
      signal: options.signal,
    })
  } catch (cause) {
    // Сетевой обрыв/CORS: демон недоступен — отдельный код, чтобы UI мог отреагировать.
    throw new ApiError(0, 'daemon_unreachable', 'демон недоступен', {
      cause: cause instanceof Error ? cause.message : String(cause),
    })
  }

  const text = await response.text()
  let parsed: unknown
  try {
    parsed = text.length > 0 ? JSON.parse(text) : undefined
  } catch {
    parsed = undefined
  }

  if (!response.ok) {
    if (isErrorBody(parsed)) {
      throw new ApiError(
        response.status,
        parsed.code,
        parsed.message,
        isDetails(parsed.details) ? parsed.details : undefined,
      )
    }
    throw new ApiError(response.status, 'internal', `HTTP ${response.status}`)
  }

  return parsed as T
}

// --- эндпоинты (типы берутся из operations сгенерированной спеки) ---

export function healthz(signal?: AbortSignal): Promise<string> {
  return request<string>('/healthz', { signal })
}

export function version(): Promise<OkResponse<'version'>> {
  return request<OkResponse<'version'>>('/version')
}

export function listProjects(): Promise<OkResponse<'listProjects'>> {
  return request<OkResponse<'listProjects'>>('/projects')
}

export function createProject(body: CreateProjectRequest): Promise<CreatedResponse<'createProject'>> {
  return request<CreatedResponse<'createProject'>>('/projects', { method: 'POST', body })
}

export function getProject(id: number): Promise<OkResponse<'getProject'>> {
  return request<OkResponse<'getProject'>>(`/projects/${id}`)
}

export function patchProject(id: number, body: PatchProjectRequest): Promise<OkResponse<'patchProject'>> {
  return request<OkResponse<'patchProject'>>(`/projects/${id}`, { method: 'PATCH', body })
}

export function listProjectPipelines(id: number): Promise<OkResponse<'listProjectPipelines'>> {
  return request<OkResponse<'listProjectPipelines'>>(`/projects/${id}/pipelines`)
}

export type CreatePipelineRequest = components['schemas']['CreatePipelineRequest']
export type CreatePipelineVersionRequest = components['schemas']['CreatePipelineVersionRequest']
export type ImportPipelineRequest = components['schemas']['ImportPipelineRequest']

export function getPipeline(id: number): Promise<OkResponse<'getPipeline'>> {
  return request<OkResponse<'getPipeline'>>(`/pipelines/${id}`)
}

export function createPipeline(body: CreatePipelineRequest): Promise<CreatedResponse<'createPipeline'>> {
  return request<CreatedResponse<'createPipeline'>>('/pipelines', { method: 'POST', body })
}

export function importPipeline(body: ImportPipelineRequest): Promise<CreatedResponse<'importPipeline'>> {
  return request<CreatedResponse<'importPipeline'>>('/pipelines/import', { method: 'POST', body })
}

export function listPipelineVersions(id: number): Promise<OkResponse<'listPipelineVersions'>> {
  return request<OkResponse<'listPipelineVersions'>>(`/pipelines/${id}/versions`)
}

export function createPipelineVersion(
  id: number,
  body: CreatePipelineVersionRequest,
): Promise<CreatedResponse<'createPipelineVersion'>> {
  return request<CreatedResponse<'createPipelineVersion'>>(`/pipelines/${id}/versions`, {
    method: 'POST',
    body,
  })
}

export function getPipelineVersion(vid: number): Promise<OkResponse<'getPipelineVersion'>> {
  return request<OkResponse<'getPipelineVersion'>>(`/pipeline-versions/${vid}`)
}

/** Текстовый GET (text/yaml, text/csv): тело не парсим, ошибки — в едином JSON-формате. */
async function requestText(path: string): Promise<string> {
  const url = new URL(apiBaseUrl() + path)
  const headers = new Headers()
  const token = apiToken()
  if (token) headers.set('Authorization', `Bearer ${token}`)

  let response: Response
  try {
    response = await fetch(url, { headers })
  } catch (cause) {
    throw new ApiError(0, 'daemon_unreachable', 'демон недоступен', {
      cause: cause instanceof Error ? cause.message : String(cause),
    })
  }
  const text = await response.text()
  if (!response.ok) {
    try {
      const parsed: unknown = JSON.parse(text)
      if (isErrorBody(parsed)) {
        throw new ApiError(response.status, parsed.code, parsed.message, isDetails(parsed.details) ? parsed.details : undefined)
      }
    } catch (error) {
      if (error instanceof ApiError) throw error
    }
    throw new ApiError(response.status, 'internal', `HTTP ${response.status}`)
  }
  return text
}

/** Экспорт версии пайплайна — text/yaml. */
export function exportPipelineVersion(vid: number): Promise<string> {
  return requestText(`/pipeline-versions/${vid}/export`)
}

type ListRunsQuery = NonNullable<operations['listRuns']['parameters']['query']>

export function listRuns(filter: ListRunsQuery = {}): Promise<OkResponse<'listRuns'>> {
  return request<OkResponse<'listRuns'>>('/runs', { query: { ...filter } })
}

export function createRun(body: CreateRunRequest, idempotencyKey?: string): Promise<CreatedResponse<'createRun'>> {
  return request<CreatedResponse<'createRun'>>('/runs', { method: 'POST', body, idempotencyKey })
}

export function getRun(id: string): Promise<OkResponse<'getRun'>> {
  return request<OkResponse<'getRun'>>(`/runs/${id}`)
}

export function stopRun(id: string, idempotencyKey?: string): Promise<OkResponse<'stopRun'>> {
  return request<OkResponse<'stopRun'>>(`/runs/${id}/stop`, { method: 'POST', idempotencyKey })
}

export function resumeRun(id: string, idempotencyKey?: string): Promise<OkResponse<'resumeRun'>> {
  return request<OkResponse<'resumeRun'>>(`/runs/${id}/resume`, { method: 'POST', idempotencyKey })
}

export function createNote(
  id: string,
  body: CreateNoteRequest,
  idempotencyKey?: string,
): Promise<CreatedResponse<'createNote'>> {
  return request<CreatedResponse<'createNote'>>(`/runs/${id}/notes`, {
    method: 'POST',
    body,
    idempotencyKey,
  })
}

export function listRunEvents(
  id: string,
  params: { after_id?: number; limit?: number } = {},
): Promise<OkResponse<'listRunEvents'>> {
  return request<OkResponse<'listRunEvents'>>(`/runs/${id}/events`, { query: { ...params } })
}

/**
 * Содержимое файла артефакта (F-02, fix-task-4): 200 → text/plain (сырой текст),
 * ошибки — JSON Error{code,message} (404 not_found, 413 too_large > 5 МБ, 401).
 * Общий request не подходит: он парсит успех как JSON.
 */
export async function getArtifactContent(runId: string, artifactId: number): Promise<string> {
  const url = new URL(`${apiBaseUrl()}/runs/${runId}/artifacts/${artifactId}/content`)
  const headers = new Headers()
  const token = apiToken()
  if (token) headers.set('Authorization', `Bearer ${token}`)

  let response: Response
  try {
    response = await fetch(url, { headers })
  } catch (cause) {
    throw new ApiError(0, 'daemon_unreachable', 'демон недоступен', {
      cause: cause instanceof Error ? cause.message : String(cause),
    })
  }
  const text = await response.text()
  if (!response.ok) {
    try {
      const parsed: unknown = JSON.parse(text)
      if (isErrorBody(parsed)) {
        throw new ApiError(response.status, parsed.code, parsed.message, isDetails(parsed.details) ? parsed.details : undefined)
      }
    } catch (error) {
      if (error instanceof ApiError) throw error
    }
    throw new ApiError(response.status, 'internal', `HTTP ${response.status}`)
  }
  return text
}

export function resolveGate(
  id: string,
  body: ResolveGateRequest,
  idempotencyKey?: string,
): Promise<OkResponse<'resolveGate'>> {
  return request<OkResponse<'resolveGate'>>(`/gates/${id}/resolve`, {
    method: 'POST',
    body,
    idempotencyKey,
  })
}

export function interruptStage(
  id: number,
  body: InterruptStageRequest,
  idempotencyKey?: string,
): Promise<OkResponse<'interruptStage'>> {
  return request<OkResponse<'interruptStage'>>(`/stages/${id}/interrupt`, { method: 'POST', body, idempotencyKey })
}

// --- T-23 vendor-память, T-24 метрики ---

export type MemoryTree = components['schemas']['MemoryTree']
export type MemoryFileEntry = components['schemas']['MemoryFileEntry']
export type MemoryFileContent = components['schemas']['MemoryFileContent']
export type MemoryWriteFileRequest = components['schemas']['MemoryWriteFileRequest']
export type MemoryHistoryEntry = components['schemas']['MemoryHistoryEntry']
export type MemoryPromoteRequest = components['schemas']['MemoryPromoteRequest']
export type RunMetrics = components['schemas']['RunMetrics']
export type StageMetrics = components['schemas']['StageMetrics']
export type ProjectMetrics = components['schemas']['ProjectMetrics']

export function memoryTree(): Promise<OkResponse<'memoryTree'>> {
  return request<OkResponse<'memoryTree'>>('/memory/tree')
}

type MemoryReadQuery = NonNullable<operations['memoryReadFile']['parameters']['query']>

export function memoryReadFile(query: MemoryReadQuery): Promise<OkResponse<'memoryReadFile'>> {
  return request<OkResponse<'memoryReadFile'>>('/memory/file', { query: { ...query } })
}

export function memoryWriteFile(body: MemoryWriteFileRequest): Promise<OkResponse<'memoryWriteFile'>> {
  return request<OkResponse<'memoryWriteFile'>>('/memory/file', { method: 'PUT', body })
}

type MemoryHistoryQuery = NonNullable<operations['memoryHistory']['parameters']['query']>

export function memoryHistory(query: MemoryHistoryQuery): Promise<OkResponse<'memoryHistory'>> {
  return request<OkResponse<'memoryHistory'>>('/memory/history', { query: { ...query } })
}

export function memoryPromote(body: MemoryPromoteRequest): Promise<OkResponse<'memoryPromote'>> {
  return request<OkResponse<'memoryPromote'>>('/memory/promote', { method: 'POST', body })
}

export function getRunMetrics(id: string): Promise<OkResponse<'getRunMetrics'>> {
  return request<OkResponse<'getRunMetrics'>>(`/runs/${id}/metrics`)
}

/** Метрики рана в CSV (text/csv) — для скачивания. */
export function getRunMetricsCsv(id: string): Promise<string> {
  return requestText(`/runs/${id}/metrics.csv`)
}

type ProjectMetricsQuery = NonNullable<operations['getProjectMetrics']['parameters']['query']>

export function getProjectMetrics(
  id: number,
  query: ProjectMetricsQuery = {},
): Promise<OkResponse<'getProjectMetrics'>> {
  return request<OkResponse<'getProjectMetrics'>>(`/projects/${id}/metrics`, { query: { ...query } })
}

// --- настройки, fs-пикер, уроки, удаление проекта ---

export type Settings = components['schemas']['Settings']
export type TelegramSettings = components['schemas']['TelegramSettings']
export type TelegramSettingsPut = components['schemas']['TelegramSettingsPut']
export type SupervisorSettings = components['schemas']['SupervisorSettings']
export type SupervisorSettingsPut = components['schemas']['SupervisorSettingsPut']
export type FsBrowseResult = components['schemas']['FsBrowseResult']
export type Lesson = components['schemas']['Lesson']
export type LessonDetail = components['schemas']['LessonDetail']
export type LessonDuplicatePair = components['schemas']['LessonDuplicatePair']
export type LessonConsolidation = components['schemas']['LessonConsolidation']

export function getSettings(): Promise<OkResponse<'getSettings'>> {
  return request<OkResponse<'getSettings'>>('/settings')
}

export function putTelegramSettings(body: TelegramSettingsPut): Promise<OkResponse<'putTelegramSettings'>> {
  return request<OkResponse<'putTelegramSettings'>>('/settings/telegram', { method: 'PUT', body })
}

export function putSupervisorSettings(body: SupervisorSettingsPut): Promise<OkResponse<'putSupervisorSettings'>> {
  return request<OkResponse<'putSupervisorSettings'>>('/settings/supervisor', { method: 'PUT', body })
}

type FsBrowseQuery = NonNullable<operations['fsBrowse']['parameters']['query']>

export function fsBrowse(query: FsBrowseQuery = {}): Promise<OkResponse<'fsBrowse'>> {
  return request<OkResponse<'fsBrowse'>>('/fs/browse', { query: { ...query } })
}

type ListLessonsQuery = NonNullable<operations['listLessons']['parameters']['query']>

export function listLessons(filter: ListLessonsQuery = {}): Promise<OkResponse<'listLessons'>> {
  return request<OkResponse<'listLessons'>>('/lessons', { query: { ...filter } })
}

export function getLesson(id: string): Promise<OkResponse<'getLesson'>> {
  return request<OkResponse<'getLesson'>>(`/lessons/${id}`)
}

/** Кандидаты на консолидацию уроков (T-30): дубли, протухшие superseded, нездоровые. */
export function lessonConsolidation(supersededDays?: number): Promise<OkResponse<'lessonConsolidationCandidates'>> {
  return request<OkResponse<'lessonConsolidationCandidates'>>('/lessons/consolidation', {
    query: { superseded_days: supersededDays },
  })
}

type PatchLessonBody = NonNullable<
  operations['patchLesson']['requestBody']
>['content']['application/json']

export function patchLesson(id: string, body: PatchLessonBody): Promise<OkResponse<'patchLesson'>> {
  return request<OkResponse<'patchLesson'>>(`/lessons/${id}`, { method: 'PATCH', body })
}

export function deleteProject(id: number): Promise<OkResponse<'deleteProject'>> {
  return request<OkResponse<'deleteProject'>>(`/projects/${id}`, { method: 'DELETE' })
}
