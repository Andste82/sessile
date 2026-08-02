// IME / predictive-text support for the terminal input.
//
// Mobile keyboards do not type characters — they compose them. Gboard and the
// Samsung keyboard fire an `input` event for every intermediate state of a word
// while it is being predicted or auto-completed, then commit the final word at
// compositionend. Only that committed text is real input; everything before it
// is a preview the terminal must never see.

/**
 * inputTypes a browser reports for text belonging to an in-flight composition
 * rather than to committed input (see the Input Events spec).
 */
const compositionInputTypes = new Set([
  'insertCompositionText',
  'deleteCompositionText',
  'insertFromComposition',
  'deleteByComposition',
])

/**
 * isCompositionArtifact reports whether an `input` event carries an
 * intermediate composition state that must be withheld from the terminal.
 *
 * Both checks are needed. `composing` catches keyboards that report a plain
 * `insertText` or `deleteContentBackward` in the middle of a composition —
 * Gboard does exactly this when it swaps a half-typed word for the suggestion
 * the user tapped — and the inputType check catches keyboards that emit
 * composition input without a surrounding compositionstart/end pair.
 */
export function isCompositionArtifact(
  composing: boolean,
  inputType: string
): boolean {
  return composing || compositionInputTypes.has(inputType)
}

/** The parts of a KeyboardEvent that place it inside or outside an IME. */
export interface ImeKey {
  type: string
  key: string
  keyCode: number
  isComposing: boolean
}

/**
 * isImeKey reports whether a key event belongs to the keyboard's composition
 * machinery rather than to the terminal.
 *
 * Android soft keyboards report every composing keystroke as keyCode 229 with
 * no usable `key`, so there is nothing for the terminal to send; worse, xterm
 * answers each one by diffing its helper textarea on a timer and sending the
 * difference, which is how half-typed words leak out. Keyups are excluded:
 * xterm uses them to refocus and to reset its own key-in-flight flag.
 */
export function isImeKey(e: ImeKey): boolean {
  if (e.type === 'keyup') return false
  return e.isComposing || e.keyCode === 229 || e.key === 'Process'
}

// Keys that only arm a modifier: pressing one is not the user finishing a word.
const modifierKeys = new Set([
  'Shift',
  'Control',
  'Alt',
  'AltGraph',
  'Meta',
  'CapsLock',
])

/**
 * shouldFlushIme reports whether a key event ends whatever the keyboard has
 * staged. A real key — Enter, an arrow, Ctrl-C — means the user is done with
 * the word, so it must reach the PTY before the key itself does.
 */
export function shouldFlushIme(e: ImeKey, imeActive: boolean): boolean {
  if (!imeActive || e.type !== 'keydown') return false
  if (isImeKey(e)) return false
  return !modifierKeys.has(e.key)
}
