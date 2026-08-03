import type { ITerminalAddon } from '@xterm/xterm'
import { Unicode11Addon } from '@xterm/addon-unicode11'

// The width table we run xterm with. xterm's built-in default is Unicode 6,
// whose wcwidth gives width 1 to every codepoint above U+FFFF except CJK
// planes 2 and 3 — so an emoji was allotted one cell, drew two, and the next
// character was written over its right half (issue #27). Unicode 11 is also
// the table the programs inside the PTY are working from: glibc's wcwidth has
// been Unicode 9+ for years, so this is what keeps our column count and theirs
// in agreement.
export const unicodeVersion = '11'

// The part of Terminal this needs. Structural, so a test can pass a stub —
// building a real Terminal takes a DOM.
export interface UnicodeCapable {
  loadAddon(addon: ITerminalAddon): void
  unicode: { activeVersion: string }
}

// applyUnicodeVersion registers the Unicode 11 provider and selects it.
// Reading or writing `unicode` at all requires the terminal to have been
// constructed with `allowProposedApi: true`; xterm throws otherwise.
export function applyUnicodeVersion(term: UnicodeCapable) {
  term.loadAddon(new Unicode11Addon())
  term.unicode.activeVersion = unicodeVersion
}
