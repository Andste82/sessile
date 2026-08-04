import { describe, expect, it, vi, afterEach } from 'vitest'
import { loadSymbolFont, symbolFontFamilies } from './fonts'

// document does not exist in the test environment, so each case installs the
// shape loadSymbolFont reaches for and takes it away again.
function withDocument(doc: unknown) {
  ;(globalThis as { document?: unknown }).document = doc
}

afterEach(() => {
  delete (globalThis as { document?: unknown }).document
  vi.useRealTimers()
})

describe('loadSymbolFont', () => {
  it('asks for every symbol family at the terminal font size', async () => {
    const load = vi.fn().mockResolvedValue([])
    withDocument({ fonts: { load } })

    await loadSymbolFont(13)

    // Both faces ship: they divide the symbol blocks between them.
    expect(load).toHaveBeenCalledTimes(symbolFontFamilies.length)
    expect(load.mock.calls.map(([spec]) => spec)).toEqual(
      symbolFontFamilies.map((family) => `13px ${family}`),
    )
    // A font is only fetched when a character asks for it, so each sample has to
    // be one that face actually carries — fonts.symbols.test.ts checks that
    // against the files themselves.
    expect(load.mock.calls.map(([, text]) => text)).toEqual(['☢', '⚛'])
  })

  // The terminal renders with whatever the machine has if this fails, which is
  // what it did before the font existed — never a reason to fail the mount.
  it('resolves when the font fails to load', async () => {
    withDocument({ fonts: { load: vi.fn().mockRejectedValue(new Error('offline')) } })
    await expect(loadSymbolFont(13)).resolves.toBeUndefined()
  })

  it('resolves where the font loading API does not exist', async () => {
    withDocument({})
    await expect(loadSymbolFont(13)).resolves.toBeUndefined()
    withDocument({ fonts: {} })
    await expect(loadSymbolFont(13)).resolves.toBeUndefined()
  })

  // A slow font must not hold the terminal closed: it only costs the width of a
  // symbol that happens to arrive before it does.
  it('gives up waiting rather than blocking the terminal', async () => {
    vi.useFakeTimers()
    withDocument({ fonts: { load: () => new Promise(() => {}) } })

    const settled = vi.fn()
    void loadSymbolFont(13).then(settled)

    await vi.advanceTimersByTimeAsync(1499)
    expect(settled).not.toHaveBeenCalled()

    await vi.advanceTimersByTimeAsync(1)
    expect(settled).toHaveBeenCalled()
  })
})
