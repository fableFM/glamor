import { afterEach, describe, expect, it } from 'vitest'
import { WebSocketServer } from 'ws'
import type { WebSocket as ServerWebSocket } from 'ws'
import type { AddressInfo } from 'node:net'
import { EventClient } from './events'
import type { ConnectionStatus, EventPhase } from './events'
import type { Event } from '../api/client'

/*
  Тест WS-клиента против реального мок-сервера (пакет ws).
  Клиент ходит глобальным WebSocket (Node 22+), сервер — на случайном порту.
*/

function makeEvent(id: number, kind = 'stream.text'): Event {
  return {
    id,
    run_id: 'run-1',
    stage_id: 1,
    ts: new Date(1_700_000_000_000 + id * 1000).toISOString(),
    kind: kind as Event['kind'],
    payload: { text: `event ${id}` },
  }
}

function synced(lastEventId: number) {
  return { type: 'synced', last_event_id: lastEventId }
}

interface MockConnection {
  lastEventId: number
  runId: string
}

interface MockConn {
  send: (message: unknown) => void
  close: (code?: number, reason?: string) => void
}

interface MockServer {
  url: string
  connections: MockConnection[]
  close: () => Promise<void>
}

function startMockServer(onConnection: (conn: MockConn) => void): Promise<MockServer> {
  const wss = new WebSocketServer({ port: 0 })
  const sockets = new Set<ServerWebSocket>()
  const connections: MockConnection[] = []

  wss.on('connection', (socket, request) => {
    sockets.add(socket)
    socket.on('close', () => sockets.delete(socket))
    const url = new URL(request.url ?? '/', 'http://localhost')
    connections.push({
      lastEventId: Number(url.searchParams.get('last_event_id') ?? '0'),
      runId: url.searchParams.get('run_id') ?? '',
    })
    onConnection({
      send: (message) => socket.send(JSON.stringify(message)),
      close: (code, reason) => socket.close(code, reason),
    })
  })

  return new Promise((resolve) => {
    wss.on('listening', () => {
      const { port } = wss.address() as AddressInfo
      resolve({
        url: `ws://127.0.0.1:${port}`,
        connections,
        close: () =>
          new Promise<void>((done) => {
            for (const socket of sockets) socket.terminate()
            wss.close(() => done())
          }),
      })
    })
  })
}

