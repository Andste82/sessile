import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { api } from '@/api/client'
import type { AppConfig, CreateSessionBody, Session } from '@/api/types'

// Session list + config store, with background polling for live client counts.
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
  async function refreshSessions() {
    try {
      sessions.value = await api.listSessions()
      error.value = null
    } catch (e) {
      error.value = e instanceof Error ? e.message : String(e)
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

  async function createSession(body: CreateSessionBody): Promise<Session> {
    const created = await api.createSession(body)
    sessions.value = [created, ...sessions.value.filter((s) => s.id !== created.id)]
    return created
  }

  async function deleteSession(id: string) {
    await api.deleteSession(id)
    sessions.value = sessions.value.filter((s) => s.id !== id)
    closeTab(id)
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
    createSession,
    deleteSession,
    renameSession,
    markStopped,
    restartSession,
  }
})
