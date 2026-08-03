// The family name of the terminal's symbol face: Noto Sans Symbols, the subset
// covering arrows, technical symbols, geometric shapes and dingbats (58 kB
// woff2, OFL). The @font-face that loads it is in style.css.
//
// It goes in the terminal's font
// stack after the monospace faces and before the colour emoji ones (issue #46).
//
// ☠ ⚛ ☢ ⌘ ⇧ ■ are *text* presentation characters: one column in the terminal
// and one column to every program in the PTY. Nothing in the monospace stack
// carries them though, and the only fonts that did were the colour emoji fonts
// the stack has to end with (issue #27) — so the browser drew a two-cell emoji
// into a one-cell box and the next character landed on top of it. Which fonts a
// machine happens to have is what decided whether that happened: the same page
// was fine on Android Chrome and broken in desktop Firefox. Carrying a
// monochrome face for these blocks is what makes it neither.
//
// Characters that default to emoji presentation (✅ 🟩 😀) are unaffected: a
// browser reaches for a colour font for those whatever the family order says,
// which is what we want — they are two columns wide and have the room.
export const symbolFontFamily = '"Noto Sans Symbols"'

// A representative glyph from the subset. document.fonts.load needs some text
// to decide what to fetch, and a font is only fetched when a character asks for
// it, so this has to be a character the file actually covers — ☢ U+2622 is in
// the middle of the block this is here for.
const SYMBOL_SAMPLE = '☢'

// How long the terminal waits for the font before opening anyway. A font that
// is slow to arrive is not a reason to sit on a blank terminal; it only costs
// the width of a symbol that appears before it lands.
const FONT_TIMEOUT_MS = 1500

/**
 * loadSymbolFont fetches the symbol font and resolves once it is usable, or
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

  const loaded = fonts
    .load(`${fontSize}px ${symbolFontFamily}`, SYMBOL_SAMPLE)
    .then(() => undefined)
    // A font that fails to load is not fatal: the terminal renders with
    // whatever the machine has, which is what it did before this existed.
    .catch(() => undefined)

  const timeout = new Promise<void>((resolve) => setTimeout(resolve, FONT_TIMEOUT_MS))
  return Promise.race([loaded, timeout])
}
