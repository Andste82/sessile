import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useToastsStore } from './toasts'

describe('toasts store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('defaults to kind info and appends in order', () => {
    const s = useToastsStore()
    s.push('a')
    s.push('b', 'warning')
    expect(s.toasts.map((t) => [t.message, t.kind])).toEqual([
      ['a', 'info'],
      ['b', 'warning'],
    ])
  })

  it('expires after the default 8 s', () => {
    const s = useToastsStore()
    s.push('a')
    vi.advanceTimersByTime(7999)
    expect(s.toasts).toHaveLength(1)
    vi.advanceTimersByTime(1)
    expect(s.toasts).toHaveLength(0)
  })

  it('honours a custom ttl and ttl 0 means sticky', () => {
    const s = useToastsStore()
    s.push('short', 'info', 100)
    s.push('sticky', 'error', 0)
    vi.advanceTimersByTime(100)
    expect(s.toasts.map((t) => t.message)).toEqual(['sticky'])
    vi.advanceTimersByTime(100000)
    expect(s.toasts).toHaveLength(1)
  })

  it('dismiss removes one toast and cancels its timer', () => {
    const s = useToastsStore()
    const a = s.push('a')
    s.push('b')
    s.dismiss(a)
    expect(s.toasts.map((t) => t.message)).toEqual(['b'])
    vi.advanceTimersByTime(8000)
    expect(s.toasts).toHaveLength(0)
  })
})
