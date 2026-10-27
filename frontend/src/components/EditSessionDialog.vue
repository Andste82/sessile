<script setup lang="ts">
import { ref, watch, computed } from 'vue'
import {
  Dialog,
  DialogPanel,
  DialogTitle,
  TransitionRoot,
  TransitionChild,
} from '@headlessui/vue'
import { useSessionsStore } from '@/stores/sessions'
import { useHostsStore } from '@/stores/hosts'
import GroupInput from './GroupInput.vue'
import EnvTable from './EnvTable.vue'
import { patchEntries, rowsFromSession, validateRows, type EnvRow } from '@/utils/env'
import type { Session, UpdateSessionBody } from '@/api/types'

// Name, group and environment. Directory, shell and host are what the session
// *is* — changing them would mean a different session, which is what creating
// one is for. Name and group work on a stopped session too: both are metadata,
// and tidying a session up is exactly what one does once it has stopped
// (§4.11). The environment is editable only while stopped: it is applied at
// spawn, so changing it under a live shell would change nothing and mislead.
const props = defineProps<{ open: boolean; session: Session | null }>()
const emit = defineEmits<{ (e: 'close'): void }>()

const store = useSessionsStore()
const hostsStore = useHostsStore()

// Status comes from the live store session, not the prop snapshot, so a
// session that starts or stops while the dialog is open flips the table.
const live = computed(() => (props.session ? (store.byId(props.session.id) ?? props.session) : null))
const stopped = computed(() => live.value?.status === 'stopped')
// A host that is gone from the list is treated as supported; restart decides.
const envSupported = computed(() => {
  const hid = props.session?.hostId
  if (!hid) return true
  return hostsStore.hosts.find((h) => h.id === hid)?.supportsEnv !== false
})
const envEditable = computed(() => stopped.value && envSupported.value)
const envNote = computed(() => {
  if (!stopped.value) return 'Stop the session to edit its environment; it applies when the session starts.'
  if (!envSupported.value) return 'This host does not support environment variables.'
  return ''
})

const envRows = ref<EnvRow[]>([])
const envTouched = ref(false)
const loadDotenv = ref(false)
const dotenvTouched = ref(false)
const envError = computed(() => (envEditable.value && envTouched.value ? validateRows(envRows.value) : null))

function onRows(rows: EnvRow[]) {
  envRows.value = rows
  envTouched.value = true
}

const name = ref('')
const group = ref('')
const submitting = ref(false)
const error = ref<string | null>(null)

// Secret values typed here must not outlive the dialog.
watch(
  () => props.open,
  (isOpen) => {
    if (!isOpen) envRows.value = []
  },
)

if (hostsStore.hosts.length === 0) void hostsStore.fetchHosts()

watch(
  () => [props.open, props.session?.id] as const,
  ([isOpen]) => {
    if (!isOpen || !props.session) return
    error.value = null
    name.value = props.session.name
    group.value = props.session.group
    envRows.value = rowsFromSession(props.session.env ?? [])
    loadDotenv.value = props.session.loadDotenv ?? false
    envTouched.value = false
    dotenvTouched.value = false
  },
  { immediate: true },
)

const canSubmit = computed(() => name.value.trim().length > 0 && !envError.value)

async function submit() {
  if (!canSubmit.value || submitting.value || !props.session) return
  submitting.value = true
  error.value = null
  try {
    const body: UpdateSessionBody = {
      name: name.value.trim(),
      group: group.value.trim(),
    }
    // The full list goes only when touched; an untouched table sends nothing,
    // so a name-only edit also works on a running session.
    if (envEditable.value && envTouched.value) body.env = patchEntries(envRows.value)
    if (envEditable.value && dotenvTouched.value) body.loadDotenv = loadDotenv.value
    await store.updateSession(props.session.id, body)
    emit('close')
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <TransitionRoot :show="open" as="template">
    <Dialog class="relative z-50" @close="emit('close')">
      <TransitionChild
        as="template"
        enter="duration-150 ease-out"
        enter-from="opacity-0"
        enter-to="opacity-100"
        leave="duration-100 ease-in"
        leave-from="opacity-100"
        leave-to="opacity-0"
      >
        <div class="fixed inset-0 bg-slate-950/70" />
      </TransitionChild>

      <div class="fixed inset-0 flex items-center justify-center p-4">
        <TransitionChild
          as="template"
          enter="duration-150 ease-out"
          enter-from="opacity-0 scale-95"
          enter-to="opacity-100 scale-100"
          leave="duration-100 ease-in"
          leave-from="opacity-100 scale-100"
          leave-to="opacity-0 scale-95"
        >
          <DialogPanel
            class="max-h-[calc(100dvh-2rem)] w-full max-w-lg overflow-y-auto rounded-xl border border-slate-700 bg-slate-800 p-6 shadow-xl"
          >
            <DialogTitle class="text-lg font-semibold text-slate-100">Edit session</DialogTitle>

            <form class="mt-5 flex flex-col gap-4" @submit.prevent="submit">
              <label class="flex flex-col gap-1 text-sm">
                <span class="text-slate-400">Name</span>
                <input
                  v-model="name"
                  type="text"
                  maxlength="64"
                  autofocus
                  class="rounded-md border border-slate-600 bg-slate-900 px-3 py-2 text-slate-100 outline-none focus:border-emerald-500"
                />
              </label>

              <label class="flex flex-col gap-1 text-sm">
                <span class="text-slate-400">Group <span class="text-slate-500">(optional)</span></span>
                <GroupInput v-model="group" />
              </label>
              <p class="-mt-2 text-xs text-slate-500">
                Clearing this removes the session from its group. A group with no
                sessions left disappears on its own.
              </p>

              <div class="flex flex-col gap-2 rounded-md border border-slate-700 p-3">
                <span class="text-sm text-slate-300">Environment ({{ envRows.length }})</span>
                <EnvTable
                  :rows="envRows"
                  mode="edit"
                  :disabled="!envEditable"
                  :disabled-note="envNote"
                  @update:rows="onRows"
                />
                <label class="flex items-center gap-2 text-sm text-slate-300">
                  <input
                    v-model="loadDotenv"
                    type="checkbox"
                    :disabled="!envEditable"
                    class="accent-emerald-500"
                    @change="dotenvTouched = true"
                  />
                  Load .env
                </label>
                <p v-if="envError" class="text-xs text-rose-400">{{ envError }}</p>
              </div>

              <p v-if="error" class="text-sm text-rose-400">{{ error }}</p>

              <div class="mt-1 flex justify-end gap-2">
                <button
                  type="button"
                  class="rounded-md border border-slate-600 px-4 py-2 text-sm text-slate-300 hover:bg-slate-700"
                  @click="emit('close')"
                >
                  Cancel
                </button>
                <button
                  type="submit"
                  :disabled="!canSubmit || submitting"
                  class="rounded-md bg-emerald-600 px-4 py-2 text-sm font-medium text-white hover:bg-emerald-500 disabled:cursor-not-allowed disabled:opacity-50"
                >
                  {{ submitting ? 'Saving…' : 'Save' }}
                </button>
              </div>
            </form>
          </DialogPanel>
        </TransitionChild>
      </div>
    </Dialog>
  </TransitionRoot>
</template>
