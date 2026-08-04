import { describe, expect, it } from 'vitest'
import {
  backoffSteps,
  closeSessionEnded,
  closeSessionUnavailable,
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

  // A session the server says is gone is not retried: another attempt only
  // earns another 4404. Coming back is not this socket's job — the server pushes
  // an attached frame onto the clients of a session it restarts, and the polled
  // list reconnects a terminal whose socket did not survive.
  it('stops retrying once the server says the session is gone', () => {
    for (const code of [closeSessionEnded, closeSessionUnavailable]) {
      expect(planReconnect('connecting', code, 0)).toEqual({
        status: 'exited',
        delayMs: null,
      })
    }
  })

  it('stays stopped on any later drop while exited', () => {
    expect(planReconnect('exited', 1006, 0).delayMs).toBeNull()
    expect(planReconnect('exited', undefined, 7).delayMs).toBeNull()
  })

  // Whatever else happens, a session the server has declared gone reads as
  // ended — never as a connection in progress.
  it('never reports anything but exited once the session is gone', () => {
    expect(planReconnect('exited', 1006, 0).status).toBe('exited')
    expect(planReconnect('connected', closeSessionUnavailable, 0).status).toBe('exited')
  })
})
