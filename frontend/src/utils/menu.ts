// The two decisions a row-actions menu has to make that are worth testing on
// their own: where to put it, and where the keyboard cursor goes next.
//
// Both live here rather than inside the component because they are pure —
// this project's test suite exercises logic, not mounted components, and
// keeping these callable without a DOM is what makes that possible.

/**
 * shouldDropUp decides whether a menu opens above its trigger instead of
 * below. A file list is scrollable and most of its rows sit in the lower half
 * of the viewport, so a menu that always drops down would routinely render
 * past the fold.
 *
 * It only flips up when there is genuinely more room there: a menu taller than
 * both gaps stays below, where at least its first items are reachable.
 */
export function shouldDropUp(
  triggerTop: number,
  triggerBottom: number,
  menuHeight: number,
  viewportHeight: number,
): boolean {
  const fitsBelow = triggerBottom + menuHeight <= viewportHeight
  if (fitsBelow) return false
  return triggerTop >= menuHeight
}

/**
 * nextMenuIndex moves the keyboard cursor by delta, wrapping at both ends.
 *
 * -1 means "nothing highlighted yet", which is where the menu starts: from
 * there ArrowDown lands on the first item and ArrowUp on the last, so both
 * keys open into a usable position rather than needing a second press.
 */
export function nextMenuIndex(current: number, delta: number, length: number): number {
  if (length <= 0) return -1
  const next = current + delta
  if (next < 0) return length - 1
  if (next >= length) return 0
  return next
}
