<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { PlusIcon } from '@heroicons/vue/24/solid'
import { useSessionsStore } from '@/stores/sessions'
import { isAlreadyRunning } from '@/api/client'
import SessionListItem from '@/components/SessionListItem.vue'
import NewSessionDialog from '@/components/NewSessionDialog.vue'
import type { Session } from '@/api/types'

const store = useSessionsStore()
const router = useRouter()
const dialogOpen = ref(false)

// Polling is App-wide (it feeds the sidebar and the tab bar too), so this only
// has to make sure the list is loaded before the first tick.
onMounted(async () => {
  await Promise.all([store.fetchConfig(), store.fetchSessions()])
})

function onCreated(session: Session) {
  dialogOpen.value = false
  router.push(`/sessions/${session.id}`)
}

// Deleting can now be refused with a conflict — a session that is mid-restart
// keeps its row until the new shell is published — so this needs the same
// reporting as restarting, rather than an unhandled rejection.
async function onDelete(id: string) {
  try {
    await store.deleteSession(id)
  } catch (e) {
    store.error = e instanceof Error ? e.message : String(e)
  }
}

// Restarting from the list opens the session too: the point of the button is to
// get back into a session, and the terminal is where the restored scrollback is.
async function onRestart(id: string) {
  try {
    await store.restartSession(id)
    router.push(`/sessions/${id}`)
  } catch (e) {
    // Another browser started it first. The button asked for a live session and
    // there is one, so open it rather than reporting a conflict about the state
    // the user wanted.
    if (isAlreadyRunning(e)) {
      router.push(`/sessions/${id}`)
      return
    }
    store.error = e instanceof Error ? e.message : String(e)
  }
}
</script>

<template>
  <div class="flex h-full flex-col">
    <header
      class="flex items-center gap-3 border-b border-slate-800 bg-slate-900 px-4 py-4 sm:px-6"
    >
      <h1 class="text-lg font-semibold tracking-tight">Sessions</h1>
      <span
        v-if="store.config"
        class="ml-2 hidden truncate font-mono text-xs text-slate-500 sm:inline"
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

    <main class="mx-auto w-full max-w-5xl flex-1 overflow-y-auto p-4 sm:p-6">
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
          @restart="onRestart"
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
