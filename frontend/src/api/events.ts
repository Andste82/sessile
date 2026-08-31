// Event-channel codec (PROJECT_PLAN.md §5.1).
//
// Separate from wsProtocol.ts on purpose: that module is the terminal socket,
// where text frames are control messages alongside binary terminal bytes. This
// one is /ws/events, which carries session list state and nothing else.

import type { Session, Status } from './types'

export interface SessionsEvent {
  type: 'sessions'
  sessions: Session[]
}
export interface SessionEvent {
  type: 'session'
  session: Session
}
export interface SessionGoneEvent {
  type: 'sessionGone'
  sessionId: string
}

export type ServerEvent = SessionsEvent | SessionEvent | SessionGoneEvent

const statuses: Status[] = ['running', 'stopped']

/**
 * Narrow one session object, or null if it is not one.
 *
 * Validated field by field rather than cast, because everything downstream — the
 * indicator, the card — switches on `status`, and a value outside the union
 * would render as nothing at all with no clue why.
 */
function parseSession(v: unknown): Session | null {
  if (typeof v !== 'object' || v === null) return null
  const s = v as Record<string, unknown>
  if (typeof s.id !== 'string' || s.id === '') return null
  if (typeof s.name !== 'string' || typeof s.shell !== 'string') return null
  if (!statuses.includes(s.status as Status)) return null

  return {
    id: s.id,
    name: s.name,
    directory: str(s.directory),
    shell: s.shell,
    status: s.status as Status,
    pid: num(s.pid),
    created: str(s.created),
    lastActivity: str(s.lastActivity),
    rows: num(s.rows),
    cols: num(s.cols),
    clientCount: num(s.clientCount),
    command: str(s.command),
    cwd: str(s.cwd),
    title: str(s.title),
  }
}

const str = (v: unknown): string => (typeof v === 'string' ? v : '')
const num = (v: unknown): number => (typeof v === 'number' ? v : 0)

/** Parse a server→client event frame, or null if it is not a valid one. */
export function parseEvent(data: string): ServerEvent | null {
  let msg: unknown
  try {
    msg = JSON.parse(data)
  } catch {
    return null
  }
  if (typeof msg !== 'object' || msg === null || !('type' in msg)) return null
  const m = msg as Record<string, unknown>

  switch (m.type) {
    case 'sessions': {
      if (!Array.isArray(m.sessions)) return null
      // One malformed entry drops that entry, not the whole snapshot: the rest
      // of the list is still the truth, and losing it would leave the dashboard
      // empty with no explanation.
      const sessions = m.sessions
        .map(parseSession)
        .filter((s): s is Session => s !== null)
      return { type: 'sessions', sessions }
    }
    case 'session': {
      const session = parseSession(m.session)
      return session ? { type: 'session', session } : null
    }
    case 'sessionGone':
      return typeof m.sessionId === 'string' && m.sessionId !== ''
        ? { type: 'sessionGone', sessionId: m.sessionId }
        : null
    default:
      // Includes the `error` frame the server sends when it cannot build a
      // snapshot (§5.1). There is nothing to apply, and the subscription
      // stands, so it is not this module's business.
      return null
  }
}

/** Build the event-channel URL from the current page origin. */
export function eventsWsURL(): string {
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${proto}//${window.location.host}/ws/events`
}
