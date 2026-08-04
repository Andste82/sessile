import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { clampFontSize, defaultFontSize, maxFontSize, minFontSize, useUiStore } from './ui'

// localStorage does not exist in the test environment, so each case installs the
// shape the store reaches for and takes it away again.
function withStorage(storage: unknown) {
  ;(globalThis as { localStorage?: unknown }).localStorage = storage
}

function fakeStorage(initial: Record<string, string> = {}) {
  const data = { ...initial }
  return {
    data,
    getItem: vi.fn((k: string) => (k in data ? data[k] : null)),
    setItem: vi.fn((k: string, v: string) => {
      data[k] = v
    }),
  }
}

beforeEach(() => {
  setActivePinia(createPinia())
})

afterEach(() => {
  delete (globalThis as { localStorage?: unknown }).localStorage
})

describe('clampFontSize', () => {
  it.each([
    [minFontSize - 1, minFontSize],
    [maxFontSize + 1, maxFontSize],
    [minFontSize, minFontSize],
    [maxFontSize, maxFontSize],
    ['20', 20],
    [14.6, 15],
  ])('maps %o onto %i', (input, want) => {
    expect(clampFontSize(input)).toBe(want)
  })

  // A garbled or empty stored value must not become the smallest font we allow,
  // which is what clamping alone would do with Number('') === 0.
  it.each([['', 'nonsense', null, undefined, NaN]].flat())(
    'falls back to the default for %o',
    (input) => {
      expect(clampFontSize(input)).toBe(defaultFontSize)
    },
  )
})

describe('terminal font size', () => {
  it('defaults when nothing is stored', () => {
    withStorage(fakeStorage())
    expect(useUiStore().terminalFontSize).toBe(defaultFontSize)
  })

  it('restores the stored size', () => {
    withStorage(fakeStorage({ 'sessile.terminalFontSize': '19' }))
    expect(useUiStore().terminalFontSize).toBe(19)
  })

  it('persists a new size', async () => {
    const storage = fakeStorage()
    withStorage(storage)
    const ui = useUiStore()

    ui.setTerminalFontSize(21)
    await Promise.resolve() // the watcher that writes runs on the microtask queue

    expect(ui.terminalFontSize).toBe(21)
    expect(storage.data['sessile.terminalFontSize']).toBe('21')
  })

  it('clamps what it is given, so a stepper can walk off either end', () => {
    withStorage(fakeStorage())
    const ui = useUiStore()

    ui.setTerminalFontSize(maxFontSize + 5)
    expect(ui.terminalFontSize).toBe(maxFontSize)
    ui.setTerminalFontSize(minFontSize - 5)
    expect(ui.terminalFontSize).toBe(minFontSize)
  })

  // A browser with storage blocked throws on access rather than returning null.
  // Losing the preference is not a reason to fail to build the store, or to let
  // a size change reject.
  it('survives storage that throws', async () => {
    withStorage({
      getItem: () => {
        throw new Error('blocked')
      },
      setItem: () => {
        throw new Error('blocked')
      },
    })
    const ui = useUiStore()
    expect(ui.terminalFontSize).toBe(defaultFontSize)

    ui.setTerminalFontSize(17)
    await Promise.resolve()

    expect(ui.terminalFontSize).toBe(17)
  })
})
