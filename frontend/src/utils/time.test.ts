import { describe, expect, it } from 'vitest'
import { duration, relativeTime } from './time'

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

describe('duration', () => {
  it.each([
    ['2026-08-07T11:59:58Z', '2s'],
    ['2026-08-07T11:59:01Z', '59s'],
    ['2026-08-07T11:57:00Z', '3m'],
    ['2026-08-07T11:00:00Z', '1h'],
    ['2026-08-05T12:00:00Z', '2d'],
  ])('formats %s as %s', (iso, want) => {
    expect(duration(iso, now)).toBe(want)
  })

  // A session that has never changed state has no since timestamp. "0s" would
  // read as one that just did, so the card shows nothing at all instead.
  it.each([
    ['an empty string', ''],
    ['an unparseable value', 'not a date'],
  ])('returns nothing for %s', (_label, iso) => {
    expect(duration(iso, now)).toBe('')
  })

  it('never counts backwards from a clock that is slightly ahead', () => {
    expect(duration('2026-08-07T12:00:05Z', now)).toBe('0s')
  })
})
