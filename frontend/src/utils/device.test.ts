import { describe, expect, it } from 'vitest'
import { hasFinePointer } from './device'

function host(matches: Record<string, boolean>) {
  const queried: string[] = []
  return {
    queried,
    win: {
      matchMedia: (query: string) => {
        queried.push(query)
        return { matches: matches[query] ?? false }
      },
    },
  }
}

describe('hasFinePointer', () => {
  it('accepts a mouse-driven device', () => {
    const { win } = host({ '(hover: hover) and (pointer: fine)': true })
    expect(hasFinePointer(win)).toBe(true)
  })

  it('rejects a touchscreen', () => {
    const { win } = host({ '(hover: hover) and (pointer: fine)': false })
    expect(hasFinePointer(win)).toBe(false)
  })

  it('asks for hover and a fine pointer together', () => {
    // Either half alone matches devices we do not want: a stylus reports a
    // fine pointer, and some touch devices report hover.
    const { win, queried } = host({})
    hasFinePointer(win)
    expect(queried).toEqual(['(hover: hover) and (pointer: fine)'])
  })

  it('treats a browser without matchMedia as touch', () => {
    expect(hasFinePointer({})).toBe(false)
  })
})
