import { defineStore } from 'pinia'
import { ref, computed, watch } from 'vue'
import { api } from '@/api/client'
import type { ServerEvent } from '@/api/events'
import type { AppConfig, CreateSessionBody, Session, UpdateSessionBody } from '@/api/types'
import { useUiStore } from './ui'

/**
 * SessionGroup is one block of the grouped session list. An empty name is the
 * ungrouped block, which the views render without a heading.
 */
export interface SessionGroup {
  name: string
  sessions: Session[]
}

// Ordered ids of the sessions open as terminal tabs. In localStorage, like the
// terminal font size (`ui.ts`), because a reload otherwise leaves the one tab
// the router mounted and throws the rest of the working set away — the sessions
// themselves survive on the server (§9), only this view of them was lost.
//
// Deliberately *not* mirrored across browser tabs through the `storage` event
// the way the font size is: two windows should agree on a preference, but the
// set of open tabs is what each window is working on, so opening one in the
// first window must not make it appear in the second.
const openTabsKey = 'sessile.openTabs'

/**
 * parseOpenTabs reads a stored value as the ordered tab ids. Anything that is
 * not the array of strings we write — a corrupted entry, a cleared one, a
 * shape from a future version — reads as "no tabs", which costs a click per
 * session and cannot show a tab that was never open.
 */
export function parseOpenTabs(value: unknown): string[] {
  if (typeof value !== 'string') return []
  try {
    const parsed: unknown = JSON.parse(value)
    if (!Array.isArray(parsed)) return []
    return parsed.filter((id): id is string => typeof id === 'string' && id !== '')
  } catch {
    return []
  }
}

// Storage is not guaranteed: a browser with cookies blocked throws on access
// rather than returning null, and losing the tab set is not a reason to fail to
// build the store.
function readOpenTabs(): string[] {
  try {
    return parseOpenTabs(localStorage.getItem(openTabsKey))
  } catch {
    return []
  }
}

// Session list + config store. The list is kept live by the event channel
// (§5.1); polling remains as the fallback for while that socket is down.
export const useSessionsStore = defineStore('sessions', () => {
  const sessions = ref<Session[]>([])
  const config = ref<AppConfig | null>(null)
  const loading = ref(false)
  const error = ref<string | null>(null)

  // Ordered ids of sessions opened as terminal tabs, restored from the last
  // page load once the session list says which of them still exist.
  const openTabIds = ref<string[]>([])

  // Ids read from storage but not yet checked against the server's list. They
  // are held here rather than put straight into openTabIds so a deleted
  // session — or one belonging to whoever used this browser before — never
  // shows up as a tab, not even for the one paint before the list arrives.
  // Nulled by the first authoritative snapshot, which is what merges them in.
  let pendingRestore: string[] | null = readOpenTabs()

  // Assigns rather than pushes: the watcher below is on the ref, not deep, so
  // a mutated array would apply on screen and never reach storage.
  function openTab(id: string) {
    if (openTabIds.value.includes(id)) return
    openTabIds.value = [...openTabIds.value, id]
  }

  function closeTab(id: string) {
    openTabIds.value = openTabIds.value.filter((t) => t !== id)
    // The files & processes panel is keyed by session id and would otherwise
    // outlive every tab that ever opened one. removeSession() closes the tab
    // too, so a deleted session is covered by this as well.
    useUiStore().forgetSessionPanel(id)
  }

  watch(openTabIds, (ids) => {
    try {
      localStorage.setItem(openTabsKey, JSON.stringify(ids))
    } catch {
      // Unwritable storage: the tabs still work for this page's lifetime.
    }
  })

  /**
   * setSessions applies a whole-list snapshot from the server — the one place
   * that knows which sessions exist, and therefore the only place that can
   * decide which tabs are real.
   *
   * On the first snapshot it merges the restored ids in stored order, dropping
   * the ones the server does not list. Afterwards it only prunes: a session
   * deleted from another client loses its tab here as well as through
   * `sessionGone`, whichever arrives first. The length guard keeps a poll that
   * changed nothing from writing storage every few seconds.
   */
  function setSessions(list: Session[]) {
    sessions.value = list
    const exists = new Set(list.map((s) => s.id))
    if (pendingRestore) {
      const restored = pendingRestore.filter((id) => exists.has(id))
      pendingRestore = null
      // A tab opened before the list landed — the session this page was
      // deep-linked to — keeps its tab and goes after the restored ones.
      const opened = openTabIds.value.filter((id) => !restored.includes(id))
      openTabIds.value = [...restored, ...opened]
      return
    }
    const kept = openTabIds.value.filter((id) => exists.has(id))
    if (kept.length !== openTabIds.value.length) openTabIds.value = kept
  }

  // Sessions in display order: the ungrouped ones first and unlabelled, then
  // each named group alphabetically. Ungrouped goes first and headerless
  // because "" is the absence of a group, not a group called "Default" — a
  // user who never touches this feature sees exactly the flat list they saw
  // before it existed (§4.11).
  const grouped = computed<SessionGroup[]>(() => {
    const ungrouped = sessions.value.filter((s) => s.group === '')
    const named = new Map<string, Session[]>()
    for (const s of sessions.value) {
      if (s.group === '') continue
      const bucket = named.get(s.group)
      if (bucket) bucket.push(s)
      else named.set(s.group, [s])
    }
    const out: SessionGroup[] = ungrouped.length > 0 ? [{ name: '', sessions: ungrouped }] : []
    for (const name of [...named.keys()].sort((a, b) => a.localeCompare(b))) {
      out.push({ name, sessions: named.get(name)! })
    }
    return out
  })

  // Every group name currently in use, sorted. There is no group entity — this
  // *is* the group list (§4.11), which is why a group vanishes on its own once
  // its last session is deleted. Used for the create/edit dialogs' suggestions
  // and by the grouped views.
  const groupNames = computed(() =>
    [...new Set(sessions.value.map((s) => s.group).filter((g) => g !== ''))].sort((a, b) =>
      a.localeCompare(b),
    ),
  )

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
      setSessions(await api.listSessions())
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
      setSessions(await api.listSessions())
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
        setSessions(ev.sessions)
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

  async function updateSession(id: string, body: UpdateSessionBody) {
    const updated = await api.updateSession(id, body)
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
    grouped,
    groupNames,
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
    updateSession,
    markStopped,
    markAllStopped,
    restartSession,
  }
})
