import { describe, it, expect } from 'vitest'
import { documentTitleFor } from './useDocumentTitle'

describe('documentTitleFor', () => {
  // The title used to be built from the route meta alone, so every open
  // terminal tab was called "Terminal" and told the user nothing about which
  // session it held.
  it('prefers the session name over the route title', () => {
    expect(documentTitleFor('Terminal', 'build-server')).toBe('Sessile • build-server')
  })

  it('falls back to the route title when there is no session', () => {
    expect(documentTitleFor('Sessions', null)).toBe('Sessile — Sessions')
    expect(documentTitleFor('Terminal', undefined)).toBe('Sessile — Terminal')
  })

  it('falls back to the brand alone with neither', () => {
    expect(documentTitleFor(null, null)).toBe('Sessile')
    expect(documentTitleFor('', '')).toBe('Sessile')
  })

  // A session whose name is only whitespace would otherwise render as
  // "Sessile • " — worse than the route title it replaced.
  it('ignores a blank session name', () => {
    expect(documentTitleFor('Terminal', '   ')).toBe('Sessile — Terminal')
  })

  it('keeps the session name verbatim', () => {
    expect(documentTitleFor('Terminal', ' ~/src — notes ')).toBe('Sessile • ~/src — notes')
  })
})
