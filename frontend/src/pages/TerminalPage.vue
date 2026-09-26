<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { CommandLineIcon, FolderIcon, SparklesIcon } from '@heroicons/vue/24/outline'
import TaskSidePanel from '@/components/TaskSidePanel.vue'
import { useTasksStore } from '@/stores/tasks'
import { hasFinePointer } from '@/utils/device'
import { useWideScreen } from '@/composables/useBreakpoint'
import TerminalView from '@/components/TerminalView.vue'
import TabBar from '@/components/TabBar.vue'
import HostKeyTrustDialog from '@/components/HostKeyTrustDialog.vue'
import FileBrowserPanel from '@/components/FileBrowserPanel.vue'
import { useSessionsStore } from '@/stores/sessions'
import { useUiStore } from '@/stores/ui'
import { ApiRequestError, api, isAlreadyRunning } from '@/api/client'
import type { HostKeyErrorDetails, Session, Task } from '@/api/types'
import type { ConnStatus } from '@/composables/useTerminal'

const route = useRoute()
const store = useSessionsStore()
const ui = useUiStore()

const id = computed(() => String(route.params.id))
const session = ref<Session | null>(null)
const conn = ref<ConnStatus>('connecting')
const loadError = ref<string | null>(null)

const restarting = ref(false)
const restartError = ref<string | null>(null)
// A task session's restart can start the agent fresh or rebuild its
// devcontainer (§4.12.6); both default off — a plain restart resumes.
const restartFresh = ref(false)
const rebuildContainer = ref(false)
const restartMode = ref<'' | 'auto' | 'plan' | 'normal'>('')
const task = ref<Task | null>(null)
const isTask = computed(() => !!session.value?.taskId)

// The task panel (§4.17.4): a column on a desktop, a sheet on a phone. Open by
// default where there is room, and opened by an approval request wherever it
// arrives — a write call that nobody sees just times out.
const tasksStore = useTasksStore()
const touch = !hasFinePointer(window)
const taskPanelOpen = ref(!touch && window.innerWidth >= 1024)
const pending = computed(() => tasksStore.pendingCount(session.value?.taskId))
watch(pending, (n, old) => {
  if (n > (old ?? 0)) taskPanelOpen.value = true
})
// The task's two panes (§4.12, E10): the agent, which runs on the sessile
// server, and the user's own shell on the task's host. Side by side where
// there is room, two tabs where there is not. The split is remembered per
// browser: it is a view preference, like the panel's own open state.
const wide = useWideScreen()
const shellId = computed(() => task.value?.shellSessionId ?? '')
const splitAvailable = computed(
  () => isTask.value && shellId.value !== '' && session.value?.targetType === 'local',
)
// A task on a host, whose shell pane is not there (or was never opened).
const canOpenShell = computed(
  () =>
    isTask.value &&
    session.value?.targetType === 'local' &&
    !!task.value &&
    task.value.spec.target !== 'local' &&
    !shellId.value,
)
const splitKey = 'sessile.taskSplit'
const splitPct = ref(readSplit())
const pane = ref<'agent' | 'shell'>('agent')

function readSplit(): number {
  try {
    const v = Number(localStorage.getItem(splitKey))
    return v >= 20 && v <= 80 ? v : 55
  } catch {
    return 55
  }
}

// Dragging the divider: pointer events on the page, so the pointer keeps
// being tracked even when it leaves the thin divider itself.
const dragging = ref(false)
function startDrag(e: PointerEvent) {
  dragging.value = true
  ;(e.target as HTMLElement).setPointerCapture?.(e.pointerId)
}
function onDrag(e: PointerEvent) {
  if (!dragging.value) return
  const host = document.getElementById('task-panes')
  if (!host) return
  const rect = host.getBoundingClientRect()
  const pct = ((e.clientX - rect.left) / rect.width) * 100
  splitPct.value = Math.min(80, Math.max(20, Math.round(pct)))
}
function endDrag() {
  if (!dragging.value) return
  dragging.value = false
  try {
    localStorage.setItem(splitKey, String(splitPct.value))
  } catch {
    // A browser that refuses storage just forgets the split.
  }
}

