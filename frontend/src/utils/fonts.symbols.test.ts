import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { inflateSync } from 'node:zlib'
import { createRequire } from 'node:module'

// This file exists because the first font shipped for issue #46 did not contain
// the glyphs it was shipped for. Its package advertises the Unicode blocks the
// subset was cut from; what survived the cut was 841 glyphs of Roman numerals.
// Nothing in the build could tell, because nothing looked inside the file.
//
// So this looks inside the file. It reads the .woff beside the .woff2 the app
// serves — same glyphs, a container that is a zlib blob per table rather than a
// brotli stream, so it can be read here without a font toolchain.

const require = createRequire(import.meta.url)
// Both faces the app ships. They divide the blocks between them, so a character
// only has to be in one of them.
const fontFiles = [
  '@fontsource/noto-sans-symbols-2/files/noto-sans-symbols-2-symbols-400-normal.woff',
  '@fontsource/noto-sans-symbols/files/noto-sans-symbols-symbols-400-normal.woff',
].map((m) => require.resolve(m))

interface Font {
  unitsPerEm: number
  /** Glyph id for a codepoint, or 0 when the font has no glyph for it. */
  glyphOf(codepoint: number): number
  /** Advance width in em. */
  advanceOf(glyph: number): number
}

function readWoff(path: string): Font {
  const b = readFileSync(path)
  if (b.toString('ascii', 0, 4) !== 'wOFF') throw new Error(`${path} is not WOFF1`)

  const tables: Record<string, Buffer> = {}
  const numTables = b.readUInt16BE(12)
  for (let i = 0; i < numTables; i++) {
    const dir = 44 + i * 20
    const tag = b.toString('ascii', dir, dir + 4)
    const offset = b.readUInt32BE(dir + 4)
    const compressed = b.readUInt32BE(dir + 8)
    const original = b.readUInt32BE(dir + 12)
    const raw = b.subarray(offset, offset + compressed)
    tables[tag] = compressed === original ? raw : inflateSync(raw)
  }

  const unitsPerEm = tables.head.readUInt16BE(18)
  const longMetrics = tables.hhea.readUInt16BE(34)

  return {
    unitsPerEm,
    glyphOf: (cp) => lookupCmap(tables.cmap, cp),
    advanceOf: (g) =>
      tables.hmtx.readUInt16BE(Math.min(g, longMetrics - 1) * 4) / unitsPerEm,
  }
}

// Enough of cmap to answer "does this font have this character": the format 4
// subtable for the BMP, format 12 for everything above it.
function lookupCmap(cmap: Buffer, cp: number): number {
  let chosen: { format: number; offset: number } | null = null
  const subtables = cmap.readUInt16BE(2)
  for (let i = 0; i < subtables; i++) {
    const rec = 4 + i * 8
    const platform = cmap.readUInt16BE(rec)
    const encoding = cmap.readUInt16BE(rec + 2)
    const offset = cmap.readUInt32BE(rec + 4)
    const format = cmap.readUInt16BE(offset)
    if (format === 12) return lookupFormat12(cmap, offset, cp)
    if (format === 4 && platform === 3 && (encoding === 1 || encoding === 10)) {
      chosen = { format, offset }
    }
  }
  return chosen ? lookupFormat4(cmap, chosen.offset, cp) : 0
}

function lookupFormat12(cmap: Buffer, offset: number, cp: number): number {
  const groups = cmap.readUInt32BE(offset + 12)
  for (let g = 0; g < groups; g++) {
    const rec = offset + 16 + g * 12
    const start = cmap.readUInt32BE(rec)
    const end = cmap.readUInt32BE(rec + 4)
    if (cp >= start && cp <= end) return cmap.readUInt32BE(rec + 8) + (cp - start)
  }
  return 0
}

