// Device capability checks. Used where the right behaviour differs between a
// machine driven by a mouse and one driven by a finger — not by screen size,
// which says nothing about how the user actually points at things.

/** The part of `window` these checks need, so they can be tested directly. */
export interface MediaQueryHost {
  matchMedia?: (query: string) => { matches: boolean }
}

/**
 * hasFinePointer reports whether the primary input is a real pointing device —
 * a mouse or trackpad — rather than a touchscreen.
 *
 * `pointer: fine` alone is true for a stylus, and `hover: hover` alone is true
 * for some touch devices that fake hover on long-press, so both are required.
 * Browsers without matchMedia are treated as touch: the behaviours gated on
 * this are conveniences, and the touch-side cost of guessing wrong (a virtual
 * keyboard opening unbidden) is the higher one.
 */
export function hasFinePointer(win: MediaQueryHost): boolean {
  return win.matchMedia?.('(hover: hover) and (pointer: fine)').matches ?? false
}
