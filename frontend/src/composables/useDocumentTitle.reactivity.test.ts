import { describe, expect, it, beforeEach, vi } from 'vitest'
import { effectScope, nextTick, reactive } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import type { Session } from '@/api/types'

// The route App.vue's copy of useRoute() hands back: one reactive object whose
// fields the router rewrites in place on every navigation.
const route = reactive<{
  name: string
  params: Record<string, string>
  meta: Record<string, unknown>
}>({ name: 'dashboard', params: {}, meta: { title: 'Sessions' } })

vi.mock('vue-router', () => ({ useRoute: () => route }))

// The test environment has no DOM; the composable only ever writes one field.
;(globalThis as { document?: unknown }).document = { title: '' }

const { useDocumentTitle, setActiveSessionName } = await import('./useDocumentTitle')
const { useSessionsStore } = await import('@/stores/sessions')

function session(over: Partial<Session> = {}): Session {
  return {
    id: 'a',
    name: 'build-server',
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

function navigate(name: string, id?: string, title?: string) {
  route.name = name
  route.params = id ? { id } : {}
  route.meta = { title }
}

describe('useDocumentTitle in an app', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    navigate('dashboard', undefined, 'Sessions')
    setActiveSessionName(null)
    document.title = ''
  })

  // The terminal page holds the session whether it came from the list or from
  // fetching the id directly, so the title must not need the list at all. A
  // list that has not been polled yet — or whose request failed — used to leave
  // every terminal called "Sessile — Terminal".
  it('names the session from the page even with an empty list', async () => {
    const store = useSessionsStore()
    const scope = effectScope()
    scope.run(() => useDocumentTitle())

    navigate('terminal', 'a', 'Terminal')
    setActiveSessionName('build-server')
    await nextTick()

    expect(store.sessions).toHaveLength(0)
    expect(document.title).toBe('Sessile • build-server')

    scope.stop()
  })

  it('takes the page over the list when they disagree', async () => {
    const store = useSessionsStore()
    store.sessions = [session({ id: 'a', name: 'stale' })]
    const scope = effectScope()
    scope.run(() => useDocumentTitle())

    navigate('terminal', 'a', 'Terminal')
    setActiveSessionName('fresh')
    await nextTick()
    expect(document.title).toBe('Sessile • fresh')

    scope.stop()
  })

  it('falls back to the list when the page has nothing yet', async () => {
    const store = useSessionsStore()
    store.sessions = [session({ id: 'a' })]
    const scope = effectScope()
    scope.run(() => useDocumentTitle())

    navigate('terminal', 'a', 'Terminal')
    await nextTick()
    expect(document.title).toBe('Sessile • build-server')

    scope.stop()
  })

  // The page clears its name on the way out; nothing after it should inherit a
  // session name from a terminal that is no longer on screen.
  it('drops the page name when the terminal goes away', async () => {
    const scope = effectScope()
    scope.run(() => useDocumentTitle())

    navigate('terminal', 'a', 'Terminal')
    setActiveSessionName('build-server')
    await nextTick()
    expect(document.title).toBe('Sessile • build-server')

    setActiveSessionName(null)
    navigate('settings', undefined, 'Settings')
    await nextTick()
    expect(document.title).toBe('Sessile — Settings')

    scope.stop()
  })

  it('names the session once the list arrives', async () => {
    const store = useSessionsStore()
    const scope = effectScope()
    scope.run(() => useDocumentTitle())

    // Opening a terminal before the list has loaded: the name is not known yet.
    navigate('terminal', 'a', 'Terminal')
    await nextTick()
    expect(document.title).toBe('Sessile — Terminal')

    // …and the poll that fills the list has to correct it, with no navigation.
    store.sessions = [session({ id: 'a' })]
    await nextTick()
    expect(document.title).toBe('Sessile • build-server')

    scope.stop()
  })

  it('follows a switch between tabs', async () => {
    const store = useSessionsStore()
    store.sessions = [session({ id: 'a' }), session({ id: 'b', name: 'logs' })]
    const scope = effectScope()
    scope.run(() => useDocumentTitle())

    navigate('terminal', 'a', 'Terminal')
    await nextTick()
    expect(document.title).toBe('Sessile • build-server')

    navigate('terminal', 'b', 'Terminal')
    await nextTick()
    expect(document.title).toBe('Sessile • logs')

    scope.stop()
  })

  it('follows a rename of the session on screen', async () => {
    const store = useSessionsStore()
    store.sessions = [session({ id: 'a' })]
    const scope = effectScope()
    scope.run(() => useDocumentTitle())

    navigate('terminal', 'a', 'Terminal')
    await nextTick()
    expect(document.title).toBe('Sessile • build-server')

    store.sessions = [session({ id: 'a', name: 'renamed' })]
    await nextTick()
    expect(document.title).toBe('Sessile • renamed')

    scope.stop()
  })

  it('goes back to the route title when leaving the terminal', async () => {
    const store = useSessionsStore()
    store.sessions = [session({ id: 'a' })]
    const scope = effectScope()
    scope.run(() => useDocumentTitle())

    navigate('terminal', 'a', 'Terminal')
    await nextTick()
    expect(document.title).toBe('Sessile • build-server')

    navigate('dashboard', undefined, 'Sessions')
    await nextTick()
    expect(document.title).toBe('Sessile — Sessions')

    scope.stop()
  })
})
