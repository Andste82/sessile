import { describe, it, expect } from 'vitest'
import { applyModifiers, encodeSpecial, noMods, type Mods } from './keys'

const mods = (m: Partial<Mods>): Mods => ({ ...noMods, ...m })

describe('applyModifiers', () => {
  it('passes plain input through untouched', () => {
    expect(applyModifiers('c', noMods)).toBe('c')
  })

  it('maps Ctrl + letter to a control code', () => {
    expect(applyModifiers('c', mods({ ctrl: true }))).toBe('\x03')
    expect(applyModifiers('C', mods({ ctrl: true }))).toBe('\x03')
    expect(applyModifiers('d', mods({ ctrl: true }))).toBe('\x04')
  })

  it('prefixes Alt + letter with ESC', () => {
    expect(applyModifiers('b', mods({ alt: true }))).toBe('\x1bb')
  })

  it('upper-cases with Shift', () => {
    expect(applyModifiers('a', mods({ shift: true }))).toBe('A')
  })

  it('leaves multi-char (paste) input alone', () => {
    expect(applyModifiers('hello', mods({ ctrl: true }))).toBe('hello')
  })
})

describe('encodeSpecial', () => {
  it('encodes plain special keys', () => {
    expect(encodeSpecial('Escape')).toBe('\x1b')
    expect(encodeSpecial('Tab')).toBe('\t')
    expect(encodeSpecial('Up')).toBe('\x1b[A')
    expect(encodeSpecial('Delete')).toBe('\x1b[3~')
  })

  it('encodes Shift-Tab as CSI Z', () => {
    expect(encodeSpecial('Tab', mods({ shift: true }))).toBe('\x1b[Z')
  })

  it('adds a modifier parameter to cursor keys', () => {
    expect(encodeSpecial('Right', mods({ ctrl: true }))).toBe('\x1b[1;5C')
    expect(encodeSpecial('Left', mods({ shift: true }))).toBe('\x1b[1;2D')
  })

  it('adds a modifier parameter to tilde keys', () => {
    expect(encodeSpecial('Delete', mods({ ctrl: true }))).toBe('\x1b[3;5~')
  })

  it('encodes function keys', () => {
    expect(encodeSpecial('F1')).toBe('\x1bOP')
    expect(encodeSpecial('F5')).toBe('\x1b[15~')
    expect(encodeSpecial('F12')).toBe('\x1b[24~')
  })
})
