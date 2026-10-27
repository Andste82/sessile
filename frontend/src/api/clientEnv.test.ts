import { afterEach, describe, it, expect, vi } from 'vitest'
import { api } from './client'

describe('environment requests', () => {
  afterEach(() => vi.unstubAllGlobals())

  function stub(body: unknown) {
    const f = vi.fn().mockResolvedValue({
      status: 200,
      ok: true,
      statusText: '',
      text: async () => JSON.stringify(body),
    })
    vi.stubGlobal('fetch', f)
    return f
  }

  it('getEnvDefaults GETs /api/env-defaults', async () => {
    const f = stub({ env: [] })
    await expect(api.getEnvDefaults()).resolves.toEqual({ env: [] })
    expect(f.mock.calls[0][0]).toBe('/api/env-defaults')
    expect(f.mock.calls[0][1]?.method).toBeUndefined()
  })

  it('putEnvDefaults PUTs the entries under env, leaving a keep-secret value omitted', async () => {
    const f = stub({ env: [] })
    await api.putEnvDefaults([
      { name: 'A', value: '1', secret: false },
      { name: 'S', secret: true },
    ])
    expect(f.mock.calls[0][0]).toBe('/api/env-defaults')
    expect(f.mock.calls[0][1].method).toBe('PUT')
    expect(f.mock.calls[0][1].body).toBe(
      '{"env":[{"name":"A","value":"1","secret":false},{"name":"S","secret":true}]}',
    )
  })

  it('updateSession omits undefined fields', async () => {
    const f = stub({})
    await api.updateSession('s1', { name: 'n', env: undefined, loadDotenv: undefined })
    expect(f.mock.calls[0][1].body).toBe('{"name":"n"}')
  })

  it('updateSession sends env and loadDotenv when given', async () => {
    const f = stub({})
    await api.updateSession('s1', { env: [{ name: 'S', secret: true }], loadDotenv: false })
    expect(JSON.parse(f.mock.calls[0][1].body)).toEqual({
      env: [{ name: 'S', secret: true }],
      loadDotenv: false,
    })
  })
})
