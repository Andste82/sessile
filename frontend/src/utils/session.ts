import type { Session } from '@/api/types'

/**
 * displayDirectory prefers where the shell actually is over where it was
 * started. cwd follows `cd`; directory is what the session was created with and
 * is all that is left once the session stops.
 */
export function displayDirectory(s: Session): string {
  return s.cwd || s.directory
}

/**
 * displayCommand is the card's "what is running" line: the foreground program,
 * as the kernel named it (§4.7).
 *
 * A fact and nothing more. Whether a session wants something from you is not
 * derived here, or anywhere — a program can be at a prompt, mid-question or
 * halfway through a build and look identical from outside the pty, and a guess
 * that is wrong is worse than a blank line.
 */
export function displayCommand(s: Session): string {
  if (s.status !== 'running') return 'stopped'
  return s.command
}
