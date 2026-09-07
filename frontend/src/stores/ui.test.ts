import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import {
  clampFontSize,
  defaultCopyOnSelect,
  defaultFontSize,
  maxFontSize,
  minFontSize,
  defaultFilesPanelTab,
  parseCollapsedGroups,
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

describe('files & processes panel state', () => {
  // The panel is one component instance serving every terminal tab, so the
  // state that decides what it shows has to be keyed by session.
  it('keeps open, tab and path separate per session', () => {
    const ui = useUiStore()

    ui.setPanelOpen('a', true)
    ui.setPanelTab('a', 'processes')
    ui.setPanelPath('a', '/srv/app')

    expect(ui.panelFor('a')).toMatchObject({ open: true, tab: 'processes', path: '/srv/app' })
    expect(ui.panelFor('b')).toMatchObject({ open: false, tab: defaultFilesPanelTab })
    expect(ui.panelFor('b').path).toBeUndefined()
  })

  it('starts on the Files tab', () => {
    expect(useUiStore().panelFor('a').tab).toBe('files')
  })

  // undefined is what makes the explorer open at the session root exactly
  // once; '' is a real path — the target's own default root — and storing it
  // has to count as "has been here".
  it('treats a stored empty path as a visited directory', () => {
    const ui = useUiStore()
    ui.setPanelPath('a', '')
    expect(ui.panelFor('a').path).toBe('')
  })

  it('forgets a session, leaving its neighbours alone', () => {
    const ui = useUiStore()
    ui.setPanelPath('a', '/one')
    ui.setPanelPath('b', '/two')

    ui.forgetSessionPanel('a')

    expect(ui.filesPanels.a).toBeUndefined()
    expect(ui.panelFor('b').path).toBe('/two')
    // Asking again after forgetting is a fresh start, not the old directory.
    expect(ui.panelFor('a').path).toBeUndefined()
  })
})

const nothingCollapsed = { dashboard: [], sidebar: [] }

describe('parseCollapsedGroups', () => {
  it('reads back what the store writes', () => {
    const stored = JSON.stringify({ dashboard: ['Production'], sidebar: ['Staging'] })
    expect(parseCollapsedGroups(stored)).toEqual({
      dashboard: ['Production'],
      sidebar: ['Staging'],
    })
  })

  // Anything unreadable errs towards showing sessions rather than hiding them.
  // The bare array is what an earlier build wrote, when both lists shared one
  // set — it has no per-view answer in it, so it reads as nothing collapsed
  // rather than being applied to both.
  it.each([null, undefined, '', 'not json', '"Production"', '["Production"]', '7'])(
    'falls back to nothing collapsed for %o',
    (input) => {
      expect(parseCollapsedGroups(input)).toEqual(nothingCollapsed)
    },
  )

  it('keeps only the strings, and fills in a missing view', () => {
    expect(parseCollapsedGroups('{"dashboard":["Production",7,null,"Staging"]}')).toEqual({
      dashboard: ['Production', 'Staging'],
      sidebar: [],
    })
  })
})

describe('collapsed groups', () => {
  it('toggles a group on and off', () => {
    withStorage(fakeStorage())
    const ui = useUiStore()
    expect(ui.isGroupCollapsed('dashboard', 'Production')).toBe(false)

    ui.toggleGroup('dashboard', 'Production')
    expect(ui.isGroupCollapsed('dashboard', 'Production')).toBe(true)
    expect(ui.isGroupCollapsed('dashboard', 'Staging')).toBe(false)

    ui.toggleGroup('dashboard', 'Production')
    expect(ui.isGroupCollapsed('dashboard', 'Production')).toBe(false)
  })

  // The two lists are different views, not two windows onto one: folding a
  // group away on the dashboard must leave the navigation strip alone.
  it('keeps the two lists apart', () => {
    withStorage(fakeStorage())
    const ui = useUiStore()

    ui.toggleGroup('dashboard', 'Production')

    expect(ui.isGroupCollapsed('dashboard', 'Production')).toBe(true)
    expect(ui.isGroupCollapsed('sidebar', 'Production')).toBe(false)

    ui.toggleGroup('sidebar', 'Production')
    ui.toggleGroup('dashboard', 'Production')

    expect(ui.isGroupCollapsed('dashboard', 'Production')).toBe(false)
    expect(ui.isGroupCollapsed('sidebar', 'Production')).toBe(true)
  })

  it('persists both views', async () => {
    const storage = fakeStorage()
    withStorage(storage)
    const ui = useUiStore()

    ui.toggleGroup('sidebar', 'Production')
    await Promise.resolve()

    expect(storage.data['sessile.collapsedGroups']).toBe(
      JSON.stringify({ dashboard: [], sidebar: ['Production'] }),
    )
  })

  it('starts from what another session of this browser stored', () => {
    withStorage(
      fakeStorage({
        'sessile.collapsedGroups': JSON.stringify({ dashboard: [], sidebar: ['Staging'] }),
      }),
    )
    const ui = useUiStore()
    expect(ui.isGroupCollapsed('sidebar', 'Staging')).toBe(true)
    expect(ui.isGroupCollapsed('dashboard', 'Staging')).toBe(false)
  })

  // Two tabs are a normal way to use this app: collapsing in one has to reach
  // the other, or the same list reads as both open and closed.
  it('follows another tab', () => {
    withStorage(fakeStorage())
    const win = withWindow()
    const ui = useUiStore()

    win.storage(
      'sessile.collapsedGroups',
      JSON.stringify({ dashboard: ['Production'], sidebar: [] }),
    )
    expect(ui.isGroupCollapsed('dashboard', 'Production')).toBe(true)

    win.storage(null, null) // a clear() takes every preference with it
    expect(ui.isGroupCollapsed('dashboard', 'Production')).toBe(false)
  })
})
