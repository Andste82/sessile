import { describe, it, expect, beforeEach, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { ApiRequestError } from '@/api/client'
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
