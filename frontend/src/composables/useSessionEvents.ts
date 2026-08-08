import { onMounted, onUnmounted } from 'vue'
import { eventsWsURL, parseEvent } from '@/api/events'
import { useSessionsStore } from '@/stores/sessions'
import { backoffSteps } from '@/utils/reconnect'

// How often to poll while the event channel is down. Faster than the 5 s this
// replaced, because polling is now the degraded path and not the normal one:
// it runs while something is already wrong, and should not also be slow.
const fallbackPollMs = 3000

/**
 * useSessionEvents keeps the session list live from /ws/events (§5.1).
 *
 * The channel replaces the app-wide 5 s poll. Polling stays as the fallback for
 * exactly as long as the socket is down, because the socket closing is itself
 * the signal that the list may be stale — and an unreachable backend still has
 * to grey out every session, which only the failing poll can conclude.
 *
 * Mounted once, at the app root: the list is on screen everywhere, since the
 * sidebar and the terminal tab bar both draw indicators from it.
 */
export function useSessionEvents() {
  const store = useSessionsStore()

  let ws: WebSocket | null = null
  let retryTimer: ReturnType<typeof setTimeout> | null = null
  let attempt = 0
  let closed = false

  function connect() {
    if (closed) return

    // Poll until the channel proves itself. The first snapshot stops it, so a
    // healthy connection costs one request; a backend that never answers keeps
    // the list as fresh as it can be.
    store.startPolling(fallbackPollMs)

    let socket: WebSocket
    try {
      socket = new WebSocket(eventsWsURL())
    } catch {
      scheduleRetry()
      return
    }
    ws = socket

    socket.onmessage = (ev) => {
      if (typeof ev.data !== 'string') return // the channel is text-only (§5.1)
      const parsed = parseEvent(ev.data)
      if (!parsed) return
      store.applyEvent(parsed)
      // Stop polling on the first frame that carried state, not on open: a
      // socket that connects and then says nothing is not yet a working
      // channel, and dropping the fallback at open would leave the list frozen.
      store.stopPolling()
      attempt = 0
    }

    socket.onclose = () => {
      if (ws === socket) ws = null
      scheduleRetry()
    }

    // onerror is followed by onclose, which does the scheduling. Swallowing it
    // here only keeps it off the console as an unhandled event.
    socket.onerror = () => {}
  }

  function scheduleRetry() {
    if (closed || retryTimer) return
    // Unlike a terminal socket, there is no close code that means "stop trying"
    // (§5.1 defines no session-gone case for this channel), so the only
    // decision left is how long to wait — the same ladder terminals use.
    const delay = backoffSteps[Math.min(attempt, backoffSteps.length - 1)]
    attempt += 1
    store.startPolling(fallbackPollMs)
    retryTimer = setTimeout(() => {
      retryTimer = null
      connect()
    }, delay)
  }

  function stop() {
    closed = true
    if (retryTimer) {
      clearTimeout(retryTimer)
      retryTimer = null
    }
    if (ws) {
      // Drop the handlers first: closing fires onclose, which would otherwise
      // schedule a reconnect for a component that is going away.
      ws.onmessage = null
      ws.onclose = null
      ws.onerror = null
      ws.close()
      ws = null
    }
    store.stopPolling()
  }

  onMounted(connect)
  onUnmounted(stop)
}