// Opening the shell pane for a task that has none yet, or whose pane stopped.
const openingShell = ref(false)
async function openShell() {
  if (!task.value || openingShell.value) return
  openingShell.value = true
  try {
    const s = await api.openTaskShell(task.value.id)
    store.upsertSession(s)
    task.value = { ...task.value, shellSessionId: s.id }
    pane.value = 'shell'
  } catch (e) {
    restartError.value = e instanceof Error ? e.message : String(e)
  } finally {
    openingShell.value = false
  }
}

// Same host-key-changed recovery gap as DashboardPage.vue's restart button —
// see its comment. Kept local to this page rather than shared, since the two
// restart call sites otherwise have nothing in common to factor out.
const pendingHostKey = ref<{ hostId: string; hostName: string; details: HostKeyErrorDetails } | null>(
  null,
)
// A restart keeps the session id, so the id alone cannot re-key TerminalView.
// Bumping this forces a remount, which is what makes useTerminal open a fresh
// WebSocket and replay the restored scrollback.
const reloadNonce = ref(0)

// Belongs to this session's own view, not the sidebar/dashboard — the same
// reasoning as the foreground/title lines being per-card rather than a
// standalone page (§4.10's design note). Kept in the ui store rather than
// here because the router reuses this one component for every open tab: a ref
// on the page would be one panel shared by all of them.
const filesPanelOpen = computed({
  get: () => ui.panelFor(id.value).open,
  set: (open: boolean) => ui.setPanelOpen(id.value, open),
})

async function restart() {
  if (restarting.value) return
  restarting.value = true
  restartError.value = null
  try {
    session.value = await store.restartSession(
      id.value,
      isTask.value
        ? {
            fresh: restartFresh.value,
            rebuildContainer: rebuildContainer.value,
            ...(restartMode.value ? { mode: restartMode.value } : {}),
          }
        : undefined,
    )
    restartFresh.value = false
    rebuildContainer.value = false
    restartMode.value = ''
    if (task.value) void api.getTask(task.value.id).then((t) => (task.value = t))
    reloadNonce.value++
  } catch (e) {
    // Another browser on this session got there first. Not a failure: this
    // click asked for a live session and there is one, so attach to it instead
    // of reporting "session is already running" at someone who cannot act on it.
    if (isAlreadyRunning(e)) {
      await store.refreshSessions()
      session.value = store.byId(id.value) ?? session.value
      reloadNonce.value++
      return
    }
    const details = e instanceof ApiRequestError ? e.hostKeyDetails() : null
    if (details) {
      pendingHostKey.value = {
        hostId: session.value?.hostId ?? '',
        hostName: session.value?.hostDisplayName || 'this host',
        details,
      }
    } else {
      restartError.value = e instanceof Error ? e.message : String(e)
    }
  } finally {
    restarting.value = false
  }
}

function retryRestartAfterTrust() {
  pendingHostKey.value = null
  void restart()
}

// A task session's own record, for the restart options (and the task panel).
watch(
  () => session.value?.taskId,
  async (taskId) => {
    task.value = null
    if (!taskId) return
    try {
      task.value = await api.getTask(taskId)
    } catch {
      // The restart still works without it; only the rebuild option hides.
    }
  },
  { immediate: true },
)

async function loadSession(sessionId: string) {
  store.openTab(sessionId)
  loadError.value = null
  session.value = store.byId(sessionId)
  if (session.value) return
  try {
    session.value = await api.getSession(sessionId)
    store.upsertSession(session.value)
  } catch (e) {
    loadError.value = e instanceof Error ? e.message : String(e)
  }
}

onMounted(async () => {
  // Deliberately not awaited: the terminal must not wait on either.
  if (!store.config) void store.fetchConfig()
  if (store.sessions.length === 0) void store.fetchSessions()
  await loadSession(id.value)
})

// Handle navigating directly between tabs (component is reused).
watch(id, (newId) => loadSession(newId))

// The WebSocket is the first thing to learn that this session ended. Push that
// into the store so the tab and sidebar dots agree with the banner below.
watch(conn, (c) => {
  if (c !== 'exited') return
  store.markStopped(id.value)
  session.value = store.byId(id.value) ?? session.value
})

// …and the first thing to learn that the backend as a whole went away or came
// back — a socket drop beats the next poll tick by up to its whole interval.
// The refresh decides which it was: it fails while the backend is down, which
// greys every session out, and returns the real state once it is back.
watch(conn, async (c) => {
  if (c !== 'connected' && c !== 'disconnected') return
  await store.refreshSessions()
  session.value = store.byId(id.value) ?? session.value
})

