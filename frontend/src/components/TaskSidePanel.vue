<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { XMarkIcon } from '@heroicons/vue/20/solid'
import { useTasksStore } from '@/stores/tasks'
import { useHostsStore } from '@/stores/hosts'

// The task panel (§4.17.4) beside a task's terminal: what the task is, the
// agent's own status line, sessile tool activity, and — the part that needs
// the user — write calls waiting for approval. Rendered as a column on a
// desktop and as a sheet over the terminal on a phone (sheet).
const props = defineProps<{ taskId: string; sheet?: boolean }>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'openFiles'): void }>()

const store = useTasksStore()
const hosts = useHostsStore()
const deciding = ref<string | null>(null)
const error = ref<string | null>(null)

watch(
  () => props.taskId,
  (id) => {
    if (id) void store.loadOne(id)
  },
  { immediate: true },
)
if (hosts.hosts.length === 0) void hosts.fetchHosts()

const task = computed(() => store.tasks[props.taskId])
const approvals = computed(() => store.approvals[props.taskId] ?? [])
const activity = computed(() => store.activity[props.taskId] ?? [])
const hostName = computed(() => {
  const t = task.value
  if (!t) return ''
  if (t.spec.target === 'local') return 'this server'
  return hosts.hosts.find((h) => h.id === t.hostId)?.name ?? 'a host'
})

// The state badge's colours: blocked asks for the user, done is finished,
// working is in progress.
const stateClass = computed(() => {
  switch (task.value?.state) {
    case 'blocked':
      return 'bg-amber-500/20 text-amber-300'
    case 'done':
      return 'bg-emerald-500/20 text-emerald-300'
    default:
      return 'bg-slate-700 text-slate-300'
  }
})

function pretty(v: unknown) {
  if (v === null || v === undefined) return ''
  try {
    return JSON.stringify(v, null, 2)
  } catch {
    return String(v)
  }
}

async function decide(callId: string, approve: boolean) {
  deciding.value = callId
  error.value = null
  try {
    await store.decide(props.taskId, callId, approve)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    deciding.value = null
  }
}

const statusCls: Record<string, string> = {
  running: 'text-slate-400',
  ok: 'text-emerald-400',
  error: 'text-rose-400',
  denied: 'text-amber-400',
}

function ago(at: number) {
  const s = Math.max(0, Math.round((Date.now() - at) / 1000))
  return s < 60 ? `${s}s ago` : `${Math.round(s / 60)}m ago`
}
</script>