async function waitFor(condition: () => boolean, timeoutMs = 3000): Promise<void> {
  const start = Date.now()
  while (!condition()) {
    if (Date.now() - start > timeoutMs) throw new Error('waitFor: timeout')
    await new Promise((resolve) => setTimeout(resolve, 10))
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

const FAST_BACKOFF = { initialMs: 20, factor: 2, maxMs: 50, jitter: 0 }

describe('EventClient', () => {
  let server: MockServer | undefined
  let client: EventClient | undefined

  afterEach(async () => {
    client?.stop()
    await server?.close()
    client = undefined
    server = undefined
  })

  it('synced-граница разделяет replay (bulk) и live (инкрементально)', async () => {
    const dispatched: { phase: EventPhase; ids: number[] }[] = []
    const statuses: ConnectionStatus[] = []

    server = await startMockServer((conn) => {
      // догон: события 1..3, граница, затем live 4 и 5
      conn.send(makeEvent(1))
      conn.send(makeEvent(2))
      conn.send(makeEvent(3))
      conn.send(synced(3))
      void sleep(50).then(() => {
        conn.send(makeEvent(4))
        conn.send(makeEvent(5))
      })
    })

    client = new EventClient({
      runId: '*',
      baseUrl: server.url,
      backoff: FAST_BACKOFF,
      dispatch: (events, phase) => dispatched.push({ phase, ids: events.map((e) => e.id) }),
      onStatus: (status) => statuses.push(status),
    })
    client.start()

    await waitFor(() => dispatched.some((d) => d.phase === 'live' && d.ids.includes(5)))

    expect(dispatched).toEqual([
      { phase: 'replay', ids: [1, 2, 3] },
      { phase: 'live', ids: [4] },
      { phase: 'live', ids: [5] },
    ])
    expect(statuses[0]).toBe('reconnecting')
    expect(statuses).toContain('online')
    expect(client.lastEventId()).toBe(5)
  })

  it('разрыв → реконнект с last_event_id → догон без дублей и пропусков', async () => {
    const applied: number[] = []
    const statuses: ConnectionStatus[] = []

    server = await startMockServer((() => {
      let index = 0
      return (conn: MockConn) => {
        index += 1
        if (index === 1) {
          // первая сессия: догон 1..5, live 6..7, затем сервер рвёт (resync required)
          for (let id = 1; id <= 5; id += 1) conn.send(makeEvent(id))
          conn.send(synced(5))
          void sleep(50).then(() => {
            conn.send(makeEvent(6))
            conn.send(makeEvent(7))
          })
          void sleep(150).then(() => conn.close(1001, 'resync required'))
          return
        }
        // вторая сессия: сервер присылает 7 повторно (граничный дубль), 8..9 догоном
        conn.send(makeEvent(7))
        conn.send(makeEvent(8))
        conn.send(makeEvent(9))
        conn.send(synced(9))
        void sleep(50).then(() => conn.send(makeEvent(10)))
      }
    })())

    client = new EventClient({
      runId: '*',
      baseUrl: server.url,
      backoff: FAST_BACKOFF,
      dispatch: (events) => applied.push(...events.map((e) => e.id)),
      onStatus: (status) => statuses.push(status),
    })
    client.start()

    await waitFor(() => applied.length >= 10 && applied.includes(10))
    // даём возможность проявиться лишним диспатчам
    await sleep(100)

    // последовательность строго 1..10, без дублей и пропусков
    expect(applied).toEqual([1, 2, 3, 4, 5, 6, 7, 8, 9, 10])
    // реконнект пошёл с последним применённым last_event_id = 7
    expect(server.connections.length).toBe(2)
    expect(server.connections[0].lastEventId).toBe(0)
    expect(server.connections[1].lastEventId).toBe(7)
    // статус уходил в reconnecting и вернулся в online
    expect(statuses.filter((s) => s === 'reconnecting').length).toBeGreaterThanOrEqual(2)
    expect(statuses[statuses.length - 1]).toBe('online')
    expect(client.lastEventId()).toBe(10)
  })

  it('обрыв посреди replay (до synced): буфер откатывается, догон начинается заново', async () => {
    const dispatched: { phase: EventPhase; ids: number[] }[] = []

    server = await startMockServer((() => {
      let index = 0
      return (conn: MockConn) => {
        index += 1
        if (index === 1) {
          // replay без synced и обрыв — ничего не должно примениться
          conn.send(makeEvent(1))
          conn.send(makeEvent(2))
          void sleep(50).then(() => conn.close(1001, 'resync required'))
          return
        }
        // полный догон с нуля
        conn.send(makeEvent(1))
        conn.send(makeEvent(2))
        conn.send(makeEvent(3))
        conn.send(synced(3))
        void sleep(50).then(() => conn.send(makeEvent(4)))
      }
    })())

    client = new EventClient({
      runId: '*',
      baseUrl: server.url,
      backoff: FAST_BACKOFF,
      dispatch: (events, phase) => dispatched.push({ phase, ids: events.map((e) => e.id) }),
    })
    client.start()

    await waitFor(() => dispatched.some((d) => d.phase === 'live' && d.ids.includes(4)))

    expect(dispatched).toEqual([
      { phase: 'replay', ids: [1, 2, 3] },
      { phase: 'live', ids: [4] },
    ])
    // реконнект с тем же last_event_id = 0, т.к. replay не был зафиксирован
    expect(server.connections[1].lastEventId).toBe(0)
  })
})
