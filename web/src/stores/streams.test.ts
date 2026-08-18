import { beforeEach, describe, expect, it } from 'vitest'
import type { Event } from '../api/client'
import { STREAM_BUFFER_CAP, useStreamsStore } from './streams'

/*
  F-01: история стрима не режется cap'ом — ран с 5000+ событий смотрится
  целиком; cap применяется только к live-буферу до REST-догона.
*/

function makeEvent(id: number, stageId = 1): Event {
  return {
    id,
    run_id: 'run-1',
    stage_id: stageId,
    ts: new Date(1_700_000_000_000 + id * 1000).toISOString(),
    kind: 'stream.text',
    payload: { text: `event ${id}` },
  }
}

function makeRange(from: number, to: number, stageId = 1): Event[] {
  const events: Event[] = []
  for (let id = from; id <= to; id++) events.push(makeEvent(id, stageId))
  return events
}

beforeEach(() => {
  useStreamsStore.getState().clear()
})

describe('streams store', () => {
  it('mergeHistory: >2000 событий без потерь, порядок по id сохраняется', () => {
    useStreamsStore.getState().mergeHistory(1, makeRange(1, 5000))
    const buffer = useStreamsStore.getState().byStageId[1]
    expect(buffer).toHaveLength(5000)
    expect(buffer[0].id).toBe(1)
    expect(buffer[buffer.length - 1].id).toBe(5000)
    for (let i = 1; i < buffer.length; i++) expect(buffer[i].id).toBeGreaterThan(buffer[i - 1].id)
  })

  it('mergeHistory: дедуп по id при пересечении истории и live-буфера', () => {
    useStreamsStore.getState().appendMany(1, makeRange(4990, 5010))
    useStreamsStore.getState().mergeHistory(1, makeRange(1, 5000))
    const buffer = useStreamsStore.getState().byStageId[1]
    expect(buffer).toHaveLength(5010)
    const ids = buffer.map((e) => e.id)
    expect(new Set(ids).size).toBe(ids.length)
  })

  it('live-append ПОСЛЕ догона не режет историю', () => {
    useStreamsStore.getState().mergeHistory(1, makeRange(1, 5000))
    useStreamsStore.getState().append(1, makeEvent(5001))
    const buffer = useStreamsStore.getState().byStageId[1]
    expect(buffer).toHaveLength(5001)
    expect(buffer[0].id).toBe(1)
  })

  it('live-буфер ДО догона ограничен cap', () => {
    useStreamsStore.getState().appendMany(1, makeRange(1, STREAM_BUFFER_CAP + 500))
    const buffer = useStreamsStore.getState().byStageId[1]
    expect(buffer).toHaveLength(STREAM_BUFFER_CAP)
    expect(buffer[0].id).toBe(501) // самые старые отброшены
  })

  it('mergeHistory после cap-дропа восстанавливает начало из REST', () => {
    // live пришло 2500 событий → буфер урезан до 2000; догон возвращает полную историю
    useStreamsStore.getState().appendMany(1, makeRange(1, STREAM_BUFFER_CAP + 500))
    useStreamsStore.getState().mergeHistory(1, makeRange(1, STREAM_BUFFER_CAP + 500))
    const buffer = useStreamsStore.getState().byStageId[1]
    expect(buffer).toHaveLength(STREAM_BUFFER_CAP + 500)
    expect(buffer[0].id).toBe(1)
  })

  it('mergeHistory с неотсортированным входом выдаёт порядок по id', () => {
    const shuffled = [...makeRange(1, 100)].reverse()
    useStreamsStore.getState().mergeHistory(1, shuffled)
    const buffer = useStreamsStore.getState().byStageId[1]
    expect(buffer.map((e) => e.id)).toEqual(Array.from({ length: 100 }, (_, i) => i + 1))
  })
})
