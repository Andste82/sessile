import { describe, expect, it } from 'vitest'
import {
  backoffSteps,
  closeSessionEnded,
  closeSessionUnavailable,
  exitedProbeDelay,
  planReconnect,
} from './reconnect'

describe('planReconnect', () => {
  it('backs off while the session is still running', () => {
    expect(planReconnect('connected', 1006, 0)).toEqual({
      status: 'disconnected',
      delayMs: backoffSteps[0],
    })
    expect(planReconnect('disconnected', 1006, 2)).toEqual({
      status: 'disconnected',
      delayMs: backoffSteps[2],
    })
  })

  it('holds at the longest step rather than growing without bound', () => {
    const last = backoffSteps[backoffSteps.length - 1]
    expect(planReconnect('disconnected', 1006, 99).delayMs).toBe(last)
  })

  // Issue #42: a client that sat on "session ended" had given up for good, so
  // when another browser restarted the session it stayed on a dead socket with
  // the banner up — offering to start a session that was already running.
  it('keeps asking after the server says the session is gone', () => {
    for (const code of [closeSessionEnded, closeSessionUnavailable]) {
      expect(planReconnect('connecting', code, 0)).toEqual({
        status: 'exited',
        delayMs: exitedProbeDelay,
      })
    }
  })

  it('keeps asking on any later drop while exited', () => {
    // The close code on a probe that is refused mid-handshake is not always
    // 4404 — what matters is that the session was already known to be gone.
    expect(planReconnect('exited', 1006, 0)).toEqual({
      status: 'exited',
      delayMs: exitedProbeDelay,
    })
    expect(planReconnect('exited', undefined, 7)).toEqual({
      status: 'exited',
      delayMs: exitedProbeDelay,
    })
  })

  // Probing at a flat interval is the point: a session can be restarted hours
  // after it stopped, and the client has to notice in seconds, not minutes.
  it('does not back off between probes', () => {
    const delays = [0, 1, 5, 50].map((n) => planReconnect('exited', undefined, n).delayMs)
    expect(new Set(delays).size).toBe(1)
    expect(delays[0]).toBe(exitedProbeDelay)
  })

  // The banner has to stay up across probes. Reporting a connection when the
  // socket opens would take it down every few seconds and put it back when the
  // server closed the upgrade it had just accepted.
  it('never reports anything but exited once the session is gone', () => {
    expect(planReconnect('exited', 1006, 0).status).toBe('exited')
    expect(planReconnect('connected', closeSessionUnavailable, 0).status).toBe('exited')
  })
})
