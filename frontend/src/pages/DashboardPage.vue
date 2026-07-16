<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { PlusIcon } from '@heroicons/vue/24/solid'
import { useSessionsStore } from '@/stores/sessions'
import SessionListItem from '@/components/SessionListItem.vue'
import NewSessionDialog from '@/components/NewSessionDialog.vue'
import type { Session } from '@/api/types'

const store = useSessionsStore()
const router = useRouter()
const dialogOpen = ref(false)

onMounted(async () => {
  await Promise.all([store.fetchConfig(), store.fetchSessions()])
  store.startPolling(5000) // live client counts (§12 M4)
})

onUnmounted(() => store.stopPolling())

function onCreated(session: Session) {
  dialogOpen.value = false
  router.push(`/sessions/${session.id}`)
}

async function onDelete(id: string) {
  await store.deleteSession(id)
}
</script>

<template>
  <div class="min-h-full">
    <header
      class="sticky top-0 z-10 flex items-center gap-3 border-b border-slate-800 bg-slate-900/80 px-6 py-4 backdrop-blur"
    >
      <span class="font-mono text-xl text-emerald-400">&gt;_</span>
      <h1 class="text-lg font-semibold tracking-tight">sessile</h1>
      <span
        v-if="store.config"
        class="ml-2 hidden font-mono text-xs text-slate-500 sm:inline"
        :title="store.config.root"
        >root: {{ store.config.root }}</span
      >
      <button
        class="ml-auto flex items-center gap-2 rounded-md bg-emerald-600 px-3 py-2 text-sm font-medium text-white transition hover:bg-emerald-500"
        @click="dialogOpen = true"
      >
        <PlusIcon class="h-4 w-4" /> New session
      </button>
    </header>

    <main class="mx-auto max-w-5xl p-6">
      <p v-if="store.error" class="mb-4 text-sm text-rose-400">{{ store.error }}</p>

      <div
        v-if="!store.loading && store.sessions.length === 0"
        class="mt-24 flex flex-col items-center gap-3 text-center text-slate-400"
      >
        <p class="text-lg">No sessions yet.</p>
        <button
          class="flex items-center gap-2 rounded-md border border-slate-600 px-4 py-2 text-sm hover:bg-slate-800"
          @click="dialogOpen = true"
        >
          <PlusIcon class="h-4 w-4" /> Create your first session
        </button>
      </div>

      <div v-else class="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
        <SessionListItem
          v-for="s in store.sessions"
          :key="s.id"
          :session="s"
          @delete="onDelete"
        />
      </div>
    </main>

    <NewSessionDialog
      :open="dialogOpen"
      @close="dialogOpen = false"
      @created="onCreated"
    />
  </div>
</template>
