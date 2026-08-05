import { describe, it, expect } from 'vitest'
import {
  scrollTargetFor,
  type BufferType,
  type MouseTracking,
  type ScrollTarget,
} from './gesture'

describe('scrollTargetFor', () => {
  const cases: [BufferType, MouseTracking, ScrollTarget, string][] = [
    ['normal', 'none', 'backlog', 'a plain shell scrolls its own backlog'],
    [
      'normal',
      'x10',
      'backlog',
      'x10 reports button presses only, so a wheel is still ours',
    ],
    [
      'normal',
      'vt200',
      'application',
      'a program tracking the mouse wants the scroll as a report',
    ],
    ['normal', 'drag', 'application', 'as above, drag protocol'],
    ['normal', 'any', 'application', 'as above, any-event protocol'],
    [
      'alternate',
      'none',
      'application',
      'the alternate screen has no scrollback: less and vim scroll themselves',
    ],
    [
      'alternate',
      'x10',
      'application',
      'still the alternate screen, whatever the mouse mode',
    ],
    ['alternate', 'any', 'application', 'both reasons at once'],
  ]

  for (const [buffer, mouse, want, why] of cases) {
    it(`${buffer} + ${mouse} -> ${want}: ${why}`, () => {
      expect(scrollTargetFor(buffer, mouse)).toBe(want)
    })
  }
})
