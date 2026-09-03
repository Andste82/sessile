import { describe, expect, it } from 'vitest'
import { parseEvent } from './events'

const validSession = {
  id: 'abc',
  name: 'build',
  targetType: 'local',
  directory: 'project-a',
  shell: 'bash',
  hostId: '',
  hostDisplayName: '',
  status: 'running',
  pid: 42,
  created: '2026-08-07T10:00:00Z',
  lastActivity: '2026-08-07T10:05:00Z',
  rows: 24,
  cols: 80,
  clientCount: 2,
  command: 'claude',
  cwd: 'project-a/backend',
  title: 'npm run dev',
}

describe('parseEvent', () => {
  it('parses a full session, derived fields included', () => {
    const ev = parseEvent(JSON.stringify({ type: 'session', session: validSession }))
    expect(ev).toEqual({ type: 'session', session: validSession })
  })

  it('parses a snapshot', () => {
    const ev = parseEvent(JSON.stringify({ type: 'sessions', sessions: [validSession] }))
    expect(ev?.type).toBe('sessions')
    expect(ev).toHaveProperty('sessions', [validSession])
  })

  it('parses an empty snapshot rather than treating it as invalid', () => {
    expect(parseEvent(JSON.stringify({ type: 'sessions', sessions: [] }))).toEqual({
      type: 'sessions',
      sessions: [],
    })
  })

  it('parses a deletion', () => {
    expect(parseEvent(JSON.stringify({ type: 'sessionGone', sessionId: 'abc' }))).toEqual({
      type: 'sessionGone',
      sessionId: 'abc',
    })
  })

  it('accepts a stopped session, whose derived fields are empty', () => {
    const stopped = { ...validSession, status: 'stopped', command: '', cwd: '' }
    const ev = parseEvent(JSON.stringify({ type: 'session', session: stopped }))
    expect(ev).toEqual({ type: 'session', session: stopped })
  })

  // The indicator and the card switch on status. A value outside the union
  // would render as nothing at all, with nothing to point at.
  it.each([
    ['an unknown status', { ...validSession, status: 'zombie' }],
    ['a missing id', { ...validSession, id: '' }],
    ['a non-string name', { ...validSession, name: 7 }],
  ])('rejects a session with %s', (_label, session) => {
    expect(parseEvent(JSON.stringify({ type: 'session', session }))).toBeNull()
  })

  // Losing one entry beats losing the list: the rest is still the truth, and an
  // empty dashboard would give no clue why.
  it('drops only the malformed entries of a snapshot', () => {
    const ev = parseEvent(
      JSON.stringify({
        type: 'sessions',
        sessions: [validSession, { ...validSession, id: 'b', status: 'zombie' }],
      }),
    )
    expect(ev).toEqual({ type: 'sessions', sessions: [validSession] })
  })

  it.each([
    ['not JSON', 'definitely not json'],
    ['a JSON scalar', '42'],
    ['null', 'null'],
    ['an object with no type', '{"sessions":[]}'],
    ['an unknown type', '{"type":"somethingElse"}'],
    ['a snapshot whose sessions is not an array', '{"type":"sessions","sessions":{}}'],
    ['a deletion with no id', '{"type":"sessionGone","sessionId":""}'],
    // The server sends this when it cannot build a snapshot (§5.1). There is
    // nothing to apply and the subscription stands, so it is not an event.
    ['the error frame', '{"type":"error","message":"cannot list sessions"}'],
  ])('returns null for %s', (_label, input) => {
    expect(parseEvent(input)).toBeNull()
  })

  it('fills absent optional strings rather than producing undefined fields', () => {
    const sparse = { ...validSession }
    delete (sparse as Record<string, unknown>).cwd
    delete (sparse as Record<string, unknown>).command
    delete (sparse as Record<string, unknown>).title
    const ev = parseEvent(JSON.stringify({ type: 'session', session: sparse }))
    expect(ev).toHaveProperty('session.cwd', '')
    expect(ev).toHaveProperty('session.command', '')
    expect(ev).toHaveProperty('session.title', '')
  })

  it('parses a hostopStarted event', () => {
    const ev = parseEvent(
      JSON.stringify({ type: 'hostopStarted', sessionId: 's1', opId: 'op1', kind: 'delete', path: '/tmp/x' }),
    )
    expect(ev).toEqual({ type: 'hostopStarted', sessionId: 's1', opId: 'op1', kind: 'delete', path: '/tmp/x' })
  })

  it('parses a hostopProgress event', () => {
    const ev = parseEvent(JSON.stringify({ type: 'hostopProgress', sessionId: 's1', opId: 'op1', done: 3, total: 10 }))
    expect(ev).toEqual({ type: 'hostopProgress', sessionId: 's1', opId: 'op1', done: 3, total: 10 })
  })

  it('parses a hostopDone event, ok and error alike', () => {
    expect(parseEvent(JSON.stringify({ type: 'hostopDone', sessionId: 's1', opId: 'op1', status: 'ok' }))).toEqual({
      type: 'hostopDone',
      sessionId: 's1',
      opId: 'op1',
      status: 'ok',
      message: '',
    })
    expect(
      parseEvent(JSON.stringify({ type: 'hostopDone', sessionId: 's1', opId: 'op1', status: 'error', message: 'boom' })),
    ).toEqual({ type: 'hostopDone', sessionId: 's1', opId: 'op1', status: 'error', message: 'boom' })
  })

  it.each([
    ['hostopStarted missing opId', '{"type":"hostopStarted","sessionId":"s1"}'],
    ['hostopProgress missing sessionId', '{"type":"hostopProgress","opId":"op1","done":1,"total":2}'],
    ['hostopDone missing opId', '{"type":"hostopDone","sessionId":"s1","status":"ok"}'],
  ])('returns null for %s', (_label, input) => {
    expect(parseEvent(input)).toBeNull()
  })
})
