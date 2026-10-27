import { describe, expect, it, vi } from 'vitest'
import type { DirectoriesResponse } from '@/api/types'
import { createHostBrowse, hostKeyOf, sshCreateBody, supportsStartDir } from './hostBrowse'

describe('sshCreateBody', () => {
  const common = { name: 'n', group: 'g' }
  it('omits directory when empty or blank', () => {
    expect(sshCreateBody(common, 'h', '')).toEqual({ name: 'n', group: 'g', target: 'ssh', hostId: 'h' })
    expect('directory' in sshCreateBody(common, 'h', '   ')).toBe(false)
  })
  it('sends the trimmed directory when set', () => {
    expect(sshCreateBody(common, 'h', ' /srv/app ').directory).toBe('/srv/app')
  })
})

const isAbort = (e: unknown) => (e as { name?: string })?.name === 'AbortError'

function deferred<T>() {
  let resolve!: (v: T) => void
  let reject!: (e: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

const home = (path = '/home/u'): DirectoriesResponse => ({ path, parent: '/home', directories: [] })

function hostKeyError() {
  return Object.assign(new Error('unknown key'), {
    hostKeyDetails: () => ({ keyType: 'ssh-ed25519', fingerprint: 'SHA256:x' }),
  })
}

describe('createHostBrowse', () => {
  it('connects to the home directory and becomes ready', async () => {
    const fetchDirs = vi.fn().mockResolvedValue(home())
    const b = createHostBrowse({ fetchDirs, closeBrowse: vi.fn(), isAbort })
    const p = b.start('h1')
    expect(b.state.status).toBe('connecting')
    await p
    expect(fetchDirs).toHaveBeenCalledWith('h1', '', expect.any(AbortSignal))
    expect(b.state).toMatchObject({ status: 'ready', path: '/home/u', hostId: 'h1' })
  })

  it('shows the error and recovers on Retry', async () => {
    const fetchDirs = vi
      .fn()
      .mockRejectedValueOnce(new Error('connection refused'))
      .mockResolvedValueOnce(home())
    const b = createHostBrowse({ fetchDirs, closeBrowse: vi.fn(), isAbort })
    await b.start('h1')
    expect(b.state).toMatchObject({ status: 'error', error: 'connection refused' })
    await b.retry()
    expect(b.state.status).toBe('ready')
    expect(fetchDirs).toHaveBeenCalledTimes(2)
  })

  it('ignores a late response after the host changed, aborts it, and closes the old host', async () => {
    const first = deferred<DirectoriesResponse>()
    const signals: AbortSignal[] = []
    const fetchDirs = vi.fn((host: string, _p: string, s: AbortSignal) => {
      signals.push(s)
      return host === 'h1' ? first.promise : Promise.resolve(home('/srv'))
    })
    const closeBrowse = vi.fn().mockResolvedValue(undefined)
    const b = createHostBrowse({ fetchDirs, closeBrowse, isAbort })
    const p1 = b.start('h1')
    await b.start('h2')
    expect(signals[0].aborted).toBe(true)
    expect(closeBrowse).toHaveBeenCalledWith('h1')
    first.resolve(home('/stale'))
    await p1
    expect(b.state).toMatchObject({ status: 'ready', hostId: 'h2', path: '/srv' })
  })

  it('does not surface an abort rejection as an error', async () => {
    const first = deferred<DirectoriesResponse>()
    const fetchDirs = vi.fn().mockReturnValueOnce(first.promise)
    const b = createHostBrowse({ fetchDirs, closeBrowse: vi.fn().mockResolvedValue(undefined), isAbort })
    const p1 = b.start('h1')
    b.stop()
    first.reject(Object.assign(new Error('aborted'), { name: 'AbortError' }))
    await p1
    expect(b.state.status).toBe('idle')
  })

  it('closes the server-side connection exactly once on stop', async () => {
    const closeBrowse = vi.fn().mockResolvedValue(undefined)
    const b = createHostBrowse({ fetchDirs: vi.fn().mockResolvedValue(home()), closeBrowse, isAbort })
    await b.start('h1')
    b.stop()
    b.stop()
    expect(closeBrowse).toHaveBeenCalledTimes(1)
    expect(closeBrowse).toHaveBeenCalledWith('h1')
  })

  it('never calls close when nothing was started', () => {
    const closeBrowse = vi.fn()
    createHostBrowse({ fetchDirs: vi.fn(), closeBrowse, isAbort }).stop()
    expect(closeBrowse).not.toHaveBeenCalled()
  })

  it('moves to needsTrust on a host-key error and re-runs after trust', async () => {
    const fetchDirs = vi.fn().mockRejectedValueOnce(hostKeyError()).mockResolvedValueOnce(home())
    const b = createHostBrowse({ fetchDirs, closeBrowse: vi.fn(), isAbort })
    await b.start('h1')
    expect(b.state.status).toBe('needsTrust')
    expect(b.state.hostKey?.fingerprint).toBe('SHA256:x')
    await b.retry()
    expect(b.state.status).toBe('ready')
  })

  it('dismissing the trust dialog leaves an error with Retry available', async () => {
    const b = createHostBrowse({
      fetchDirs: vi.fn().mockRejectedValue(hostKeyError()),
      closeBrowse: vi.fn(),
      isAbort,
    })
    await b.start('h1')
    b.dismissTrust()
    expect(b.state.status).toBe('error')
    expect(b.state.hostKey).toBeNull()
  })

  it('a host-key error reported from a later navigation needs trust too', async () => {
    const b = createHostBrowse({ fetchDirs: vi.fn().mockResolvedValue(home()), closeBrowse: vi.fn(), isAbort })
    await b.start('h1')
    b.reportHostKey(hostKeyError())
    expect(b.state.status).toBe('needsTrust')
  })

  it('load() shares the lifecycle signal and fails when not connected', async () => {
    const fetchDirs = vi.fn().mockResolvedValue(home())
    const b = createHostBrowse({ fetchDirs, closeBrowse: vi.fn(), isAbort })
    await expect(b.load('/x')).rejects.toThrow()
    await b.start('h1')
    await b.load('/x')
    expect(fetchDirs).toHaveBeenLastCalledWith('h1', '/x', expect.any(AbortSignal))
  })
})

describe('hostKeyOf', () => {
  it('is null for plain errors and non-objects', () => {
    expect(hostKeyOf(new Error('x'))).toBeNull()
    expect(hostKeyOf('x')).toBeNull()
    expect(hostKeyOf(null)).toBeNull()
  })
})

describe('supportsStartDir', () => {
  it.each([
    [{ targetOS: 'linux', terminalType: '' }, true],
    [{ targetOS: '', terminalType: 'bash' }, true],
    [{ targetOS: 'windows', terminalType: '' }, false],
    [{ targetOS: '', terminalType: 'cmd' }, false],
    [{ targetOS: 'other', terminalType: 'powershell' }, false],
  ])('%j', (host, want) => {
    expect(supportsStartDir(host)).toBe(want)
  })
})
