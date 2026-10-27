import { describe, it, expect, beforeEach, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { ApiRequestError } from '@/api/client'
import { useEnvDefaultsStore } from './envDefaults'

vi.mock('@/api/client', async () => {
  const actual = await vi.importActual<typeof import('@/api/client')>('@/api/client')
  return { ...actual, api: { getEnvDefaults: vi.fn(), putEnvDefaults: vi.fn() } }
})

const { api } = await import('@/api/client')
const getMock = vi.mocked(api.getEnvDefaults)
const putMock = vi.mocked(api.putEnvDefaults)

describe('envDefaults store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('loads defaults into rows; secrets carry no value', async () => {
    getMock.mockResolvedValue({
      env: [
        { name: 'A', value: '1', secret: false, isSet: true },
        { name: 'TOKEN', secret: true, isSet: true },
      ],
    })
    const s = useEnvDefaultsStore()
    await s.load()
    expect(s.loaded).toBe(true)
    expect(s.rows.map((r) => r.name)).toEqual(['A', 'TOKEN'])
    expect(s.rows[1].secret).toBe(true)
    expect(s.rows[1].value).toBe('')
    expect(s.rows[1].isSet).toBe(true)
  })

  it('loads rows as own (editable) and keeps them own after save', async () => {
    getMock.mockResolvedValue({
      env: [
        { name: 'A', value: '1', secret: false, isSet: true },
        { name: 'TOKEN', secret: true, isSet: true },
      ],
    })
    putMock.mockResolvedValue({
      env: [{ name: 'A', value: '2', secret: false, isSet: true }],
    })
    const s = useEnvDefaultsStore()
    await s.load()
    expect(s.rows.every((r) => r.origin === 'own')).toBe(true)
    expect(s.rows[0].value).toBe('1')
    await s.save()
    expect(s.rows.every((r) => r.origin === 'own')).toBe(true)
  })

  it('records a load failure instead of rejecting', async () => {
    getMock.mockRejectedValue(new ApiRequestError(500, 'internal', 'boom'))
    const s = useEnvDefaultsStore()
    await expect(s.load()).resolves.toBeUndefined()
    expect(s.loadError).toBe('boom')
  })

  it('saves untouched secrets as keep (no value) and reports saved', async () => {
    getMock.mockResolvedValue({
      env: [{ name: 'TOKEN', secret: true, isSet: true }],
    })
    putMock.mockResolvedValue({ env: [{ name: 'TOKEN', secret: true, isSet: true }] })
    const s = useEnvDefaultsStore()
    await s.load()
    await s.save()
    expect(putMock).toHaveBeenCalledTimes(1)
    const sent = putMock.mock.calls[0][0]
    expect(sent).toHaveLength(1)
    expect(sent[0].name).toBe('TOKEN')
    expect(sent[0].secret).toBe(true)
    expect(sent[0].value).toBeUndefined()
    expect(s.saved).toBe(true)
    expect(s.saveError).toBeNull()
  })

  it('does not call the server when structural validation fails', async () => {
    getMock.mockResolvedValue({ env: [] })
    const s = useEnvDefaultsStore()
    await s.load()
    s.setRows([
      { key: 'k', name: '1bad', value: 'x', secret: false, origin: 'own', isSet: false, removed: false },
    ])
    await s.save()
    expect(putMock).not.toHaveBeenCalled()
    expect(s.saveError).toBeTruthy()
  })

  it('surfaces the server error on save', async () => {
    getMock.mockResolvedValue({ env: [] })
    putMock.mockRejectedValue(new ApiRequestError(400, 'validation', 'reserved name TERM'))
    const s = useEnvDefaultsStore()
    await s.load()
    s.setRows([
      { key: 'k', name: 'TERM', value: 'x', secret: false, origin: 'own', isSet: false, removed: false },
    ])
    await s.save()
    expect(s.saveError).toBe('reserved name TERM')
    expect(s.saved).toBe(false)
  })

  it('clear drops all rows', async () => {
    getMock.mockResolvedValue({ env: [{ name: 'A', value: '1', secret: false, isSet: true }] })
    const s = useEnvDefaultsStore()
    await s.load()
    s.clear()
    expect(s.rows).toEqual([])
    expect(s.loaded).toBe(false)
  })
})
