<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { ArrowPathIcon } from '@heroicons/vue/20/solid'
import { api, isUnsupportedPlatform } from '@/api/client'
import type { Process } from '@/api/types'
import ProcessTreeNode from './ProcessTreeNode.vue'

const props = defineProps<{ sessionId: string }>()

const loading = ref(false)
const error = ref<string | null>(null)
const unsupported = ref(false)
const rootPid = ref<number | null>(null)
const scoped = ref(true)
const processes = ref<Process[]>([])
// "session" asks the backend to narrow to this session's own processes —
// for SSH it may not be able to (§4.10) and falls back to "all" itself,
// reported via `scoped`. This toggle is the user's own choice of which to
// ask for, independent of whether the last request actually got scoped.
const requestedScope = ref<'session' | 'all'>('session')

async function load() {
  loading.value = true
  error.value = null
  unsupported.value = false
  try {
    const res = await api.processTree(props.sessionId, requestedScope.value)
    rootPid.value = res.rootPid
    scoped.value = res.scoped
    processes.value = res.processes
  } catch (e) {
    if (isUnsupportedPlatform(e)) {
      unsupported.value = true
    } else {
      error.value = e instanceof Error ? e.message : String(e)
    }
  } finally {
    loading.value = false
  }
}

onMounted(load)
// A restart gives the session a new shell under a new PID — the old tree
// would otherwise keep showing a process that no longer exists.
watch(() => props.sessionId, load)
</script>

<template>
  <div class="flex h-full flex-col">
    <div class="flex items-center justify-between border-b border-slate-800 px-3 py-2">
      <span class="text-xs font-medium uppercase tracking-wide text-slate-400">
        Process tree<template v-if="rootPid !== null"> — root {{ rootPid }}</template>
      </span>
      <div class="flex items-center gap-2">
        <div class="flex rounded-md border border-slate-700 text-xs">
          <button
            type="button"
            class="rounded-l-md px-2 py-0.5"
            :class="requestedScope === 'session' ? 'bg-emerald-600 text-white' : 'text-slate-400 hover:bg-slate-800'"
            @click="requestedScope = 'session'; load()"
          >
            This session
          </button>
          <button
            type="button"
            class="rounded-r-md px-2 py-0.5"
            :class="requestedScope === 'all' ? 'bg-emerald-600 text-white' : 'text-slate-400 hover:bg-slate-800'"
            @click="requestedScope = 'all'; load()"
          >
            All processes
          </button>
        </div>
        <button
          type="button"
          class="flex h-6 w-6 shrink-0 items-center justify-center rounded text-slate-400 hover:bg-slate-800 hover:text-slate-200 disabled:opacity-50"
          :disabled="loading"
          title="Refresh"
          @click="load"
        >
          <ArrowPathIcon class="h-3.5 w-3.5" :class="{ 'animate-spin': loading }" />
        </button>
      </div>
    </div>

    <p
      v-if="!loading && !error && !unsupported && requestedScope === 'session' && !scoped"
      class="border-b border-slate-800 bg-slate-800/50 px-3 py-1.5 text-xs text-slate-400"
    >
      Couldn't narrow this to just the session — showing every process on the target instead.
    </p>

    <div class="min-h-0 flex-1 overflow-y-auto p-2">
      <p v-if="unsupported" class="p-3 text-xs text-slate-500">
        This target's OS doesn't have process-tree support yet.
      </p>
      <p v-else-if="error" class="p-3 text-xs text-rose-400">{{ error }}</p>
      <p v-else-if="!loading && processes.length === 0" class="p-3 text-xs text-slate-500">
        No child processes.
      </p>
      <ul v-else>
        <ProcessTreeNode v-for="p in processes" :key="p.pid" :process="p" :depth="0" />
      </ul>
    </div>
  </div>
</template>
