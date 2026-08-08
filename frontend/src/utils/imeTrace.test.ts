import { describe, expect, it } from 'vitest'
import {
  captureImeTraceFlag,
  createImeTrace,
  formatImeTrace,
  imeTraceEnabled,
  imeTraceKey,
  stateFlags,
} from './imeTrace'

function fakeStore(initial: Record<string, string> = {}) {
  const map = new Map(Object.entries(initial))
  return {
    getItem: (k: string) => map.get(k) ?? null,
    setItem: (k: string, v: string) => void map.set(k, v),
    removeItem: (k: string) => void map.delete(k),
  }
}

describe('captureImeTraceFlag', () => {
  it.each([
    ['?debug=ime', true],
    ['?foo=1&debug=ime', true],
    ['?debug=scroll', false],
    ['?debug=', false],
    ['', false],
    ['?other=ime', false],
  ])('reads %s as %s', (search, want) => {
    expect(captureImeTraceFlag(search, fakeStore())).toBe(want)
  })

  it('is turned off again by an explicit other debug value', () => {
    const store = fakeStore({ [imeTraceKey]: '1' })
    expect(captureImeTraceFlag('?debug=off', store)).toBe(false)
    expect(imeTraceEnabled(store)).toBe(false)
  })

  // Private-mode browsers throw on storage access; the query must still work
  // for whoever opened the terminal URL directly.
  it('falls back to the query when storage throws', () => {
    const hostile = {
      getItem: () => {
        throw new Error('denied')
      },
      setItem: () => {
        throw new Error('denied')
      },
      removeItem: () => {
        throw new Error('denied')
      },
    }
    expect(captureImeTraceFlag('?debug=ime', hostile)).toBe(true)
    expect(captureImeTraceFlag('', hostile)).toBe(false)
  })
})

describe('imeTraceEnabled', () => {
  // This is the sequence that shipped broken: the flag was only ever read on
  // the terminal page, so opening the app on the dashboard with ?debug=ime and
  // tapping a session arrived with no query and nothing written down. Capture
  // happens at startup now, whatever route the app was opened on, and the
  // terminal only reads.
  it('is on at the terminal after the app was launched on the dashboard', () => {
    const store = fakeStore()
    captureImeTraceFlag('?debug=ime', store) // main.ts, at boot, on "/"
    expect(imeTraceEnabled(store)).toBe(true) // useTerminal, later, on "/sessions/x"
  })

  it('is on when the terminal URL itself carried the flag', () => {
    const store = fakeStore()
    captureImeTraceFlag('?debug=ime', store)
    expect(imeTraceEnabled(store)).toBe(true)
  })

  it('is off when nothing ever asked for it', () => {
    const store = fakeStore()
    captureImeTraceFlag('', store)
    expect(imeTraceEnabled(store)).toBe(false)
  })

  it('reads nothing without a store', () => {
    expect(imeTraceEnabled(null)).toBe(false)
  })
})

describe('createImeTrace', () => {
  it('stamps entries relative to the first one', () => {
    let now = 1000
    const trace = createImeTrace(10, () => now)

    trace.record({ kind: 'compositionstart' })
    now = 1040
    trace.record({ kind: 'compositionend' })

    expect(trace.entries().map((e) => e.at)).toEqual([0, 40])
  })

  it('keeps the newest entries when the cap is reached', () => {
    const trace = createImeTrace(3, () => 0)
    for (const kind of ['a', 'b', 'c', 'd']) trace.record({ kind })

    expect(trace.entries().map((e) => e.kind)).toEqual(['b', 'c', 'd'])
  })

  it('restarts the clock after a clear', () => {
    let now = 500
    const trace = createImeTrace(10, () => now)
    trace.record({ kind: 'a' })
    trace.clear()
    now = 900
    trace.record({ kind: 'b' })

    expect(trace.entries()).toHaveLength(1)
    expect(trace.entries()[0].at).toBe(0)
  })

  it('hands back a copy, so a caller cannot edit the log', () => {
    const trace = createImeTrace(10, () => 0)
    trace.record({ kind: 'a' })
    trace.entries().push({ at: 0, kind: 'injected' })

    expect(trace.entries().map((e) => e.kind)).toEqual(['a'])
  })
})

describe('formatImeTrace', () => {
  // The entire bug is whether one space survives. A bare trailing space in a
  // log line is invisible, so every captured string is quoted.
  it('quotes strings, so a space can be seen', () => {
    const out = formatImeTrace([
      { at: 12, kind: 'input gated', inputType: 'insertText', data: ' ', ta: 'hello ' },
    ])
    expect(out).toContain('data=" "')
    expect(out).toContain('ta="hello "')
  })

  it('shows an empty string as such rather than as nothing', () => {
    const out = formatImeTrace([{ at: 0, kind: 'settle', ta: '' }])
    expect(out).toContain('ta=""')
  })

  it('keeps newlines visible', () => {
    const out = formatImeTrace([{ at: 0, kind: 'SENT', data: 'ls\n' }])
    expect(out).toContain('data="ls\\n"')
    expect(out.split('\n')).toHaveLength(1)
  })

  it('renders one line per entry', () => {
    const out = formatImeTrace([
      { at: 0, kind: 'compositionstart' },
      { at: 5, kind: 'compositionend' },
    ])
    expect(out.split('\n')).toHaveLength(2)
  })

  it('says so when nothing has been recorded', () => {
    expect(formatImeTrace([])).toContain('nothing recorded')
  })

  it('includes key identity for a keydown', () => {
    const out = formatImeTrace([{ at: 0, kind: 'ime key', key: 'Process', keyCode: 229 }])
    expect(out).toContain('key="Process"')
    expect(out).toContain('code=229')
  })
})

describe('stateFlags', () => {
  it('renders the sequence state compactly', () => {
    expect(stateFlags(true, false, false)).toBe('A+ C- D-')
    expect(stateFlags(true, true, true)).toBe('A+ C+ D+')
    expect(stateFlags(false, false, false)).toBe('A- C- D-')
  })
})
