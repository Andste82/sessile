/** The state of a terminal's connection, as the UI shows it. */
export type ConnStatus = 'connecting' | 'connected' | 'exited' | 'disconnected'

// WS close codes that mean the session is gone, not the connection (§5): 4404
// when attaching to a missing or stopped session, 4000 when one was deleted
// under us. A shell that merely exits no longer closes the connection — the
// session can come back under the same id, and that connection is where the
// server says so.
export const closeSessionEnded = 4000
export const closeSessionUnavailable = 4404

// Backoff for a connection that dropped with the session still running.
export const backoffSteps = [1000, 2000, 4000, 8000, 15000]

export interface ReconnectPlan {
  /** What the UI should say from here on. */
  status: 'exited' | 'disconnected'
  /** How long to wait before trying again, or null to stop trying. */
  delayMs: number | null
}

/**
 * planReconnect decides what to do after a socket closes.
 *
 * A dropped connection under a live session is retried with backoff. A session
 * the server says is gone is not retried at all: reconnecting would only earn
 * another 4404, and this client does not need to ask. It is told — the server
 * hands a stopped session's clients to the shell that replaces it, so an
 * attached frame arrives on the socket it already has; and the session list is
 * polled app-wide, so a terminal whose socket did not survive is reconnected by
 * TerminalPage when the list says the session is running again. Two paths that
 * already exist and cost nothing extra are enough.
 */
export function planReconnect(
  current: ConnStatus,
  code: number | undefined,
  attempt: number,
): ReconnectPlan {
  const sessionGone = code === closeSessionEnded || code === closeSessionUnavailable
  if (sessionGone || current === 'exited') {
    return { status: 'exited', delayMs: null }
  }
  return {
    status: 'disconnected',
    delayMs: backoffSteps[Math.min(attempt, backoffSteps.length - 1)],
  }
}
