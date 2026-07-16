// WebSocket control-message codec (PROJECT_PLAN.md §5).
// Binary frames carry raw terminal bytes; text frames carry these JSON
// control messages. This module handles only the text/control layer.

export interface AttachedControl {
  type: 'attached'
  sessionId: string
  replayBytes: number
}
export interface ExitControl {
  type: 'exit'
}
export interface ErrorControl {
  type: 'error'
  message: string
}

export type ServerControl = AttachedControl | ExitControl | ErrorControl

/** Parse a server→client text control frame, or null if it is not valid. */
export function parseControl(data: string): ServerControl | null {
  let msg: unknown
  try {
    msg = JSON.parse(data)
  } catch {
    return null
  }
  if (typeof msg !== 'object' || msg === null || !('type' in msg)) return null
  const m = msg as Record<string, unknown>
  switch (m.type) {
    case 'attached':
      if (typeof m.sessionId === 'string' && typeof m.replayBytes === 'number') {
        return { type: 'attached', sessionId: m.sessionId, replayBytes: m.replayBytes }
      }
      return null
    case 'exit':
      return { type: 'exit' }
    case 'error':
      return { type: 'error', message: typeof m.message === 'string' ? m.message : '' }
    default:
      return null
  }
}

/** Encode a client→server resize control frame as a JSON string. */
export function encodeResize(cols: number, rows: number): string {
  return JSON.stringify({ type: 'resize', cols, rows })
}

/** Build the WebSocket URL for a session from the current page origin. */
export function sessionWsURL(id: string): string {
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${proto}//${window.location.host}/ws/sessions/${encodeURIComponent(id)}`
}
