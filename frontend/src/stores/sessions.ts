import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { api } from '@/api/client'
import type { ServerEvent } from '@/api/events'
import type { AppConfig, CreateSessionBody, Session } from '@/api/types'

// Session list + config store. The list is kept live by the event channel
// (§5.1); polling remains as the fallback for while that socket is down.
export const useSessionsStore = defineStore('sessions', () => {
  const sessions = ref<Session[]>([])
  const config = ref<AppConfig | null>(null)
  const loading = ref(false)
  const error = ref<string | null>(null)

  // Ordered ids of sessions opened as terminal tabs.
  const openTabIds = ref<string[]>([])

  function openTab(id: string) {
    if (!openTabIds.value.includes(id)) openTabIds.value.push(id)
  }

  function closeTab(id: string) {
    openTabIds.value = openTabIds.value.filter((t) => t !== id)
  }

  const byId = computed(
    () => (id: string) => sessions.value.find((s) => s.id === id) ?? null,
  )

  // Records the failure instead of rejecting, like the session fetches do. Two
  // of the three callers fire this without awaiting, so a rejection here went
  // nowhere: the shell list stayed empty and the New session dialog offered
  // nothing, with no indication why.
  async function fetchConfig() {
    try {
      config.value = await api.config()
      error.value = null
    } catch (e) {
      error.value = e instanceof Error ? e.message : String(e)
    }
  }

  async function fetchSessions() {
    loading.value = true
    error.value = null
    try {
      sessions.value = await api.listSessions()
    } catch (e) {
      error.value = e instanceof Error ? e.message : String(e)
    } finally {
      loading.value = false
    }
  }

  // refreshSessions updates the list without toggling the loading flag, for
  // background polling (keeps client counts live).
  //
  // A failure is not left to sit on the stale list. The backend is the only
  // thing that runs a shell, so if it cannot be reached, none of the sessions
  // are running whatever the last successful poll said — see markAllStopped.
  async function refreshSessions() {
    try {
      sessions.value = await api.listSessions()
      error.value = null
    } catch (e) {
      error.value = e instanceof Error ? e.message : String(e)
      markAllStopped()
    }
  }

  let pollTimer: ReturnType<typeof setInterval> | null = null

  function startPolling(intervalMs = 5000) {
    stopPolling()
    pollTimer = setInterval(refreshSessions, intervalMs)
  }

  function stopPolling() {
    if (pollTimer) {
      clearInterval(pollTimer)
      pollTimer = null
    }
  }

  // Applies one frame from the event channel (§5.1).
  //
  // This lives in the store rather than in the composable that owns the socket
  // so it can be tested: the repo has no component tests, and everything worth
  // asserting about the channel is which of these three things it does.
  function applyEvent(ev: ServerEvent) {
    switch (ev.type) {
      case 'sessions':
        // The snapshot is the whole truth, including a session this client
        // never saw created and one it never saw deleted.
        sessions.value = ev.sessions
        error.value = null
        break
      case 'session':
        upsertSession(ev.session)
        error.value = null
        break
      case 'sessionGone':
        removeSession(ev.sessionId)
        error.value = null
        break
    }
  }

  function removeSession(id: string) {
    sessions.value = sessions.value.filter((s) => s.id !== id)
    closeTab(id)
  }

  // Inserts or replaces a single session. Fetching one by id (a deep link into
  // a terminal, say) otherwise left it out of the list, so the tab bar showed
  // "session" and the window title fell back to the plain route title until the
  // next poll.
  function upsertSession(session: Session) {
    const idx = sessions.value.findIndex((s) => s.id === session.id)
    if (idx === -1) sessions.value = [session, ...sessions.value]
    else sessions.value = sessions.value.map((s) => (s.id === session.id ? session : s))
  }

  async function createSession(body: CreateSessionBody): Promise<Session> {
    const created = await api.createSession(body)
    sessions.value = [created, ...sessions.value.filter((s) => s.id !== created.id)]
    return created
  }

  async function deleteSession(id: string) {
    await api.deleteSession(id)
    removeSession(id)
  }

  async function renameSession(id: string, name: string) {
    const updated = await api.renameSession(id, name)
    sessions.value = sessions.value.map((s) => (s.id === id ? updated : s))
    return updated
  }

  // Records a session whose shell exited while its terminal was open. The list
  // is only polled from the dashboard, so without this the tab and the sidebar
  // keep showing a running session right next to the "session ended" banner,
  // until the user navigates away and back.
  function markStopped(id: string) {
    sessions.value = sessions.value.map((s) =>
      s.id === id ? { ...s, status: 'stopped', clientCount: 0 } : s,
    )
  }

  // Marks every session stopped, for when the backend itself has gone away.
  //
  // Only the terminal that happened to be on screen learned about a backend
  // restart, through its own WebSocket closing; every other session kept the
  // green dot from the last successful poll until the user clicked it. Since a
  // shell only exists inside the backend process, an unreachable backend means
  // none of them are running — and a session that comes back is corrected by
  // the next successful poll, which is a fresh snapshot of the truth.
  function markAllStopped() {
    if (!sessions.value.some((s) => s.status === 'running' || s.clientCount > 0)) return
    sessions.value = sessions.value.map((s) => ({
      ...s,
      status: 'stopped',
      clientCount: 0,
    }))
  }

  // Gives a stopped session a new shell under the same id, with its scrollback
  // and command history restored. The id is unchanged, so any open tab keeps
  // pointing at the same session and only needs to reconnect.
  async function restartSession(id: string) {
    const restarted = await api.restartSession(id)
    sessions.value = sessions.value.map((s) => (s.id === id ? restarted : s))
    return restarted
  }

  return {
    sessions,
    config,
    loading,
    error,
    byId,
    openTabIds,
    openTab,
    closeTab,
    fetchConfig,
    fetchSessions,
    refreshSessions,
    startPolling,
    stopPolling,
    applyEvent,
    removeSession,
    upsertSession,
    createSession,
    deleteSession,
    renameSession,
    markStopped,
    markAllStopped,
    restartSession,
  }
})
