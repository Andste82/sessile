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
}>({ name: 'dashboard', params: {}, meta: { title: 'Dashboard' } })

vi.mock('vue-router', () => ({ useRoute: () => route }))

// The test environment has no DOM; the composable only ever writes one field.
;(globalThis as { document?: unknown }).document = { title: '' }

const { useDocumentTitle } = await import('./useDocumentTitle')
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
    activity: 'idle',
    activitySince: '2026-08-03T10:00:00Z',
    command: 'bash',
    cwd: '.',
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
    navigate('dashboard', undefined, 'Dashboard')
    document.title = ''
  })

  it('names the session once the list arrives', async () => {
    const store = useSessionsStore()
    const scope = effectScope()
    scope.run(() => useDocumentTitle())

    // Opening a terminal before the list has loaded: the name is not known yet.
    navigate('terminal', 'a', 'Terminal')
    await nextTick()
    expect(document.title).toBe('sessile • Terminal')

    // …and the poll that fills the list has to correct it, with no navigation.
    store.sessions = [session({ id: 'a' })]
    await nextTick()
    expect(document.title).toBe('sessile • build-server')

    scope.stop()
  })

  it('follows a switch between tabs', async () => {
    const store = useSessionsStore()
    store.sessions = [session({ id: 'a' }), session({ id: 'b', name: 'logs' })]
    const scope = effectScope()
    scope.run(() => useDocumentTitle())

    navigate('terminal', 'a', 'Terminal')
    await nextTick()
    expect(document.title).toBe('sessile • build-server')

    navigate('terminal', 'b', 'Terminal')
    await nextTick()
    expect(document.title).toBe('sessile • logs')

    scope.stop()
  })

  it('follows a rename of the session on screen', async () => {
    const store = useSessionsStore()
    store.sessions = [session({ id: 'a' })]
    const scope = effectScope()
    scope.run(() => useDocumentTitle())

    navigate('terminal', 'a', 'Terminal')
    await nextTick()
    expect(document.title).toBe('sessile • build-server')

    store.sessions = [session({ id: 'a', name: 'renamed' })]
    await nextTick()
    expect(document.title).toBe('sessile • renamed')

    scope.stop()
  })

  it('goes back to the route title when leaving the terminal', async () => {
    const store = useSessionsStore()
    store.sessions = [session({ id: 'a' })]
    const scope = effectScope()
    scope.run(() => useDocumentTitle())

    navigate('terminal', 'a', 'Terminal')
    await nextTick()
    expect(document.title).toBe('sessile • build-server')

    navigate('dashboard', undefined, 'Dashboard')
    await nextTick()
    expect(document.title).toBe('sessile • Dashboard')

    scope.stop()
  })
})
