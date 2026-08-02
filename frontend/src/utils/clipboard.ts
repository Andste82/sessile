// Clipboard support for the terminal (issue #21).
//
// xterm treats Ctrl+V as the control byte it maps to (^V, readline's
// quoted-insert) and cancels the key event, so the browser never gets to run
// its own paste — only the context menu, which arrives as a `paste` event,
// worked. These helpers identify the paste chords that must be handed back to
// the browser, and the `input` events through which mobile keyboards report a
// paste when they emit no clipboard event at all.

/** The parts of a KeyboardEvent that decide whether it is a paste chord. */
export interface KeyChord {
  type: string
  key: string
  ctrlKey: boolean
  metaKey: boolean
  shiftKey: boolean
  altKey: boolean
}

/**
 * isPasteShortcut reports whether a key event is a clipboard paste chord the
 * browser should be left to perform itself: Ctrl+V (Cmd+V on Apple platforms),
 * its Shift plain-text variant, and Shift+Insert.
 *
 * Matching is on `key`, never `code`: on a Dvorak layout the physical V key
 * types `k`, and matching the position would swallow readline's Ctrl+K. Only
 * keydown/keypress count — keyup carries no default action to preserve, and
 * xterm uses it to refocus the terminal.
 */
export function isPasteShortcut(e: KeyChord, isApple: boolean): boolean {
  if (e.type !== 'keydown' && e.type !== 'keypress') return false
  // Alt is never part of a paste chord, and Ctrl+Alt is AltGr on Windows.
  if (e.altKey) return false
  if (e.key === 'Insert') return e.shiftKey && !e.ctrlKey && !e.metaKey
  if (e.key.toLowerCase() !== 'v') return false
  // On macOS the chord is Cmd+V; Ctrl+V stays quoted-insert.
  return isApple ? e.metaKey && !e.ctrlKey : e.ctrlKey && !e.metaKey
}

/**
 * isCopyShortcut reports whether a key event asks for the selection to be
 * copied: Ctrl+Shift+C and Ctrl+Insert. Plain Ctrl+C is deliberately absent —
 * it is SIGINT and nothing else, whether or not text is selected.
 *
 * Apple platforms are excluded for the letter chord: Cmd+C is the system copy
 * there, and xterm never claims it, so the browser already handles it.
 */
export function isCopyShortcut(e: KeyChord, isApple: boolean): boolean {
  if (e.type !== 'keydown' && e.type !== 'keypress') return false
  if (e.altKey || e.metaKey) return false
  if (e.key === 'Insert') return e.ctrlKey && !e.shiftKey
  if (e.key.toLowerCase() !== 'c') return false
  return !isApple && e.ctrlKey && e.shiftKey
}

/**
 * inputTypes a browser reports when text arrives from the clipboard rather
 * than from typing (see the Input Events spec).
 */
const pasteInputTypes = new Set(['insertFromPaste', 'insertFromPasteAsQuotation'])

/**
 * isPasteInput reports whether an `input`/`beforeinput` event carries pasted
 * text. xterm ignores these — it only forwards `insertText` — which is why a
 * paste offered by a mobile keyboard's clipboard menu never reaches the PTY.
 */
export function isPasteInput(inputType: string): boolean {
  return pasteInputTypes.has(inputType)
}

/** The parts of an InputEvent that can carry pasted text. */
export interface PastePayload {
  data?: string | null
  dataTransfer?: { getData(format: string): string } | null
}

/**
 * pastedText pulls the text out of an input event describing a paste.
 * Browsers disagree about where it lives: a plain-text field usually reports
 * it in `data`, richer editing paths in `dataTransfer`. Either may be absent,
 * in which case the caller has to recover the text after it lands.
 */
export function pastedText(e: PastePayload): string {
  if (typeof e.data === 'string' && e.data.length > 0) return e.data
  return e.dataTransfer?.getData('text/plain') || ''
}

/** isApplePlatform decides which modifier carries the clipboard chords. */
export function isApplePlatform(nav: {
  platform?: string
  userAgent?: string
}): boolean {
  return /Mac|iPhone|iPad|iPod/.test(nav.platform || nav.userAgent || '')
}
