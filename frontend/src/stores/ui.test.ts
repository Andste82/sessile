import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import {
  clampFontSize,
  defaultCopyOnSelect,
  defaultFontSize,
  maxFontSize,
  minFontSize,
  parseCopyOnSelect,
  useUiStore,
} from './ui'

// localStorage does not exist in the test environment, so each case installs the
// shape the store reaches for and takes it away again.
function withStorage(storage: unknown) {
  ;(globalThis as { localStorage?: unknown }).localStorage = storage
}

// Same for window: the store subscribes to `storage` on it, and the test needs
// to hold on to the handler so it can play the other tab.
function withWindow() {
  const handlers: Record<string, (e: unknown) => void> = {}
  ;(globalThis as { window?: unknown }).window = {
    addEventListener: (type: string, fn: (e: unknown) => void) => {
      handlers[type] = fn
    },
  }
  return {
    // Fires what another tab's write would deliver here.
    storage(key: string | null, newValue: string | null) {
      handlers.storage?.({ key, newValue })
    },
  }
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
  delete (globalThis as { window?: unknown }).window
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

describe('parseCopyOnSelect', () => {
  it.each([
    ['true', true],
    ['false', false],
  ])('reads %o as %o', (input, want) => {
    expect(parseCopyOnSelect(input)).toBe(want)
  })

  // Anything we did not write is "no usable value", not "off": '' and null are
  // falsy, and reading them as a decision would silently disable the feature.
  it.each([['', 'nonsense', '1', null, undefined]].flat())(
    'falls back to the default for %o',
    (input) => {
      expect(parseCopyOnSelect(input)).toBe(defaultCopyOnSelect)
    },
  )
})

describe('copy on select', () => {
  it('defaults when nothing is stored', () => {
    withStorage(fakeStorage())
    expect(useUiStore().copyOnSelect).toBe(defaultCopyOnSelect)
  })

  it('restores a stored choice', () => {
    withStorage(fakeStorage({ 'sessile.copyOnSelect': 'false' }))
    expect(useUiStore().copyOnSelect).toBe(false)
  })

  it('persists a new choice', async () => {
    const storage = fakeStorage()
    withStorage(storage)
    const ui = useUiStore()

    ui.setCopyOnSelect(false)
    await Promise.resolve() // the watcher that writes runs on the microtask queue

    expect(ui.copyOnSelect).toBe(false)
    expect(storage.data['sessile.copyOnSelect']).toBe('false')
  })

  it('follows a choice another tab wrote', () => {
    const win = withWindow()
    withStorage(fakeStorage())
    const ui = useUiStore()

    win.storage('sessile.copyOnSelect', 'false')

    expect(ui.copyOnSelect).toBe(false)
  })

  // A cleared storage reports no key at all, and takes every preference with it.
  it('falls back to the default on a cleared storage', () => {
    const win = withWindow()
    withStorage(fakeStorage({ 'sessile.copyOnSelect': 'false' }))
    const ui = useUiStore()
    expect(ui.copyOnSelect).toBe(false)

    win.storage(null, null)

    expect(ui.copyOnSelect).toBe(defaultCopyOnSelect)
  })

  // The two preferences share one `storage` handler, so a write to either must
  // leave the other where it was.
  it('is left alone by a font size from another tab', () => {
    const win = withWindow()
    withStorage(fakeStorage({ 'sessile.copyOnSelect': 'false' }))
    const ui = useUiStore()

    win.storage('sessile.terminalFontSize', '24')

    expect(ui.terminalFontSize).toBe(24)
    expect(ui.copyOnSelect).toBe(false)
  })

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
    expect(ui.copyOnSelect).toBe(defaultCopyOnSelect)

    ui.setCopyOnSelect(!defaultCopyOnSelect)
    await Promise.resolve()

    expect(ui.copyOnSelect).toBe(!defaultCopyOnSelect)
  })
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

  // Two tabs on the same session mirror each other, so one of them rendering at
  // the old size until it is reloaded reads as the setting not having taken.
  it('follows a size another tab wrote', () => {
    const win = withWindow()
    withStorage(fakeStorage())
    const ui = useUiStore()

    win.storage('sessile.terminalFontSize', '24')

    expect(ui.terminalFontSize).toBe(24)
  })

  it('ignores another key', () => {
    const win = withWindow()
    withStorage(fakeStorage())
    const ui = useUiStore()

    win.storage('sessile.somethingElse', '24')

    expect(ui.terminalFontSize).toBe(defaultFontSize)
  })

  it.each([
    ['a cleared preference', 'sessile.terminalFontSize', null],
    ['a cleared storage, which reports no key at all', null, null],
  ])('falls back to the default on %s', (_label, key, newValue) => {
    const win = withWindow()
    withStorage(fakeStorage({ 'sessile.terminalFontSize': '24' }))
    const ui = useUiStore()
    expect(ui.terminalFontSize).toBe(24)

    win.storage(key, newValue)

    expect(ui.terminalFontSize).toBe(defaultFontSize)
  })

  // Applying an incoming value writes it straight back through the persisting
  // watcher. setItem with the value already stored is a no-op that notifies
  // nobody, so the two tabs cannot bounce it between them — but the write must
  // at least carry the value that arrived, not the one it replaced.
  it('writes back exactly what arrived', async () => {
    const win = withWindow()
    const storage = fakeStorage({ 'sessile.terminalFontSize': '13' })
    withStorage(storage)
    useUiStore()

    win.storage('sessile.terminalFontSize', '24')
    await Promise.resolve()

    expect(storage.data['sessile.terminalFontSize']).toBe('24')
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