<template>
  <div
    class="flex flex-col bg-slate-900"
    :class="
      sheet
        ? 'fixed inset-x-0 bottom-14 z-30 max-h-[65dvh] rounded-t-xl border-t border-slate-700 shadow-2xl'
        : 'h-full w-80 shrink-0 border-l border-slate-800'
    "
  >
    <div class="flex items-center justify-between border-b border-slate-800 px-3 py-2">
      <span class="text-xs font-medium uppercase tracking-wide text-slate-400">Task</span>
      <button
        type="button"
        class="flex h-6 w-6 items-center justify-center rounded text-slate-400 hover:bg-slate-800 hover:text-slate-200"
        aria-label="Close task panel"
        @click="emit('close')"
      >
        <XMarkIcon class="h-4 w-4" />
      </button>
    </div>

    <div class="min-h-0 flex-1 overflow-y-auto p-3 text-sm">
      <template v-if="task">
        <p class="font-medium text-slate-100">{{ task.spec.name }}</p>
        <p class="mt-1 flex items-center gap-2">
          <!-- What the agent says it is doing (§4.18.2), not a guess from
               the terminal. -->
          <span
            v-if="task.state"
            class="rounded px-1.5 py-0.5 text-xs font-medium"
            :class="stateClass"
            >{{ task.state }}</span
          >
          <span v-if="task.spec.epic" class="text-xs text-slate-400">{{ task.spec.epic }}</span>
        </p>
        <p class="mt-1 text-slate-300" :class="task.summary ? '' : 'text-slate-500'">
          {{ task.summary || 'No status from the agent yet.' }}
        </p>
        <p
          v-if="task.state === 'blocked' && task.question"
          class="mt-2 rounded-md border border-amber-600/60 bg-slate-800/60 p-2 text-slate-200"
        >
          <span class="block text-xs font-medium uppercase tracking-wide text-amber-400">Waiting for an answer</span>
          {{ task.question }}
        </p>

        <!-- Write calls waiting for the user: the reason this panel exists. -->
        <div v-if="approvals.length" class="mt-4 flex flex-col gap-2">
          <p class="text-xs font-medium uppercase tracking-wide text-amber-400">Waiting for your approval</p>
          <div v-for="a in approvals" :key="a.callId" class="rounded-md border border-amber-600/60 bg-slate-800/60 p-3">
            <p class="font-mono text-xs text-slate-100">{{ a.name }}</p>
            <pre
              v-if="pretty(a.input) && pretty(a.input) !== '{}'"
              class="mt-2 max-h-48 overflow-auto whitespace-pre-wrap break-all rounded bg-slate-900 p-2 text-xs text-slate-300"
              >{{ pretty(a.input) }}</pre
            >
            <div class="mt-2 flex gap-2">
              <button
                type="button"
                class="rounded-md bg-emerald-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-emerald-500 disabled:opacity-50"
                :disabled="deciding === a.callId"
                @click="decide(a.callId, true)"
              >
                Approve
              </button>
              <button
                type="button"
                class="rounded-md border border-slate-600 px-3 py-1.5 text-xs text-slate-300 hover:border-rose-500 hover:text-rose-400 disabled:opacity-50"
                :disabled="deciding === a.callId"
                @click="decide(a.callId, false)"
              >
                Deny
              </button>
            </div>
          </div>
          <p v-if="error" class="text-xs text-rose-400">{{ error }}</p>
        </div>

        <dl class="mt-4 grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs">
          <dt class="text-slate-500">Host</dt>
          <dd class="truncate text-slate-300">{{ hostName }}</dd>
          <template v-if="task.spec.repo">
            <dt class="text-slate-500">Repo</dt>
            <dd class="break-all text-slate-300">
              {{ task.spec.repo.url }}<span v-if="task.spec.repo.ref"> @ {{ task.spec.repo.ref }}</span>
            </dd>
          </template>
          <template v-if="task.spec.devcontainer">
            <dt class="text-slate-500">Container</dt>
            <dd class="text-slate-300">
              devcontainer ({{ task.spec.devcontainer.mode }}){{ task.spec.devcontainer.dockerSocket ? ', Docker socket' : '' }}
            </dd>
          </template>
          <dt class="text-slate-500">Mode</dt>
          <dd class="text-slate-300">{{ task.spec.agent.mode === 'normal' ? 'normal' : 'plan first' }}</dd>
          <template v-if="task.dir">
            <dt class="text-slate-500">Folder</dt>
            <dd class="break-all font-mono text-slate-400">{{ task.dir }}</dd>
          </template>
        </dl>
        <button
          type="button"
          class="mt-2 rounded-md border border-slate-600 px-2.5 py-1 text-xs text-slate-300 hover:bg-slate-800"
          @click="emit('openFiles')"
        >
          Open files
        </button>

        <div class="mt-5">
          <p class="mb-1 text-xs font-medium uppercase tracking-wide text-slate-500">Tool activity</p>
          <p v-if="activity.length === 0" class="text-xs text-slate-500">No sessile tools called yet.</p>
          <ul class="flex flex-col gap-1">
            <li v-for="a in activity" :key="a.callId" class="text-xs">
              <span class="font-mono text-slate-300">{{ a.name }}</span>
              <span class="ml-1" :class="statusCls[a.status]">{{ a.status }}</span>
              <span class="ml-1 text-slate-600">{{ ago(a.at) }}</span>
              <p v-if="a.message" class="mt-0.5 line-clamp-3 break-all text-slate-500">{{ a.message }}</p>
            </li>
          </ul>
        </div>
      </template>
      <p v-else class="text-xs text-slate-500">Loading the task…</p>
    </div>
  </div>
</template>
