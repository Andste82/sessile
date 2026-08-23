import { describe, expect, it } from 'vitest'
import { relativeTime } from './time'

const now = Date.parse('2026-08-07T12:00:00Z')

describe('relativeTime', () => {
  it.each([
    ['2026-08-07T11:59:58Z', 'just now'],
    ['2026-08-07T11:59:30Z', '30s ago'],
    ['2026-08-07T11:57:00Z', '3m ago'],
    ['2026-08-07T11:00:00Z', '1h ago'],
    ['2026-08-05T12:00:00Z', '2d ago'],
  ])('formats %s as %s', (iso, want) => {
    expect(relativeTime(iso, now)).toBe(want)
  })

  it('returns nothing for an unparseable value', () => {
    expect(relativeTime('not a date', now)).toBe('')
  })
})
