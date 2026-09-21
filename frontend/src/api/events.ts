// Event-channel codec (PROJECT_PLAN.md §5.1).
//
// Separate from wsProtocol.ts on purpose: that module is the terminal socket,
// where text frames are control messages alongside binary terminal bytes. This
// one is /ws/events, which carries session list state and nothing else.

import type { Session, Status, TargetType } from './types'

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

// Hostop progress events (§4.10, §5.2) — Delete/Copy started/progress/done,
// keyed by opId so a listener can filter to the one it started.
export interface HostopStartedEvent {
  type: 'hostopStarted'
  sessionId: string
  opId: string
  kind: 'delete' | 'copy'
  path: string
}
export interface HostopProgressEvent {
  type: 'hostopProgress'
  sessionId: string
  opId: string
  done: number
  total: number
}
export interface HostopDoneEvent {
  type: 'hostopDone'
  sessionId: string
  opId: string
  status: 'ok' | 'error'
  message: string
}

// Task agent events (§4.17.4, §5.3): tool activity, held write calls, and
// the agent's status line.
export interface TaskToolEvent {
  type: 'taskTool'
  taskId: string
  callId: string
  name: string
  status: 'running' | 'ok' | 'error' | 'denied'
  message: string
}
export interface TaskApprovalEvent {
  type: 'taskApproval'
  taskId: string
  callId: string
  name: string
  input: unknown
  status: 'pending' | 'approved' | 'denied' | 'expired'
}
export interface TaskSummaryEvent {
  type: 'taskSummary'
  taskId: string
  summary: string
}
/** A task marked its own state (§4.18.2). */
export interface TaskStateEvent {
  type: 'taskState'
  taskId: string
  state: 'working' | 'blocked' | 'done'
  summary: string
  question: string
}

export type TaskEvent = TaskToolEvent | TaskApprovalEvent | TaskSummaryEvent | TaskStateEvent

export type ServerEvent =
  | SessionsEvent
  | SessionEvent
  | SessionGoneEvent
  | HostopStartedEvent
  | HostopProgressEvent
  | HostopDoneEvent
  | TaskEvent

const statuses: Status[] = ['running', 'stopped']
const targetTypes: TargetType[] = ['local', 'ssh']

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
    targetType: targetTypes.includes(s.targetType as TargetType) ? (s.targetType as TargetType) : 'local',
    directory: str(s.directory),
    shell: s.shell,
    hostId: str(s.hostId),
    hostDisplayName: str(s.hostDisplayName),
    group: str(s.group),
    taskId: typeof s.taskId === 'string' && s.taskId ? s.taskId : null,
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
    case 'hostopStarted':
      if (typeof m.opId !== 'string' || typeof m.sessionId !== 'string') return null
      return {
        type: 'hostopStarted',
        sessionId: m.sessionId,
        opId: m.opId,
        kind: m.kind === 'copy' ? 'copy' : 'delete',
        path: str(m.path),
      }
    case 'hostopProgress':
      if (typeof m.opId !== 'string' || typeof m.sessionId !== 'string') return null
      return { type: 'hostopProgress', sessionId: m.sessionId, opId: m.opId, done: num(m.done), total: num(m.total) }
    case 'hostopDone':
      if (typeof m.opId !== 'string' || typeof m.sessionId !== 'string') return null
      return {
        type: 'hostopDone',
        sessionId: m.sessionId,
        opId: m.opId,
        status: m.status === 'error' ? 'error' : 'ok',
        message: str(m.message),
      }
    case 'taskTool': {
      if (typeof m.taskId !== 'string' || typeof m.callId !== 'string') return null
      const st = ['running', 'ok', 'error', 'denied'].includes(m.status as string) ? m.status : 'error'
      return {
        type: 'taskTool',
        taskId: m.taskId,
        callId: m.callId,
        name: str(m.name),
        status: st as TaskToolEvent['status'],
        message: str(m.message),
      }
    }
    case 'taskApproval': {
      if (typeof m.taskId !== 'string' || typeof m.callId !== 'string') return null
      const st = ['pending', 'approved', 'denied', 'expired'].includes(m.status as string) ? m.status : 'expired'
      return {
        type: 'taskApproval',
        taskId: m.taskId,
        callId: m.callId,
        name: str(m.name),
        input: m.input ?? null,
        status: st as TaskApprovalEvent['status'],
      }
    }
    case 'taskSummary':
      if (typeof m.taskId !== 'string') return null
      return { type: 'taskSummary', taskId: m.taskId, summary: str(m.summary) }
    case 'taskState': {
      if (typeof m.taskId !== 'string') return null
      if (!['working', 'blocked', 'done'].includes(m.state as string)) return null
      return {
        type: 'taskState',
        taskId: m.taskId,
        state: m.state as TaskStateEvent['state'],
        summary: str(m.summary),
        question: str(m.question),
      }
    }
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
