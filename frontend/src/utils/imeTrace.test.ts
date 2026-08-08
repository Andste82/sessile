import { describe, expect, it } from 'vitest'
import { createImeTrace, formatImeTrace, imeTraceEnabled, stateFlags } from './imeTrace'

describe('imeTraceEnabled', () => {
  it.each([
    ['?debug=ime', true],
    ['?foo=1&debug=ime', true],
    ['?debug=scroll', false],
    ['?debug=', false],
    ['', false],
    ['?other=ime', false],
  ])('reads %s as %s', (search, want) => {
    expect(imeTraceEnabled(search)).toBe(want)
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