// A restart from another browser reaches an attached client over its own socket
// (the server moves it to the new shell), but not one that has no socket left —
// arriving while the session was stopped is refused, so a page opened then never
// had one. For those the polled list is the only signal, and it is worth acting
// on: the session is running, so reconnect instead of showing a dead terminal
// with a button that would only be told "already running".
watch(
  () => store.byId(id.value)?.status,
  (status) => {
    if (status !== 'running' || conn.value !== 'exited') return
    session.value = store.byId(id.value) ?? session.value
    reloadNonce.value++
  },
)
</script>

<template>
  <div class="flex h-full flex-col bg-slate-900">
    <TabBar :conn="conn" />

    <div class="flex min-h-0 flex-1">
      <!-- min-w-0: this column is a *row* flex item, so its automatic minimum
           size is its content's min-content width — which is the key bar's, and
           that is wider than a phone screen. Without it, opening the key bar
           widens the whole terminal column past the viewport (clipped by the
           app shell's overflow-hidden) instead of letting the bar's own
           overflow-x-auto rows scroll. Before the files panel put this column
           in a row, it was a column flex item, where no such minimum applies. -->
      <div class="relative min-h-0 min-w-0 flex-1">
        <p v-if="loadError" class="p-6 text-sm text-rose-400">{{ loadError }}</p>

        <!-- Two panes for a task: its agent here on the server, and the
             user's own shell on its host. -->
        <div v-else-if="splitAvailable" id="task-panes" class="flex h-full min-h-0 flex-col md:flex-row"
             @pointermove="onDrag" @pointerup="endDrag" @pointercancel="endDrag">
          <div v-if="!wide" class="flex shrink-0 gap-1 border-b border-slate-800 px-2 py-1 text-xs">
            <button
              type="button"
              class="rounded px-3 py-1.5"
              :class="pane === 'agent' ? 'bg-slate-800 text-slate-100' : 'text-slate-400'"
              @click="pane = 'agent'"
            >
              Agent
            </button>
            <button
              type="button"
              class="rounded px-3 py-1.5"
              :class="pane === 'shell' ? 'bg-slate-800 text-slate-100' : 'text-slate-400'"
              @click="pane = 'shell'"
            >
              Shell on {{ session?.hostDisplayName || 'the host' }}
            </button>
          </div>

          <div
            v-show="wide || pane === 'agent'"
            class="min-h-0 min-w-0 flex-1"
            :style="wide ? { flex: `0 0 ${splitPct}%` } : undefined"
          >
            <TerminalView
              :key="`${id}:${reloadNonce}`"
              :session-id="id"
              class="h-full p-2"
              @status="conn = $event"
            />
          </div>

          <div
            v-if="wide"
            class="group w-1 shrink-0 cursor-col-resize bg-slate-800 hover:bg-emerald-600"
            :class="{ 'bg-emerald-600': dragging }"
            title="Drag to resize"
            @pointerdown="startDrag"
          />

          <div v-show="wide || pane === 'shell'" class="min-h-0 min-w-0 flex-1">
            <TerminalView :key="`shell:${shellId}`" :session-id="shellId" class="h-full p-2" />
          </div>
        </div>

        <TerminalView
          v-else
          :key="`${id}:${reloadNonce}`"
          :session-id="id"
          class="h-full p-2"
          @status="conn = $event"
        />

        <button
          type="button"
          class="absolute right-2 top-2 z-10 flex h-8 w-8 items-center justify-center rounded-md bg-slate-800/80 text-slate-300 shadow hover:bg-slate-700 hover:text-slate-100"
          :class="{ 'text-emerald-400': filesPanelOpen }"
          title="Files &amp; processes"
          @click="filesPanelOpen = !filesPanelOpen"
        >
          <FolderIcon class="h-4 w-4" />
        </button>
        <!-- A task whose shell pane was closed, or never opened, gets it back
             here rather than from a menu. -->
        <button
          v-if="canOpenShell"
          type="button"
          class="absolute right-24 top-2 z-10 flex h-8 items-center gap-1 rounded-md bg-slate-800/80 px-2 text-xs text-slate-300 shadow hover:bg-slate-700 hover:text-slate-100 disabled:opacity-60"
          :disabled="openingShell"
          title="Open a shell on this task's host"
          @click="openShell()"
        >
          <CommandLineIcon class="h-4 w-4" /> Shell
        </button>
        <button
          v-if="isTask"
          type="button"
          class="absolute right-12 top-2 z-10 flex h-8 w-8 items-center justify-center rounded-md bg-slate-800/80 text-slate-300 shadow hover:bg-slate-700 hover:text-slate-100"
          :class="{ 'text-emerald-400': taskPanelOpen }"
          :title="pending ? `Task — ${pending} waiting for approval` : 'Task'"
          @click="taskPanelOpen = !taskPanelOpen"
        >
          <SparklesIcon class="h-4 w-4" />
          <span
            v-if="pending"
            class="absolute -right-1 -top-1 flex h-4 min-w-4 items-center justify-center rounded-full bg-amber-500 px-1 text-[10px] font-semibold text-slate-900"
            >{{ pending }}</span
          >
        </button>

        <div
          v-if="conn === 'exited'"
          class="absolute inset-x-0 top-0 z-10 flex justify-center p-3"
        >
          <div
            class="flex flex-wrap items-center justify-center gap-x-3 gap-y-1.5 rounded-md bg-slate-800 px-3 py-1.5 text-sm text-slate-300 shadow"
          >
            <span>Session ended — the shell process has exited.</span>
            <button
              type="button"
              class="rounded bg-emerald-600 px-2.5 py-1 text-xs font-medium text-white hover:bg-emerald-500 focus:outline-none focus-visible:ring-2 focus-visible:ring-emerald-400 disabled:opacity-60"
              :disabled="restarting"
              @click="restart"
            >
              {{ restarting ? 'Restarting…' : isTask ? 'Restart task' : 'Restart session' }}
            </button>
            <template v-if="isTask">
              <label class="flex items-center gap-1.5 text-xs text-slate-400">
                <input v-model="restartFresh" type="checkbox" class="accent-emerald-400" />
                Start the agent fresh
              </label>
              <label v-if="task?.spec.devcontainer" class="flex items-center gap-1.5 text-xs text-slate-400">
                <input v-model="rebuildContainer" type="checkbox" class="accent-emerald-400" />
                Rebuild the container
              </label>
              <!-- A task keeps the mode it was created with, and a restart
                   is where the user changes their mind about it. -->
              <label class="flex items-center gap-1.5 text-xs text-slate-400">
                Mode
                <select v-model="restartMode" class="rounded border border-slate-600 bg-slate-800 px-1.5 py-0.5 text-xs text-slate-200">
                  <option value="">Unchanged ({{ task?.spec.agent.mode || 'auto' }})</option>
                  <option value="auto">Auto</option>
                  <option value="plan">Approve each step</option>
                  <option value="normal">Normal</option>
                </select>
              </label>
            </template>
            <span v-if="restartError" class="w-full text-center text-xs text-rose-400">{{
              restartError
            }}</span>
          </div>
        </div>

        <div
          v-else-if="conn === 'disconnected'"
          class="absolute inset-0 z-10 flex items-center justify-center bg-slate-900/70 backdrop-blur-sm"
        >
          <div class="flex items-center gap-3 rounded-lg bg-slate-800 px-5 py-3 text-sm text-slate-200 shadow-lg">
            <span class="h-4 w-4 animate-spin rounded-full border-2 border-slate-500 border-t-emerald-400" />
            Disconnected — reconnecting…
          </div>
        </div>
      </div>

      <TaskSidePanel
        v-if="isTask && taskPanelOpen && session?.taskId"
        :task-id="session.taskId"
        :sheet="touch"
        @close="taskPanelOpen = false"
        @open-files="filesPanelOpen = true"
      />
      <FileBrowserPanel v-if="filesPanelOpen" :session-id="id" @close="filesPanelOpen = false" />
    </div>

    <HostKeyTrustDialog
      :open="pendingHostKey !== null"
      :host-id="pendingHostKey?.hostId ?? ''"
      :host-name="pendingHostKey?.hostName ?? ''"
      :details="pendingHostKey?.details ?? null"
      @close="pendingHostKey = null"
      @trusted="retryRestartAfterTrust"
    />
  </div>
</template>
