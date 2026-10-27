import { describe, it, expect } from 'vitest'
import type { Session } from '@/api/types'
import { compareNames, compareSessions } from './naturalSort'

const sortNames = (names: string[]) => [...names].sort(compareNames)

describe('compareNames', () => {
  it('orders digit runs numerically', () => {
    expect(sortNames(['a10', 'a2', 'a1'])).toEqual(['a1', 'a2', 'a10'])
    expect(sortNames(['host10', 'host9', 'host2'])).toEqual(['host2', 'host9', 'host10'])
  })

  it('ignores case', () => {
    expect(sortNames(['beta', 'Alpha', 'gamma'])).toEqual(['Alpha', 'beta', 'gamma'])
    expect(sortNames(['b', 'A', 'c'])).toEqual(['A', 'b', 'c'])
  })

  it('sorts umlauts next to their base letter, not after z', () => {
    expect(sortNames(['Zeta', 'Äpfel', 'Berta'])).toEqual(['Äpfel', 'Berta', 'Zeta'])
  })

  it('is deterministic for names the collator calls equal', () => {
    expect(compareNames('x', 'x')).toBe(0)
    expect(sortNames(['prod', 'Prod'])).toEqual(sortNames(['Prod', 'prod']))
    expect(compareNames('prod', 'Prod')).not.toBe(0)
  })
})

describe('compareSessions', () => {
  const s = (id: string, name: string) => ({ id, name }) as Session

  it('orders by name naturally', () => {
    const list = [s('1', 'web10'), s('2', 'web2')].sort(compareSessions)
    expect(list.map((x) => x.name)).toEqual(['web2', 'web10'])
  })

  it('breaks ties on id, whatever the input order', () => {
    const a = [s('b', 'same'), s('a', 'same')].sort(compareSessions)
    const b = [s('a', 'same'), s('b', 'same')].sort(compareSessions)
    expect(a.map((x) => x.id)).toEqual(['a', 'b'])
    expect(b.map((x) => x.id)).toEqual(['a', 'b'])
  })
})
