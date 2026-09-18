import { describe, it, expect, afterEach, beforeEach, vi } from 'vitest'
import { nextTick } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { ApiRequestError } from '@/api/client'
import type { Session } from '@/api/types'
import { parseOpenTabs, useSessionsStore } from './sessions'

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

    configMock.mockResolvedValue({ shells: ['bash'], version: '1.2.3', allowLocalHost: true, allowAgentScripts: true })
    await store.fetchConfig()

    expect(store.config).toEqual({ shells: ['bash'], version: '1.2.3', allowLocalHost: true })
    expect(store.error).toBeNull()
  })
})

function session(over: Partial<Session> = {}): Session {
  return {
    id: 'a',
    name: 'a',
    targetType: 'local',
    directory: '.',
    shell: 'bash',
    hostId: '',
    hostDisplayName: '',
    taskId: null,
    group: '',
    status: 'running',
    pid: 42,
    created: '2026-08-03T10:00:00Z',
    lastActivity: '2026-08-03T10:00:00Z',
    rows: 24,
    cols: 80,
    clientCount: 1,
    command: 'bash',
    cwd: '.',
    title: '',
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

// The event channel (§5.1) reaches the store through exactly one function, so
// this is where its behaviour is worth asserting — the composable around it is
// socket plumbing.
describe('applyEvent', () => {
  it('replaces the whole list on a snapshot', () => {
    const store = useSessionsStore()
    store.sessions = [session({ id: 'stale' })]

    store.applyEvent({ type: 'sessions', sessions: [session({ id: 'a' }), session({ id: 'b' })] })

    expect(store.sessions.map((s) => s.id)).toEqual(['a', 'b'])
  })

  it('drops a session the snapshot no longer lists', () => {
    const store = useSessionsStore()
    store.sessions = [session({ id: 'gone-elsewhere' })]

    store.applyEvent({ type: 'sessions', sessions: [] })

    expect(store.sessions).toEqual([])
  })

  it('updates a session in place without reordering the list', () => {
    const store = useSessionsStore()
    store.sessions = [session({ id: 'a' }), session({ id: 'b' })]

    store.applyEvent({ type: 'session', session: session({ id: 'b', command: 'htop' }) })

    expect(store.sessions.map((s) => s.id)).toEqual(['a', 'b'])
    expect(store.sessions[1].command).toBe('htop')
  })

  it('inserts a session created by another client', () => {
    const store = useSessionsStore()
    store.sessions = [session({ id: 'a' })]

    store.applyEvent({ type: 'session', session: session({ id: 'new' }) })

    expect(store.sessions.map((s) => s.id)).toContain('new')
  })

  it('removes a deleted session and closes its tab', () => {
    const store = useSessionsStore()
    store.sessions = [session({ id: 'a' }), session({ id: 'b' })]
    store.openTab('b')

    store.applyEvent({ type: 'sessionGone', sessionId: 'b' })

    expect(store.sessions.map((s) => s.id)).toEqual(['a'])
    expect(store.openTabIds).not.toContain('b')
  })

  // A failed poll sets an error and greys everything out. The channel coming
  // back is the evidence that the backend is reachable again, so a stale error
  // must not survive it.
  it('clears an earlier error', () => {
    const store = useSessionsStore()
    store.error = 'backend unreachable'

    store.applyEvent({ type: 'sessions', sessions: [session()] })

    expect(store.error).toBeNull()
  })
})

describe('groupNames', () => {
  // There is no group entity: this list *is* the set of groups (§4.11), which
  // is why a group disappears on its own once its last session is gone.
  it('lists the distinct groups in use, sorted, ignoring ungrouped sessions', () => {
    const store = useSessionsStore()
    store.sessions = [
      session({ id: 'a', group: 'Staging' }),
      session({ id: 'b', group: 'Production' }),
      session({ id: 'c', group: 'Production' }),
      session({ id: 'd', group: '' }),
    ]
    expect(store.groupNames).toEqual(['Production', 'Staging'])
  })

  it('is empty when nothing is grouped', () => {
    const store = useSessionsStore()
    store.sessions = [session({ id: 'a' }), session({ id: 'b' })]
    expect(store.groupNames).toEqual([])
  })
})

describe('grouped', () => {
  // Ungrouped first and nameless, named groups alphabetically after it. The
  // empty name is what the views read as "render these without a heading".
  it('puts the ungrouped block first, then named groups in order', () => {
    const store = useSessionsStore()
    store.sessions = [
      session({ id: 'a', group: 'Staging' }),
      session({ id: 'b', group: '' }),
      session({ id: 'c', group: 'Production' }),
      session({ id: 'd', group: 'Staging' }),
    ]
    expect(store.grouped.map((g) => [g.name, g.sessions.map((s) => s.id)])).toEqual([
      ['', ['b']],
      ['Production', ['c']],
      ['Staging', ['a', 'd']],
    ])
  })

  // Someone who never uses groups must get exactly one block and no heading.
  it('is a single unnamed block when nothing is grouped', () => {
    const store = useSessionsStore()
    store.sessions = [session({ id: 'a' }), session({ id: 'b' })]
    expect(store.grouped).toHaveLength(1)
    expect(store.grouped[0].name).toBe('')
  })

  // No ungrouped block at all when every session is filed somewhere — an
  // empty first block would render as a gap above the first heading.
  it('omits the ungrouped block when it would be empty', () => {
    const store = useSessionsStore()
    store.sessions = [session({ id: 'a', group: 'Production' })]
    expect(store.grouped.map((g) => g.name)).toEqual(['Production'])
  })
})

describe('parseOpenTabs', () => {
  it.each([
    ['["a","b"]', ['a', 'b']],
    // Not the array we write, in every shape storage can hand back.
    [null, []],
    ['', []],
    ['not json', []],
    ['{"a":1}', []],
    ['"a"', []],
    // Mixed contents: keep the usable ids rather than dropping the whole set.
    ['["a",7,null,"","b"]', ['a', 'b']],
  ])('reads %j as %j', (stored, want) => {
    expect(parseOpenTabs(stored)).toEqual(want)
  })
})

// Tabs are restored from localStorage, like the terminal font size, because a
// reload otherwise left only the session the router mounted. What makes that
// safe is the session list: a stored id the server does not list — deleted
// since, or belonging to whoever used this browser before — never becomes a
// tab.
describe('open tabs across a reload', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  afterEach(() => {
    delete (globalThis as { localStorage?: unknown }).localStorage
  })

  function withStoredTabs(ids: string[]) {
    const data: Record<string, string> = { 'sessile.openTabs': JSON.stringify(ids) }
    const storage = {
      getItem: vi.fn((k: string) => (k in data ? data[k] : null)),
      setItem: vi.fn((k: string, v: string) => {
        data[k] = v
      }),
    }
    ;(globalThis as { localStorage?: unknown }).localStorage = storage
    return { storage, data }
  }

  it('restores the stored tabs in order once the list arrives', async () => {
    withStoredTabs(['b', 'a'])
    listSessionsMock.mockResolvedValue([session({ id: 'a' }), session({ id: 'b' })])
    const store = useSessionsStore()

    // Nothing before the snapshot: an id is not a tab until the server has
    // confirmed the session behind it.
    expect(store.openTabIds).toEqual([])

    await store.fetchSessions()

    expect(store.openTabIds).toEqual(['b', 'a'])
  })

  it('drops a stored tab whose session is gone', async () => {
    withStoredTabs(['a', 'deleted'])
    listSessionsMock.mockResolvedValue([session({ id: 'a' })])
    const store = useSessionsStore()

    await store.fetchSessions()

    expect(store.openTabIds).toEqual(['a'])
  })

  // The terminal page opens its own tab on mount, before the list it fired off
  // has come back. That tab is the one the user is looking at, so it survives
  // the merge — a deep link into a session that was not in the stored set
  // included.
  it('keeps a tab opened before the list arrived', async () => {
    withStoredTabs(['a'])
    listSessionsMock.mockResolvedValue([session({ id: 'a' }), session({ id: 'deep' })])
    const store = useSessionsStore()
    store.openTab('deep')

    await store.fetchSessions()

    expect(store.openTabIds).toEqual(['a', 'deep'])
  })

  it('restores nothing when storage cannot be read', async () => {
    ;(globalThis as { localStorage?: unknown }).localStorage = {
      getItem: vi.fn(() => {
        throw new Error('cookies blocked')
      }),
      setItem: vi.fn(),
    }
    listSessionsMock.mockResolvedValue([session({ id: 'a' })])
    const store = useSessionsStore()

    await store.fetchSessions()

    expect(store.openTabIds).toEqual([])
  })

  it('writes the tab set as it changes', async () => {
    const { storage, data } = withStoredTabs([])
    listSessionsMock.mockResolvedValue([session({ id: 'a' }), session({ id: 'b' })])
    const store = useSessionsStore()
    await store.fetchSessions()

    store.openTab('a')
    store.openTab('b')
    await nextTick()
    expect(data['sessile.openTabs']).toBe('["a","b"]')

    store.closeTab('a')
    await nextTick()
    expect(data['sessile.openTabs']).toBe('["b"]')
    expect(storage.setItem).toHaveBeenCalled()
  })

  // Restoring happens once. A later snapshot is still authoritative about
  // which tabs are real: a session deleted from another client loses its tab
  // whether the event channel or the next poll notices first.
  it('prunes a tab whose session disappears from a later snapshot', async () => {
    withStoredTabs([])
    listSessionsMock.mockResolvedValue([session({ id: 'a' }), session({ id: 'b' })])
    const store = useSessionsStore()
    await store.fetchSessions()
    store.openTab('a')
    store.openTab('b')

    store.applyEvent({ type: 'sessions', sessions: [session({ id: 'a' })] })

    expect(store.openTabIds).toEqual(['a'])
  })

  // A poll that changes nothing must not keep rewriting storage: the pruned
  // array is only assigned when it is actually shorter.
  it('leaves the tab set untouched by a snapshot that changes nothing', async () => {
    withStoredTabs(['a'])
    listSessionsMock.mockResolvedValue([session({ id: 'a' })])
    const store = useSessionsStore()
    await store.fetchSessions()
    await nextTick()
    const before = store.openTabIds

    await store.refreshSessions()
    await nextTick()

    expect(store.openTabIds).toBe(before)
  })
})
