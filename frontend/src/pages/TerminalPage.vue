<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import TerminalView from '@/components/TerminalView.vue'
import TabBar from '@/components/TabBar.vue'
import StatusDot from '@/components/StatusDot.vue'
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
  if (!store.config) store.fetchConfig()
  if (store.sessions.length === 0) store.fetchSessions()
  await loadSession(id.value)
})

// Handle navigating directly between tabs (component is reused).
watch(id, (newId) => loadSession(newId))

const statusLabel = computed(() => {
  switch (conn.value) {
    case 'connected':
      return 'connected'
    case 'connecting':
      return 'connecting…'
    case 'disconnected':
      return 'disconnected'
    case 'exited':
      return 'session ended'
  }
  return ''
})
</script>

<template>
  <div class="flex h-full flex-col bg-slate-900">
    <TabBar />
    <header
      class="flex items-center gap-3 border-b border-slate-800 px-4 py-2.5"
    >
      <StatusDot v-if="session" :status="session.status" />
      <span class="truncate font-medium text-slate-100">
        {{ session?.name ?? id }}
      </span>
      <span v-if="session" class="hidden font-mono text-xs text-slate-500 sm:inline">
        {{ session.directory }} · {{ session.shell }}
      </span>
      <span
        class="ml-auto shrink-0 text-xs"
        :class="{
          'text-emerald-400': conn === 'connected',
          'text-amber-400': conn === 'connecting' || conn === 'disconnected',
          'text-slate-500': conn === 'exited',
        }"
        >{{ statusLabel }}</span
      >
    </header>

    <div class="relative min-h-0 flex-1">
      <p v-if="loadError" class="p-6 text-sm text-rose-400">{{ loadError }}</p>
      <TerminalView
        v-else
        :key="id"
        :session-id="id"
        class="h-full p-2"
        @status="conn = $event"
      />

      <div
        v-if="conn === 'exited'"
        class="pointer-events-none absolute inset-x-0 top-0 flex justify-center p-3"
      >
        <span
          class="rounded-md bg-slate-800 px-3 py-1.5 text-sm text-slate-300 shadow"
          >Session ended — the shell process has exited.</span
        >
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
