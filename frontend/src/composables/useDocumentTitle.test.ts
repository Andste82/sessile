import { describe, it, expect } from 'vitest'
import { documentTitleFor } from './useDocumentTitle'

describe('documentTitleFor', () => {
  // The title used to be built from the route meta alone, so every open
  // terminal tab was called "Terminal" and told the user nothing about which
  // session it held.
  it('prefers the session name over the route title', () => {
    expect(documentTitleFor('Terminal', 'build-server')).toBe('sessile • build-server')
  })

  // The same separator on every route: the dashboard and settings are named
  // the same way a session is, because the part after the bullet is simply
  // what you are looking at.
  it('falls back to the route title when there is no session', () => {
    expect(documentTitleFor('Sessions', null)).toBe('sessile • sessions')
    expect(documentTitleFor('Settings', null)).toBe('sessile • settings')
    expect(documentTitleFor('Terminal', undefined)).toBe('sessile • terminal')
  })

  it('trims a route title that arrives padded', () => {
    expect(documentTitleFor('  Sessions  ', null)).toBe('sessile • sessions')
    expect(documentTitleFor('   ', null)).toBe('sessile')
  })

  it('falls back to the brand alone with neither', () => {
    expect(documentTitleFor(null, null)).toBe('sessile')
    expect(documentTitleFor('', '')).toBe('sessile')
  })

  // A session whose name is only whitespace would otherwise render as
  // "sessile • " — worse than the route title it replaced.
  it('ignores a blank session name', () => {
    expect(documentTitleFor('Terminal', '   ')).toBe('sessile • terminal')
  })

  // The brand and the route word are ours to style; a session name is the
  // user's text and is left exactly as they typed it.
  it('keeps the session name verbatim', () => {
    expect(documentTitleFor('Terminal', ' Build Server ')).toBe('sessile • Build Server')
    expect(documentTitleFor('Terminal', ' ~/src — notes ')).toBe('sessile • ~/src — notes')
  })
})
