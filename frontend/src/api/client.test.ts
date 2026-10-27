import { afterEach, describe, it, expect, vi } from 'vitest'
import { ApiRequestError, api, isAbort, isAlreadyRunning } from './client'

describe('host directory requests', () => {
  afterEach(() => vi.unstubAllGlobals())

  function stubFetch(status = 200, body: unknown = { path: '/', parent: null, directories: [] }) {
    const f = vi.fn().mockResolvedValue({
      status,
      ok: status < 400,
      statusText: '',
      text: async () => (status === 204 ? '' : JSON.stringify(body)),
    })
    vi.stubGlobal('fetch', f)
    return f
  }

  it.each([
    ['h 1', undefined, '/api/hosts/h%201/directories'],
    ['h1', '', '/api/hosts/h1/directories'],
    ['h1', '/srv/a b', '/api/hosts/h1/directories?path=%2Fsrv%2Fa%20b'],
  ])('hostDirectories(%s, %s) builds the URL', async (id, path, want) => {
    const f = stubFetch()
    await api.hostDirectories(id, path)
    expect(f.mock.calls[0][0]).toBe(want)
  })

  it('passes the abort signal through', async () => {
    const f = stubFetch()
    const ac = new AbortController()
    await api.hostDirectories('h1', '', { signal: ac.signal })
    expect(f.mock.calls[0][1].signal).toBe(ac.signal)
  })

  it('closeHostBrowse sends DELETE and swallows errors', async () => {
    const f = stubFetch(204)
    await api.closeHostBrowse('h1')
    expect(f.mock.calls[0][0]).toBe('/api/hosts/h1/directories')
    expect(f.mock.calls[0][1].method).toBe('DELETE')
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('offline')))
    await expect(api.closeHostBrowse('h1')).resolves.toBeUndefined()
  })

  it('a 409 host-key response exposes hostKeyDetails', async () => {
    stubFetch(409, {
      error: { code: 'host_key_unverified', message: 'm', keyType: 'ssh-ed25519', fingerprint: 'SHA256:x' },
    })
    const err = await api.hostDirectories('h1').catch((e) => e)
    expect(err.hostKeyDetails()).toEqual({ keyType: 'ssh-ed25519', fingerprint: 'SHA256:x' })
  })
})

describe('isAbort', () => {
  it('matches AbortError only', () => {
    expect(isAbort(new DOMException('x', 'AbortError'))).toBe(true)
    expect(isAbort(new Error('x'))).toBe(false)
    expect(isAbort(null)).toBe(false)
  })
})

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
