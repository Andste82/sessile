import type { Activity, Session, Status } from '@/api/types'

/**
 * What the indicator shows. Four states rather than the backend's three plus a
 * status, because "stopped" and the three activities are mutually exclusive
 * from the viewer's side and one enum is easier to render than two.
 */
export type Indicator = 'stopped' | 'idle' | 'busy' | 'waiting'

/**
 * indicatorFor collapses status and activity into one value.
 *
 * Status wins. A stopped session's activity is already cleared server-side, but
 * the two fields arrive together and a client that trusted activity alone would
 * paint a session green for the moment between a stale list and the next frame.
 * Anything unrecognised falls to idle rather than disappearing.
 */
export function indicatorFor(status: Status, activity: Activity): Indicator {
  if (status !== 'running') return 'stopped'
  switch (activity) {
    case 'busy':
      return 'busy'
    case 'waiting':
      return 'waiting'
    default:
      return 'idle'
  }
}

/**
 * indicatorLabel is the tooltip and the accessible name. It says what the state
 * means rather than naming it: "busy" alone does not tell a first-time reader
 * that the dot is about the program in the session and not the connection.
 */
export function indicatorLabel(indicator: Indicator): string {
  switch (indicator) {
    case 'stopped':
      return 'stopped'
    case 'busy':
      return 'working'
    case 'waiting':
      return 'waiting for input'
    default:
      return 'idle at the prompt'
  }
}

/**
 * activitySummary is the card's one-line "what is happening" text.
 *
 * The program name comes first because it is the part that is measured rather
 * than inferred (§4.7). Where it is unknown — no /proc, or the lookup lost a
 * race with an exiting process — the state alone still says something true.
 */
export function activitySummary(s: Session): string {
  const indicator = indicatorFor(s.status, s.activity)
  if (indicator === 'stopped') return 'stopped'
  const state = indicatorLabel(indicator)
  return s.command ? `${s.command} · ${state}` : state
}

/**
 * displayDirectory prefers where the shell actually is over where it was
 * started. cwd follows `cd`; directory is what the session was created with and
 * is all that is left once the session stops.
 */
export function displayDirectory(s: Session): string {
  return s.cwd || s.directory
}
