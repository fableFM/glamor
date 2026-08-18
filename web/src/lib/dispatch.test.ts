import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { Event, RunDetail } from '../api/client'
import { dispatchEvents } from './dispatch'
import { useRunsStore } from '../stores/runs'

/*
  F-01/F-03: новые раны появляются в списках без ручного рефреша.
  run.created несёт объект рана в payload — карточка строится без REST.
  Старые раны (без run.created): live run.*-событие с неизвестным run_id
  догоняется REST'ом (getRun мокается).
*/

const getRunMock = vi.fn<(id: string) => Promise<RunDetail>>()

vi.mock('../api/client', async (importOriginal) => {
  const original = await importOriginal<typeof import('../api/client')>()
  return { ...original, getRun: (id: string) => getRunMock(id) }
})

function makeRunDetail(id: string, state: RunDetail['state'] = 'running'): RunDetail {
  return {
    id,
    project_id: 1,
    pipeline_version_id: 1,
    task_text: 'task',
    base_branch: 'main',
    branch: 'glamor/task',
    state,
    depth: 1,
    notify_tg: true,
    created_at: '2026-08-17T10:00:00Z',
    stages: [],
    gates: [],
    artifacts: [],
    notes: [],
  }
}

function makeEvent(id: number, runId: string, kind = 'run.state_changed', to = 'running'): Event {
  return {
    id,
    run_id: runId,
    stage_id: null,
    ts: new Date(1_700_000_000_000 + id * 1000).toISOString(),
    kind: kind as Event['kind'],
    payload: { to },
  }
}

beforeEach(() => {
  getRunMock.mockReset()
  useRunsStore.setState({ byId: {} })
})

function makeCreatedPayload(id: string): Record<string, unknown> {
  return {
    id,
    project_id: 1,
    pipeline_version_id: 2,
    task_text: 'task',
    base_branch: 'main',
    branch: 'glamor/task',
    state: 'draft',
    depth: 0,
    notify_tg: true,
    created_at: '2026-08-17T10:00:00Z',
  }
}

function makeCreatedEvent(id: number, runId: string, payload = makeCreatedPayload(runId)): Event {
  return {
    id,
    run_id: runId,
    stage_id: null,
    ts: new Date(1_700_000_000_000 + id * 1000).toISOString(),
    kind: 'run.created',
    payload,
  }
}

describe('dispatch: run.created (F-01)', () => {
  it('run.created → карточка из payload, REST-догон не нужен', () => {
    dispatchEvents([makeCreatedEvent(1, 'run-created')], 'live')
    expect(getRunMock).not.toHaveBeenCalled()
    const run = useRunsStore.getState().byId['run-created']
    expect(run).toBeDefined()
    expect(run.state).toBe('draft')
    expect(run.project_id).toBe(1)
    expect(run.pipeline_version_id).toBe(2)
    expect(run.base_branch).toBe('main')
    expect(run.depth).toBe(0)
    expect(run.notify_tg).toBe(true)
  })

  it('run.created кладёт пустой каркас в runDetails, но не затирает загруженный деталь', async () => {
    const { useRunDetailsStore } = await import('../stores/runDetails')
    useRunDetailsStore.setState({ byRunId: {} })
    dispatchEvents([makeCreatedEvent(1, 'run-fresh')], 'live')
    expect(useRunDetailsStore.getState().byRunId['run-fresh']).toMatchObject({
      id: 'run-fresh',
      stages: [],
      gates: [],
    })

    // деталь уже загружена REST'ом — run.created (replay) её не затирает
    const detail = makeRunDetail('run-known')
    useRunDetailsStore.getState().setDetail(detail)
    dispatchEvents([makeCreatedEvent(2, 'run-known')], 'replay')
    expect(useRunDetailsStore.getState().byRunId['run-known']).toBe(detail)
  })

  it('битый payload run.created → fallback на REST-догон', async () => {
    getRunMock.mockResolvedValue(makeRunDetail('run-broken'))
    dispatchEvents([makeCreatedEvent(1, 'run-broken', { id: 'run-broken' })], 'live')
    await vi.waitFor(() => expect(useRunsStore.getState().byId['run-broken']).toBeDefined())
    expect(getRunMock).toHaveBeenCalledWith('run-broken')
  })
})


describe('dispatch: неизвестный ран (F-03)', () => {
  it('live run.* с неизвестным run_id → догон getRun и upsert в runs-стор', async () => {
    getRunMock.mockResolvedValue(makeRunDetail('run-new'))
    dispatchEvents([makeEvent(1, 'run-new')], 'live')
    expect(getRunMock).toHaveBeenCalledWith('run-new')
    await vi.waitFor(() => expect(useRunsStore.getState().byId['run-new']).toBeDefined())
    expect(useRunsStore.getState().byId['run-new'].state).toBe('running')
  })

  it('replay неизвестного рана НЕ догоняется (начальный REST-снапшот всё покрыл)', () => {
    dispatchEvents([makeEvent(1, 'run-old')], 'replay')
    expect(getRunMock).not.toHaveBeenCalled()
  })

  it('известный ран: догон не нужен, state двигается из payload', () => {
    useRunsStore.getState().upsertRun(makeRunDetail('run-1', 'draft'))
    dispatchEvents([makeEvent(1, 'run-1')], 'live')
    expect(getRunMock).not.toHaveBeenCalled()
    expect(useRunsStore.getState().byId['run-1'].state).toBe('running')
  })

  it('параллельные события одного рана — один fetch (dedup)', () => {
    let resolveFetch: (run: RunDetail) => void = () => undefined
    getRunMock.mockImplementation(
      () =>
        new Promise<RunDetail>((resolve) => {
          resolveFetch = resolve
        }),
    )
    dispatchEvents([makeEvent(1, 'run-x'), makeEvent(2, 'run-x')], 'live')
    expect(getRunMock).toHaveBeenCalledTimes(1)
    resolveFetch(makeRunDetail('run-x'))
  })

  it('гонка догона: события за время полёта fetch применяются поверх снапшота (fix-task-3 п.3)', async () => {
    let resolveFetch: (run: RunDetail) => void = () => undefined
    getRunMock.mockImplementation(
      () =>
        new Promise<RunDetail>((resolve) => {
          resolveFetch = resolve
        }),
    )
    // снапшот прочитан до второго перехода: fetch вернёт устаревший стейт 'running'
    dispatchEvents([makeEvent(1, 'run-race', 'run.state_changed', 'running')], 'live')
    dispatchEvents([makeEvent(2, 'run-race', 'run.state_changed', 'waiting_gate')], 'live')
    expect(getRunMock).toHaveBeenCalledTimes(1)
    resolveFetch(makeRunDetail('run-race', 'running'))
    await vi.waitFor(() => expect(useRunsStore.getState().byId['run-race']?.state).toBe('waiting_gate'))
  })
})
