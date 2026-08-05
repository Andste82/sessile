// Where a scroll gesture belongs. A mouse wheel on a desktop already makes this
// split — xterm scrolls its backlog for a shell, and hands the scroll to the
// program for anything that draws its own screen — and a finger on a phone has
// to make the same one, or a TUI simply does not react to being scrolled.

// The mouse tracking modes xterm reports through `terminal.modes`.
export type MouseTracking = 'none' | 'x10' | 'vt200' | 'drag' | 'any'

export type BufferType = 'normal' | 'alternate'

export type ScrollTarget =
  | 'backlog' // move the terminal's own scrollback
  | 'application' // deliver the scroll to the program as input

/**
 * scrollTargetFor decides which of the two a scroll belongs to, from the state
 * xterm exposes publicly. It follows what xterm's own wheel handler does:
 *
 *  - The alternate screen has no scrollback to move. Scrolling it means telling
 *    the program to scroll, which is what less, htop and vim expect and what
 *    every native terminal does.
 *  - A program that asked for mouse tracking wants the scroll as a mouse
 *    report, whichever screen it draws on — x10 excepted, which reports button
 *    presses only and has nothing to say about a wheel.
 *  - Everything else is a shell with a backlog, where scrolling is ours.
 */
export function scrollTargetFor(
  buffer: BufferType,
  mouse: MouseTracking
): ScrollTarget {
  if (buffer === 'alternate') return 'application'
  if (mouse === 'none' || mouse === 'x10') return 'backlog'
  return 'application'
}
