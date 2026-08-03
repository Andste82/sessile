<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import TerminalView from '@/components/TerminalView.vue'
import TabBar from '@/components/TabBar.vue'
import { useSessionsStore } from '@/stores/sessions'
import { api } from '@/api/client'
import type { Session } from '@/api/types'
import type { ConnStatus } from '@/composables/useTerminal'

const route = useRoute()
const store = useSessionsStore()

const id = computed(() => String(route.params.id))
const session = ref<Session | null>(null)
const conn = ref<ConnStatus>('connecting')
const loadError = ref<string | null>(null)

const restarting = ref(false)
const restartError = ref<string | null>(null)
// A restart keeps the session id, so the id alone cannot re-key TerminalView.
// Bumping this forces a remount, which is what makes useTerminal open a fresh
// WebSocket and replay the restored scrollback.
const reloadNonce = ref(0)

async function restart() {
  if (restarting.value) return
  restarting.value = true
  restartError.value = null
  try {
    session.value = await store.restartSession(id.value)
    reloadNonce.value++
  } catch (e) {
    restartError.value = e instanceof Error ? e.message : String(e)
  } finally {
    restarting.value = false
  }
}

async function loadSession(sessionId: string) {
  store.openTab(sessionId)
  loadError.value = null
  session.value = store.byId(sessionId)
  if (session.value) return
  try {
    session.value = await api.getSession(sessionId)
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
</script>

<template>
  <div class="flex h-full flex-col bg-slate-900">
    <TabBar :conn="conn" />

    <div class="relative min-h-0 flex-1">
      <p v-if="loadError" class="p-6 text-sm text-rose-400">{{ loadError }}</p>
      <TerminalView
        v-else
        :key="`${id}:${reloadNonce}`"
        :session-id="id"
        class="h-full p-2"
        @status="conn = $event"
      />

      <div
        v-if="conn === 'exited'"
        class="absolute inset-x-0 top-0 flex justify-center p-3"
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
            {{ restarting ? 'Restarting…' : 'Restart session' }}
          </button>
          <span v-if="restartError" class="w-full text-center text-xs text-rose-400">{{
            restartError
          }}</span>
        </div>
      </div>

      <div
        v-else-if="conn === 'disconnected'"
        class="absolute inset-0 flex items-center justify-center bg-slate-900/70 backdrop-blur-sm"
      >
        <div class="flex items-center gap-3 rounded-lg bg-slate-800 px-5 py-3 text-sm text-slate-200 shadow-lg">
          <span class="h-4 w-4 animate-spin rounded-full border-2 border-slate-500 border-t-emerald-400" />
          Disconnected — reconnecting…
        </div>
      </div>
    </div>
  </div>
</template>
