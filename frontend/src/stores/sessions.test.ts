import { describe, it, expect, beforeEach, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { ApiRequestError } from '@/api/client'
import type { Session } from '@/api/types'
import { useSessionsStore } from './sessions'

vi.mock('@/api/client', async () => {
  const actual = await vi.importActual<typeof import('@/api/client')>('@/api/client')
  return {
    ...actual,
    api: { config: vi.fn(), listSessions: vi.fn() },
  }
})

const { api } = await import('@/api/client')
const configMock = vi.mocked(api.config)
const listSessionsMock = vi.mocked(api.listSessions)

describe('fetchConfig', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  // fetchConfig used to have no try/catch, unlike every other action. Two of its
  // three callers fire it without awaiting, so the rejection went nowhere: the
  // shell list stayed empty, the New session dialog offered nothing to pick, and
  // the Settings page said "Loading…" indefinitely — with nothing on screen
  // saying the request had failed.
  it('records a failure instead of rejecting', async () => {
    configMock.mockRejectedValue(new ApiRequestError(500, 'internal', 'config exploded'))
    const store = useSessionsStore()

    await expect(store.fetchConfig()).resolves.toBeUndefined()

    expect(store.error).toBe('config exploded')
    expect(store.config).toBeNull()
  })

  it('reports a non-Error rejection too', async () => {
    configMock.mockRejectedValue('bare string')
    const store = useSessionsStore()

    await store.fetchConfig()

    expect(store.error).toBe('bare string')
  })

  it('stores the config and clears a previous error on success', async () => {
    const store = useSessionsStore()
    store.error = 'stale failure from an earlier attempt'

    configMock.mockResolvedValue({ root: '/srv', shells: ['bash'], version: '1.2.3' })
    await store.fetchConfig()

    expect(store.config).toEqual({ root: '/srv', shells: ['bash'], version: '1.2.3' })
    expect(store.error).toBeNull()
  })
})

function session(over: Partial<Session> = {}): Session {
  return {
    id: 'a',
    name: 'a',
    directory: '.',
    shell: 'bash',
    status: 'running',
    pid: 42,
    created: '2026-08-03T10:00:00Z',
    lastActivity: '2026-08-03T10:00:00Z',
    rows: 24,
    cols: 80,
    clientCount: 1,
    ...over,
  }
}

describe('markStopped', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  // The list is only polled from the dashboard, so a session that ends while
  // its terminal is open has nothing else to correct its status dot.
  it('flips the session to stopped and drops its client count', () => {
    const store = useSessionsStore()
    store.sessions = [session({ id: 'a' }), session({ id: 'b' })]

    store.markStopped('a')

    expect(store.sessions[0]).toMatchObject({ id: 'a', status: 'stopped', clientCount: 0 })
    expect(store.sessions[1]).toMatchObject({ id: 'b', status: 'running', clientCount: 1 })
  })

  it('is a no-op for an id the list does not hold', () => {
    const store = useSessionsStore()
    store.sessions = [session({ id: 'a' })]

    store.markStopped('gone')

    expect(store.sessions).toEqual([session({ id: 'a' })])
  })
})

describe('markAllStopped', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  // A backend restart used to be noticed only by the terminal that happened to
  // be on screen, through its own socket closing. Every other session kept the
  // green dot from the last successful poll until it was clicked.
  it('flips every session to stopped', () => {
    const store = useSessionsStore()
    store.sessions = [
      session({ id: 'a' }),
      session({ id: 'b', status: 'stopped', clientCount: 0 }),
      session({ id: 'c', clientCount: 3 }),
    ]

    store.markAllStopped()

    expect(store.sessions.map((s) => s.status)).toEqual(['stopped', 'stopped', 'stopped'])
    expect(store.sessions.map((s) => s.clientCount)).toEqual([0, 0, 0])
  })

  // Rewriting the array on every failed poll would rerender the list — and
  // reset the dashboard's session rows — five times a second while a backend
  // stays down, with nothing to show for it.
  it('leaves the list untouched when nothing is running', () => {
    const store = useSessionsStore()
    const stopped = [session({ id: 'a', status: 'stopped', clientCount: 0 })]
    store.sessions = stopped
    const before = store.sessions

    store.markAllStopped()

    expect(store.sessions).toBe(before)
  })
})

describe('refreshSessions', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('replaces the list on success', async () => {
    listSessionsMock.mockResolvedValue([session({ id: 'b', name: 'fresh' })])
    const store = useSessionsStore()
    store.sessions = [session({ id: 'a' })]

    await store.refreshSessions()

    expect(store.sessions).toEqual([session({ id: 'b', name: 'fresh' })])
    expect(store.error).toBeNull()
  })

  // Nothing runs a shell but the backend, so an unreachable backend means no
  // session is running — whatever the last successful poll said.
  it('greys every session out when the backend cannot be reached', async () => {
    listSessionsMock.mockRejectedValue(new TypeError('Failed to fetch'))
    const store = useSessionsStore()
    store.sessions = [session({ id: 'a' }), session({ id: 'b' })]

    await store.refreshSessions()

    expect(store.sessions.map((s) => s.status)).toEqual(['stopped', 'stopped'])
    expect(store.error).toBe('Failed to fetch')
  })

  // The sessions themselves are still there — only their status is unknown, so
  // the rows stay put and the next successful poll fills the truth back in.
  it('keeps the sessions in the list when the refresh fails', async () => {
    listSessionsMock.mockRejectedValue(new ApiRequestError(502, 'internal', 'bad gateway'))
    const store = useSessionsStore()
    store.sessions = [session({ id: 'a' }), session({ id: 'b' })]

    await store.refreshSessions()

    expect(store.sessions.map((s) => s.id)).toEqual(['a', 'b'])
  })
})
