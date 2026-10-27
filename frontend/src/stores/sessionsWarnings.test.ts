import { describe, it, expect, beforeEach, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import type { Session } from '@/api/types'
import { useSessionsStore } from './sessions'
import { useToastsStore } from './toasts'

vi.mock('@/api/client', async () => {
  const actual = await vi.importActual<typeof import('@/api/client')>('@/api/client')
  return {
    ...actual,
    api: { createSession: vi.fn(), restartSession: vi.fn() },
  }
})

const { api } = await import('@/api/client')

function session(over: Partial<Session> = {}): Session {
  return {
    id: 'a',
    name: 'a',
    targetType: 'local',
    directory: '.',
    shell: 'bash',
    hostId: '',
    hostDisplayName: '',
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
    env: [],
    loadDotenv: false,
    ...over,
  }
}

describe('warnings from create and restart', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('create strips warnings from the stored session and toasts each one', async () => {
    vi.mocked(api.createSession).mockResolvedValue({
      ...session({ id: 'n' }),
      warnings: ['no .env file found', '.env line 3: reserved name TERM'],
    })
    const store = useSessionsStore()
    const created = await store.createSession({
      name: 'n',
      target: 'local',
      directory: '.',
      shell: 'bash',
      loadDotenv: true,
    })

    expect('warnings' in created).toBe(false)
    expect('warnings' in store.sessions[0]).toBe(false)
    const toasts = useToastsStore().toasts
    expect(toasts.map((t) => [t.message, t.kind])).toEqual([
      ['no .env file found', 'warning'],
      ['.env line 3: reserved name TERM', 'warning'],
    ])
  })

  it('restart strips warnings too and pushes none when empty', async () => {
    vi.mocked(api.restartSession).mockResolvedValue({ ...session({ id: 'a' }), warnings: [] })
    const store = useSessionsStore()
    store.sessions = [session({ id: 'a', status: 'stopped' })]
    const restarted = await store.restartSession('a')

    expect('warnings' in restarted).toBe(false)
    expect('warnings' in store.sessions[0]).toBe(false)
    expect(useToastsStore().toasts).toHaveLength(0)
  })

  it('tolerates a response without warnings', async () => {
    vi.mocked(api.restartSession).mockResolvedValue(session({ id: 'a' }) as never)
    const store = useSessionsStore()
    await expect(store.restartSession('a')).resolves.toBeTruthy()
  })
})
