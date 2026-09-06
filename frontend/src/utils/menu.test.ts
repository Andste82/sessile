import { describe, expect, it } from 'vitest'
import { nextMenuIndex, shouldDropUp } from './menu'

describe('shouldDropUp', () => {
  it('stays below when the menu fits there', () => {
    expect(shouldDropUp(100, 144, 200, 800)).toBe(false)
  })

  it('flips up for a row near the bottom', () => {
    // Trigger sits 44px above the fold; 200px of menu would not fit below.
    expect(shouldDropUp(756, 800, 200, 800)).toBe(true)
  })

  it('stays below when it fits in neither direction', () => {
    // Above would overflow too, and below at least shows the first items.
    expect(shouldDropUp(50, 94, 400, 300)).toBe(false)
  })

  it('treats an exact fit as fitting', () => {
    expect(shouldDropUp(600, 640, 160, 800)).toBe(false)
  })
})

describe('nextMenuIndex', () => {
  it('opens onto the first item with ArrowDown', () => {
    expect(nextMenuIndex(-1, 1, 3)).toBe(0)
  })

  it('opens onto the last item with ArrowUp', () => {
    expect(nextMenuIndex(-1, -1, 3)).toBe(2)
  })

  it('wraps past the end', () => {
    expect(nextMenuIndex(2, 1, 3)).toBe(0)
  })

  it('wraps past the start', () => {
    expect(nextMenuIndex(0, -1, 3)).toBe(2)
  })

  it('reports nothing selectable for an empty menu', () => {
    expect(nextMenuIndex(-1, 1, 0)).toBe(-1)
  })
})
