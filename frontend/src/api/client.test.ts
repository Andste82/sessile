import { describe, it, expect } from 'vitest'
import { ApiRequestError, isAlreadyRunning } from './client'

describe('isAlreadyRunning', () => {
  // With several browsers on one session, whoever clicks "restart" second loses
  // the race. The backend narrows that conflict to its own code so the loser can
  // reconnect instead of showing "session is already running" — an error about
  // exactly the state it was asking for.
  it('recognises the restart race', () => {
    const e = new ApiRequestError(409, 'already_running', 'session is already running')
    expect(isAlreadyRunning(e)).toBe(true)
  })

  // The other 409s are real refusals: a delete during a restart, an attach to a
  // stopped session. They still have to reach the user.
  it('does not swallow the other conflicts', () => {
    const e = new ApiRequestError(409, 'conflict', 'session is restarting')
    expect(isAlreadyRunning(e)).toBe(false)
  })

  it('is false for anything that is not an API error', () => {
    expect(isAlreadyRunning(new TypeError('Failed to fetch'))).toBe(false)
    expect(isAlreadyRunning('already_running')).toBe(false)
    expect(isAlreadyRunning(null)).toBe(false)
    expect(isAlreadyRunning(undefined)).toBe(false)
  })
})
