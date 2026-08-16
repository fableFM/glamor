import { apiBaseUrl, apiToken } from '../api/client'
import type { Event, SyncedMessage } from '../api/client'

/*
  WS-клиент журнала событий демона (T-04/T-13).

  Протокол /ws:
  1. догон — Event'ы с id > last_event_id;
  2. SyncedMessage {type:"synced", last_event_id} — граница replay/live;
  3. live-поток Event'ов.

  Семантика обработки:
  - события догона буферизуются и применяются bulk'ом на synced-границе;
    если соединение рвётся до synced — буфер выбрасывается, last_event_id
    не двигается, догон начнётся заново (атомарность replay);
  - live-события применяются инкрементально, last_event_id двигается
    по каждому;
  - защита от дублей: событие с id <= last_event_id отбрасывается
    (сервер при реконнекте может прислать граничное событие повторно);
  - обрыв (close 1001 "resync required" или сетевой разрыв) → reconnect
    с экспоненциальным backoff и последним известным last_event_id.
*/

export type ConnectionStatus = 'online' | 'reconnecting' | 'offline'

/** replay — события до synced-границы (bulk apply), live — после (инкрементально). */
export type EventPhase = 'replay' | 'live'

/** Минимальный структурный интерфейс WebSocket — позволяет подменить реализацию в тестах. */
export interface WebSocketLike {
  readonly readyState: number
  onopen: (() => void) | null
  onmessage: ((event: { data: unknown }) => void) | null
  onclose: ((event: { code: number; reason: string }) => void) | null
  onerror: (() => void) | null
  close(): void
}

export interface BackoffOptions {
  /** первая задержка перед реконнектом */
  initialMs: number
  /** множитель экспоненты */
  factor: number
  /** потолок задержки */
  maxMs: number
  /** доля джиттера (0.2 = ±20%) */
  jitter: number
}

const DEFAULT_BACKOFF: BackoffOptions = {
  initialMs: 500,
  factor: 2,
  maxMs: 8_000,
  jitter: 0.2,
}

export interface EventClientOptions {
  /** ран подписки; "*" — все раны (инбокс) */
  runId: string
  /** базовый URL демона; default — apiBaseUrl() */
  baseUrl?: string
  /** токен localhost-API (D-08); default — apiToken() */
  token?: string
  /** фабрика сокета; default — глобальный WebSocket */
  createWebSocket?: (url: string) => WebSocketLike
  /** приёмник событий: replay батчами, live по одному */
  dispatch: (events: Event[], phase: EventPhase) => void
  /** уведомления о статусе соединения (connection store) */
  onStatus?: (status: ConnectionStatus) => void
  backoff?: Partial<BackoffOptions>
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null
}

function isEventMessage(value: unknown): value is Event {
  return (
    isRecord(value) &&
    typeof value.id === 'number' &&
    typeof value.run_id === 'string' &&
    typeof value.kind === 'string' &&
    typeof value.ts === 'string' &&
    isRecord(value.payload)
  )
}

function isSyncedMessage(value: unknown): value is SyncedMessage {
  return isRecord(value) && value.type === 'synced' && typeof value.last_event_id === 'number'
}

export class EventClient {
  private readonly options: EventClientOptions
  private readonly backoff: BackoffOptions
  /** last_event_id per run_id подписки (D-04: догон при реконнекте). */
  private readonly lastEventIdByRun = new Map<string, number>()

  private socket: WebSocketLike | null = null
  private stopped = false
  private attempts = 0
  private retryTimer: ReturnType<typeof setTimeout> | null = null

  /** буфер догона: применяется только на synced-границе */
  private replayBuffer: Event[] = []
  /** last_event_id на момент текущего коннекта; откатываемся к нему при обрыве replay */
  private sessionStartId = 0
  private synced = false

  constructor(options: EventClientOptions) {
    this.options = options
    this.backoff = { ...DEFAULT_BACKOFF, ...options.backoff }
  }

  start(): void {
    this.stopped = false
    this.setStatus('reconnecting')
    this.connect()
  }

  stop(): void {
    this.stopped = true
    if (this.retryTimer !== null) {
      clearTimeout(this.retryTimer)
      this.retryTimer = null
    }
    const socket = this.socket
    this.socket = null
    socket?.close()
    this.setStatus('offline')
  }

  /** last_event_id подписки (для диагностики/тестов). */
  lastEventId(): number {
    return this.cursor
  }

  private get cursor(): number {
    return this.lastEventIdByRun.get(this.options.runId) ?? 0
  }

  private set cursor(value: number) {
    this.lastEventIdByRun.set(this.options.runId, value)
  }

  private buildUrl(): string {
    const base = (this.options.baseUrl ?? apiBaseUrl()).replace(/\/$/, '').replace(/^http/, 'ws')
    const params = new URLSearchParams({
      run_id: this.options.runId,
      last_event_id: String(this.cursor),
    })
    const token = this.options.token ?? apiToken()
    if (token) params.set('token', token)
    return `${base}/ws?${params.toString()}`
  }

  private connect(): void {
    this.replayBuffer = []
    this.sessionStartId = this.cursor
    this.synced = false

    const defaultFactory = (url: string) => new WebSocket(url) as unknown as WebSocketLike
    const create = this.options.createWebSocket ?? defaultFactory
    const socket = create(this.buildUrl())
    this.socket = socket

    socket.onopen = () => {
      this.attempts = 0
    }
    socket.onmessage = (message) => {
      this.handleMessage(message.data)
    }
    socket.onclose = () => {
      if (this.socket !== socket) return // старый сокет после stop()
      this.socket = null
      this.handleClose()
    }
    socket.onerror = () => {
      // всегда следом приходит onclose — вся логика там
    }
  }

  private handleMessage(data: unknown): void {
    if (typeof data !== 'string') return
    let parsed: unknown
    try {
      parsed = JSON.parse(data)
    } catch {
      return // мусор в канале — игнорируем, не рвём соединение
    }

    if (isSyncedMessage(parsed)) {
      // Граница replay/live: применяем буфер догона bulk'ом.
      if (this.synced) return
      this.synced = true
      const batch = this.replayBuffer
      this.replayBuffer = []
      this.cursor = Math.max(this.cursor, parsed.last_event_id)
      if (batch.length > 0) this.options.dispatch(batch, 'replay')
      this.setStatus('online')
      return
    }

    if (!isEventMessage(parsed)) return

    // Защита от дублей: журнал монотонный, всё старее last_event_id — повтор.
    if (parsed.id <= this.cursor) return

    if (this.synced) {
      this.cursor = parsed.id
      this.options.dispatch([parsed], 'live')
    } else {
      this.replayBuffer.push(parsed)
    }
  }

  private handleClose(): void {
    if (this.stopped) return
    if (!this.synced) {
      // Обрыв посреди догона: буфер не применялся, откатываем курсор.
      this.replayBuffer = []
      this.cursor = this.sessionStartId
    }
    this.setStatus('reconnecting')
    this.retryTimer = setTimeout(() => {
      this.retryTimer = null
      this.connect()
    }, this.nextDelay())
  }

  private nextDelay(): number {
    const { initialMs, factor, maxMs, jitter } = this.backoff
    const base = Math.min(maxMs, initialMs * factor ** this.attempts)
    this.attempts += 1
    // джиттер ±jitter, чтобы несколько клиентов не реконнектились синхронно
    const spread = base * jitter
    return base - spread + Math.random() * spread * 2
  }

  private setStatus(status: ConnectionStatus): void {
    this.options.onStatus?.(status)
  }
}
