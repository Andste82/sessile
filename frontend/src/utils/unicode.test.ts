import { describe, expect, it } from 'vitest'
import type { IUnicodeVersionProvider } from '@xterm/xterm'
import { applyUnicodeVersion, unicodeVersion } from './unicode'

// A stand-in for the parts of Terminal applyUnicodeVersion touches, which also
// runs the addon's activate() — so these tests exercise the provider the app
// actually ends up with, not a copy of its table.
function terminal() {
  const registered: IUnicodeVersionProvider[] = []
  const unicode = {
    activeVersion: '6',
    register: (p: IUnicodeVersionProvider) => registered.push(p),
    versions: [] as string[],
  }
  const term = {
    unicode,
    loadAddon: (addon: { activate(t: unknown): void }) => addon.activate(term),
  }
  return { term, unicode, registered }
}

function widthOf(codepoint: number): number {
  const { term, registered } = terminal()
  applyUnicodeVersion(term)
  return registered[0].wcwidth(codepoint)
}

describe('applyUnicodeVersion', () => {
  it('registers a provider and selects it', () => {
    const { term, unicode, registered } = terminal()
    applyUnicodeVersion(term)
    expect(registered).toHaveLength(1)
    expect(registered[0].version).toBe(unicodeVersion)
    expect(unicode.activeVersion).toBe(unicodeVersion)
  })

  it('gives emoji two columns', () => {
    // The regression in issue #27: under xterm's default Unicode 6 table these
    // are width 1, so following text lands on the glyph's right half.
    expect(widthOf(0x1f600)).toBe(2) // 😀
    expect(widthOf(0x1f680)).toBe(2) // 🚀
    expect(widthOf(0x2705)).toBe(2) // ✅
  })

  it('keeps CJK wide and ASCII narrow', () => {
    expect(widthOf(0x4e2d)).toBe(2) // 中
    expect(widthOf(0x41)).toBe(1) // A
  })

  it('gives zero columns to marks that combine with the previous cell', () => {
    expect(widthOf(0x0301)).toBe(0) // combining acute
    expect(widthOf(0xfe0f)).toBe(0) // variation selector 16
  })

  it('leaves private-use icons single-width', () => {
    // Nerd Font glyphs live in the PUA and are drawn to one cell; the shell's
    // own wcwidth counts them as one, so widening them here would desync us.
    expect(widthOf(0xe0b0)).toBe(1)
  })
})
