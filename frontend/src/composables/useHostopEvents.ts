// Fans hostop progress events (§4.10, §5.2) out to whichever component
// started the operation it cares about. Separate from stores/sessions.ts on
// purpose — a Delete/Copy's progress belongs to whichever file browser
// started it, not to session list state, so there is nothing to put in that
// store. useSessionEvents.ts pushes into this from the one shared /ws/events
// connection; nothing here opens a socket of its own.
import type { HostopDoneEvent, HostopProgressEvent, HostopStartedEvent } from '@/api/events'

export type HostopEvent = HostopStartedEvent | HostopProgressEvent | HostopDoneEvent

type Listener = (e: HostopEvent) => void

const listeners = new Set<Listener>()

/** Subscribe to every hostop event; returns the unsubscribe function. */
export function onHostopEvent(fn: Listener): () => void {
  listeners.add(fn)
  return () => listeners.delete(fn)
}

/** Dispatch one hostop event to every current subscriber. */
export function emitHostopEvent(e: HostopEvent) {
  for (const fn of listeners) fn(e)
}
