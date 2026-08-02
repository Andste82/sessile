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
