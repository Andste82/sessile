// Encoding of special keys and modifier combinations into the VT/xterm byte
// sequences a PTY expects. Used by the on-screen key bar (issue #10) so mobile
// users — whose soft keyboards lack Esc/Tab/Ctrl/arrows — can send them and
// build combos like Ctrl-C or Shift-Tab.

export interface Mods {
  ctrl: boolean
  alt: boolean
  shift: boolean
}

export type ModName = keyof Mods

export const noMods: Mods = { ctrl: false, alt: false, shift: false }

export function anyMod(m: Mods): boolean {
  return m.ctrl || m.alt || m.shift
}

// xterm modifier parameter: 1 + shift(1) + alt(2) + ctrl(4).
function modParam(m: Mods): number {
  return 1 + (m.shift ? 1 : 0) + (m.alt ? 2 : 0) + (m.ctrl ? 4 : 0)
}

// A CSI cursor/function key: `ESC [ final` unmodified, `ESC [ 1 ; p final`
// with modifiers (e.g. Ctrl-Right → ESC [ 1 ; 5 C).
function csiFinal(final: string, m: Mods): string {
  const p = modParam(m)
  return p === 1 ? `\x1b[${final}` : `\x1b[1;${p}${final}`
}

// A CSI tilde key (Delete, PageUp, …): `ESC [ n ~` or `ESC [ n ; p ~`.
function csiTilde(n: number, m: Mods): string {
  const p = modParam(m)
  return p === 1 ? `\x1b[${n}~` : `\x1b[${n};${p}~`
}

// Apply armed modifiers to a single printable character typed on the real
// keyboard. Ctrl maps @A–Z[\]^_ / a–z to control codes 0–31; Alt prefixes ESC;
// Shift upper-cases. Multi-char input (paste) is passed through unchanged.
export function applyModifiers(data: string, m: Mods): string {
  if (data.length !== 1 || !anyMod(m)) return data
  let ch = data
  if (m.shift) ch = ch.toUpperCase()
  if (m.ctrl) {
    const code = ch.toUpperCase().charCodeAt(0)
    if (code >= 64 && code <= 95) ch = String.fromCharCode(code & 0x1f)
    else if (code >= 97 && code <= 122) ch = String.fromCharCode(code & 0x1f)
  }
  if (m.alt) ch = '\x1b' + ch
  return ch
}

export type SpecialKey =
  | 'Escape'
  | 'Tab'
  | 'Delete'
  | 'Up'
  | 'Down'
  | 'Right'
  | 'Left'
  | 'Home'
  | 'End'
  | 'PageUp'
  | 'PageDown'
  | 'F1'
  | 'F2'
  | 'F3'
  | 'F4'
  | 'F5'
  | 'F6'
  | 'F7'
  | 'F8'
  | 'F9'
  | 'F10'
  | 'F11'
  | 'F12'

// F5–F12 tilde numbers (F1–F4 use the SS3 ESC O P–S form below).
const fTilde: Record<string, number> = {
  F5: 15,
  F6: 17,
  F7: 18,
  F8: 19,
  F9: 20,
  F10: 21,
  F11: 23,
  F12: 24,
}

// Encode a named special key, applying modifiers where the terminal defines a
// combined form. Function keys are sent unmodified.
export function encodeSpecial(key: SpecialKey, m: Mods = noMods): string {
  switch (key) {
    case 'Escape':
      return '\x1b'
    case 'Tab':
      return m.shift ? '\x1b[Z' : '\t'
    case 'Up':
      return csiFinal('A', m)
    case 'Down':
      return csiFinal('B', m)
    case 'Right':
      return csiFinal('C', m)
    case 'Left':
      return csiFinal('D', m)
    case 'Home':
      return csiFinal('H', m)
    case 'End':
      return csiFinal('F', m)
    case 'Delete':
      return csiTilde(3, m)
    case 'PageUp':
      return csiTilde(5, m)
    case 'PageDown':
      return csiTilde(6, m)
    case 'F1':
      return '\x1bOP'
    case 'F2':
      return '\x1bOQ'
    case 'F3':
      return '\x1bOR'
    case 'F4':
      return '\x1bOS'
    default:
      return `\x1b[${fTilde[key]}~`
  }
}
