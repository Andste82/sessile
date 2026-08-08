import { describe, expect, it } from 'vitest'
import { parseEvent } from './events'

const validSession = {
  id: 'abc',
  name: 'build',
  directory: 'project-a',
  shell: 'bash',
  status: 'running',
  pid: 42,
  created: '2026-08-07T10:00:00Z',
  lastActivity: '2026-08-07T10:05:00Z',
  rows: 24,
  cols: 80,
  clientCount: 2,
  activity: 'waiting',
  activitySince: '2026-08-07T10:04:00Z',
  command: 'claude',
  cwd: 'project-a/backend',
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

  it('accepts a stopped session, whose activity is the empty string', () => {
    const stopped = { ...validSession, status: 'stopped', activity: '', command: '', cwd: '' }
    const ev = parseEvent(JSON.stringify({ type: 'session', session: stopped }))
    expect(ev).toEqual({ type: 'session', session: stopped })
  })

  // The indicator and the card switch on status and activity. A value outside
  // the union would render as nothing at all, with nothing to point at.
  it.each([
    ['an unknown status', { ...validSession, status: 'zombie' }],
    ['an unknown activity', { ...validSession, activity: 'thinking' }],
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
    delete (sparse as Record<string, unknown>).activitySince
    const ev = parseEvent(JSON.stringify({ type: 'session', session: sparse }))
    expect(ev).toHaveProperty('session.cwd', '')
    expect(ev).toHaveProperty('session.activitySince', '')
  })
})
