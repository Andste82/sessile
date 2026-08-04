// The terminal's symbol faces, in the order the font stack lists them. Both
// Noto symbol fonts ship, because they divide the blocks between them rather
// than one superseding the other: ☠ ☢ ⌘ ■ ★ are in Symbols 2, ⚛ ⛧ ⏣ ⌬ only in
// Symbols. The @font-face rules that load them — gated by unicode-range, and
// scaled to fit a cell — are in style.css.
//
// They go in the terminal's font stack after the monospace faces and before the
// colour emoji ones (issue #46).
//
// ☠ ⚛ ☢ ⌘ ⇧ ■ are *text* presentation characters: one column in the terminal and
// one column to every program in the PTY. Nothing in the monospace stack carries
// them though, and the only fonts that did were the colour emoji fonts the stack
// has to end with (issue #27) — so the browser drew a two-cell emoji into a
// one-cell box and the next character landed on top of it. Which fonts a machine
// happens to have is what decided whether that happened: the same page was fine
// on Android Chrome and broken in desktop Firefox. Carrying monochrome faces for
// these blocks is what makes it neither.
//
// The first attempt at this shipped one font chosen from its package's metadata,
// which describes the blocks a subset was cut from rather than what survived the
// cut — the file held 841 glyphs of Roman numerals and none of the characters it
// was shipped for. fonts.symbols.test.ts reads the shipped files themselves now.
//
// Characters that default to emoji presentation (✅ 🟩 😀) are unaffected: a
// browser reaches for a colour font for those whatever the family order says,
// which is what we want — they are two columns wide and have the room.
const SYMBOL_FONTS = [
  // Each sample has to be a character the file actually covers and its
  // unicode-range admits: document.fonts.load decides what to fetch from the
  // text it is given, and a font is only ever fetched for a character that
  // needs it.
  { family: '"Noto Sans Symbols 2"', sample: '☢' },
  { family: '"Noto Sans Symbols"', sample: '⚛' },
] as const

export const symbolFontFamilies: readonly string[] = SYMBOL_FONTS.map((f) => f.family)

// How long the terminal waits for the fonts before opening anyway. A font that
// is slow to arrive is not a reason to sit on a blank terminal; it only costs
// the width of a symbol that appears before it lands.
const FONT_TIMEOUT_MS = 1500

/**
 * loadSymbolFont fetches the symbol faces and resolves once they are usable, or
 * after FONT_TIMEOUT_MS, or immediately where the API does not exist.
 *
 * Waiting matters because xterm measures each character once and caches the
 * width to work out its letter-spacing. A symbol measured against the colour
 * emoji fallback keeps that width until something clears the cache — a resize,
 * or reopening the session — so the glyph it is meant to fix stays wrong.
 */
export function loadSymbolFont(fontSize: number): Promise<void> {
  const fonts = document.fonts as FontFaceSet | undefined
  if (!fonts?.load) return Promise.resolve()

  const loaded = Promise.all(
    SYMBOL_FONTS.map((f) =>
      fonts
        .load(`${fontSize}px ${f.family}`, f.sample)
        // A font that fails to load is not fatal: the terminal renders with
        // whatever the machine has, which is what it did before this existed.
        .catch(() => undefined),
    ),
  ).then(() => undefined)

  const timeout = new Promise<void>((resolve) => setTimeout(resolve, FONT_TIMEOUT_MS))
  return Promise.race([loaded, timeout])
}
