import { describe, expect, it } from 'vitest'
import type { IUnicodeVersionProvider } from '@xterm/xterm'
import { applyUnicodeVersion, unicodeVersion } from './unicode'

// A stand-in for the parts of Terminal applyUnicodeVersion touches, so these
// tests exercise the provider the app actually ends up with — the addon's real
// Unicode 11 table with our corrections on top — and not a copy of it.
function terminal() {
  const registered: IUnicodeVersionProvider[] = []
  const unicode = {
    activeVersion: '6',
    register: (p: IUnicodeVersionProvider) => registered.push(p),
    versions: [] as string[],
  }
  return { term: { unicode }, unicode, registered }
}

function provider(): IUnicodeVersionProvider {
  const { term, registered } = terminal()
  applyUnicodeVersion(term)
  return registered[0]
}

function widthOf(codepoint: number): number {
  return provider().wcwidth(codepoint)
}

// How xterm's UnicodeService turns the provider's answers into a column count
// (its getStringCellWidth, which is the arithmetic InputHandler.print places
// cells with). Per-codepoint widths can be asserted through wcwidth; what a
// variation selector does to the cell in front of it only shows up here.
function cellWidthOf(s: string): number {
  const p = provider()
  const widthOf = (props: number) => (props >> 1) & 0b11
  let cells = 0
  let preceding = 0
  for (const ch of s) {
    const props = p.charProperties(ch.codePointAt(0)!, preceding)
    let width = widthOf(props)
    if ((props & 1) !== 0) width -= widthOf(preceding) // joins the cell before
    cells += width
    preceding = props
  }
  return cells
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

  // Issue #46, first half. The addon's table stops at the emoji of Unicode 11,
  // so everything added after it was allotted one cell and drawn across two.
  // glibc's wcwidth answers 2 for all of these, so 2 is the PTY's count too.
  it('widens the emoji added after Unicode 11', () => {
    expect(widthOf(0x1fae0)).toBe(2) // 🫠 melting face (Unicode 14)
    expect(widthOf(0x1fa77)).toBe(2) // 🩷 pink heart (15)
    expect(widthOf(0x1f6dc)).toBe(2) // 🛜 wireless (15)
    expect(widthOf(0x1f7f0)).toBe(2) // 🟰 heavy equals sign (14)
    expect(widthOf(0x1f979)).toBe(2) // 🥹 face holding back tears (14)
  })

  it('widens nothing the table already counts as zero or two', () => {
    // Unicode calls these wide, but they are combining marks: the table's 0 is
    // the answer that matters and the correction must not touch it.
    expect(widthOf(0x3099)).toBe(0) // combining voiced sound mark
    expect(widthOf(0x302a)).toBe(0) // ideographic tone mark
    // Codepoints just outside the widened ranges stay narrow.
    expect(widthOf(0x1f7ef)).toBe(1)
    expect(widthOf(0x1faff)).toBe(1)
  })

  // Issue #46, second half. U+FE0F asks for the emoji presentation of the
  // character in front of it, which every emoji font draws across two cells —
  // but the cell stayed one column wide and the glyph was cut down the middle.
  it('gives an emoji presentation sequence two columns', () => {
    expect(cellWidthOf('✔️')).toBe(2) // ✔️
    expect(cellWidthOf('☑️')).toBe(2) // ☑️
    expect(cellWidthOf('❤️')).toBe(2) // ❤️
    expect(cellWidthOf('⚠️')).toBe(2) // ⚠️
  })

  it('leaves the text presentation of the same character narrow', () => {
    // Without the selector these are text glyphs that fit one cell, and every
    // program in the PTY counts them as one column. CSS keeps the browser from
    // reaching for an emoji font for them — see .terminal-host in style.css.
    expect(cellWidthOf('✔')).toBe(1) // ✔
    expect(cellWidthOf('☑')).toBe(1) // ☑
    expect(cellWidthOf('☠')).toBe(1) // ☠
  })

  it('does not widen a base that is already wide', () => {
    // 👍 is two columns on its own; the selector adds nothing to it. Three
    // columns would be as wrong as one.
    expect(cellWidthOf('\u{1f44d}️')).toBe(2)
    expect(cellWidthOf('\u{1f44d}')).toBe(2)
  })

  it('still lets ordinary combining marks fold into their cell', () => {
    expect(cellWidthOf('é')).toBe(1) // é
    expect(cellWidthOf('中́')).toBe(2) // wide base keeps its two
  })

  it('counts a plain string by its characters', () => {
    expect(cellWidthOf('123456')).toBe(6)
    expect(cellWidthOf('123✅456')).toBe(8)
  })
})
