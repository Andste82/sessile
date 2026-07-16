<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ArrowLeftIcon } from '@heroicons/vue/24/outline'
import TerminalView from '@/components/TerminalView.vue'
import StatusDot from '@/components/StatusDot.vue'
import { useSessionsStore } from '@/stores/sessions'
import { api } from '@/api/client'
import type { Session } from '@/api/types'
import type { ConnStatus } from '@/composables/useTerminal'

const route = useRoute()
const router = useRouter()
const store = useSessionsStore()

const id = computed(() => String(route.params.id))
const session = ref<Session | null>(null)
const conn = ref<ConnStatus>('connecting')
const loadError = ref<string | null>(null)

onMounted(async () => {
  if (!store.config) store.fetchConfig()
  session.value = store.byId(id.value)
  if (!session.value) {
    try {
      session.value = await api.getSession(id.value)
    } catch (e) {
      loadError.value = e instanceof Error ? e.message : String(e)
    }
  }
})

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
  <div class="flex h-screen flex-col bg-slate-900">
    <header
      class="flex items-center gap-3 border-b border-slate-800 px-4 py-2.5"
    >
      <button
        class="rounded p-1.5 text-slate-400 hover:bg-slate-800 hover:text-slate-100"
        title="Back to dashboard"
        @click="router.push('/')"
      >
        <ArrowLeftIcon class="h-5 w-5" />
      </button>
      <StatusDot v-if="session" :status="session.status" />
      <span class="font-medium text-slate-100">
        {{ session?.name ?? id }}
      </span>
      <span v-if="session" class="font-mono text-xs text-slate-500">
        {{ session.directory }} · {{ session.shell }}
      </span>
      <span
        class="ml-auto text-xs"
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
    </div>
  </div>
</template>