function lookupFormat4(cmap: Buffer, offset: number, cp: number): number {
  if (cp > 0xffff) return 0
  const segX2 = cmap.readUInt16BE(offset + 6)
  const ends = offset + 14
  const starts = ends + segX2 + 2
  const deltas = starts + segX2
  const rangeOffsets = deltas + segX2

  for (let s = 0; s < segX2 / 2; s++) {
    if (cp > cmap.readUInt16BE(ends + s * 2)) continue
    const start = cmap.readUInt16BE(starts + s * 2)
    if (cp < start) return 0
    const delta = cmap.readInt16BE(deltas + s * 2)
    const rangeOffset = cmap.readUInt16BE(rangeOffsets + s * 2)
    if (rangeOffset === 0) return (cp + delta) & 0xffff
    const at = rangeOffsets + s * 2 + rangeOffset + (cp - start) * 2
    if (at + 1 >= cmap.length) return 0
    const glyph = cmap.readUInt16BE(at)
    return glyph === 0 ? 0 : (glyph + delta) & 0xffff
  }
  return 0
}

// The characters issue #46 was reported with, from both screenshots.
const reported: Record<string, number> = {
  '☠': 0x2620,
  '⚛': 0x269b,
  '☢': 0x2622,
  '☣': 0x2623,
  '⌘': 0x2318,
  '⇧': 0x21e7,
  '⌦': 0x2326,
  '■': 0x25a0,
  '●': 0x25cf,
  '◐': 0x25d0,
  '★': 0x2605,
  '✦': 0x2726,
  '⏩': 0x23e9,
  '⯑': 0x2bd1,
  '⛧': 0x26e7,
  '⏣': 0x23e3,
  '⌬': 0x232c,
  '✔': 0x2714,
  '☑': 0x2611,
}

// Scaling applied by the @font-face in style.css, and the width of a cell as a
// fraction of the font size — that is what a monospace advance is.
const SIZE_ADJUST = 0.65
const CELL_EM = 0.6

describe('the shipped symbol fonts', () => {
  const fonts = fontFiles.map(readWoff)

  /** The face that carries this codepoint, or null if neither does. */
  function faceFor(cp: number): { font: Font; glyph: number } | null {
    for (const font of fonts) {
      const glyph = font.glyphOf(cp)
      if (glyph !== 0) return { font, glyph }
    }
    return null
  }

  it('have a glyph for every character the issue was reported with', () => {
    const missing = Object.entries(reported)
      .filter(([, cp]) => faceFor(cp) === null)
      .map(([char, cp]) => `${char} U+${cp.toString(16).toUpperCase()}`)

    expect(missing).toEqual([])
  })

  // Having the glyph is only half of it: a glyph wider than its cell spills into
  // the next one, which is the whole complaint. size-adjust is what brings them
  // down to a cell, so it has to be checked against the same files.
  it('draw them inside a cell once scaled', () => {
    const tooWide = Object.entries(reported)
      .map(([char, cp]) => {
        const face = faceFor(cp)!
        const drawn = face.font.advanceOf(face.glyph) * SIZE_ADJUST
        return { char, cells: drawn / CELL_EM }
      })
      // The widest of them (the ⌫ ⌦ class, near a full em unscaled) stay a
      // little over even scaled; they are covered by the monospace faces ahead
      // of these on every machine that has them. A quarter of a cell over is
      // the most that goes unnoticed.
      .filter((g) => g.cells > 1.31)
      .map((g) => `${g.char} at ${g.cells.toFixed(2)} cells`)

    expect(tooWide).toEqual([])
  })

  it('are scaled by enough to matter', () => {
    // Unscaled, the median glyph is around 0.85 em against a 0.6 em cell. If
    // size-adjust ever drifts back towards 100% this test is the tripwire.
    const skull = faceFor(0x2620)!
    const drawn = skull.font.advanceOf(skull.glyph) * SIZE_ADJUST
    expect(drawn).toBeLessThanOrEqual(CELL_EM * 1.02)
  })
})
