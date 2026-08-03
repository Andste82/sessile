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

// How often a client sitting on a stopped session asks whether it is back.
// Flat rather than backing off: a session can be restarted at any moment, hours
// in, and the answer must not take minutes to arrive by then.
export const exitedProbeDelay = 3000

export interface ReconnectPlan {
  /** What the UI should say while the next attempt is pending. */
  status: 'exited' | 'disconnected'
  /** How long to wait before that attempt. */
  delayMs: number
}

/**
 * planReconnect decides what to do after a socket closes.
 *
 * A stopped session is not a dead end: it comes back under the same id,
 * restarted from this browser or any other one, and this client has to end up
 * attached to it either way. It is told so over its own socket when it still
 * has one — but a client that arrived after the session stopped never had one
 * to be told over, and any drop in between puts it back in exactly that state.
 * So it keeps asking. Whatever else fails to reach it, the next attach that
 * succeeds takes the "session ended" banner down, because that is what an
 * attached frame means.
 *
 * The status stays "exited" across those attempts. The socket opening proves
 * nothing while the session is stopped — the server accepts the upgrade and
 * then closes it with 4404 — so announcing a connection there would flicker the
 * banner off and back on every few seconds.
 */
export function planReconnect(
  current: ConnStatus,
  code: number | undefined,
  attempt: number,
): ReconnectPlan {
  const sessionGone = code === closeSessionEnded || code === closeSessionUnavailable
  if (sessionGone || current === 'exited') {
    return { status: 'exited', delayMs: exitedProbeDelay }
  }
  return {
    status: 'disconnected',
    delayMs: backoffSteps[Math.min(attempt, backoffSteps.length - 1)],
  }
}
