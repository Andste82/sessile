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
import DirectoryBrowser from './DirectoryBrowser.vue'
import type { Session } from '@/api/types'

const props = defineProps<{ open: boolean }>()
const emit = defineEmits<{
  (e: 'close'): void
  (e: 'created', session: Session): void
}>()

const store = useSessionsStore()

const name = ref('')
const directory = ref('.')
const shell = ref('')
const submitting = ref(false)
const error = ref<string | null>(null)

const shells = computed(() => store.config?.shells ?? [])
const canSubmit = computed(
  () => name.value.trim().length > 0 && directory.value !== '' && shell.value !== '',
)

// Reset to defaults whenever the dialog opens; the browser starts at the root.
watch(
  () => props.open,
  (isOpen) => {
    if (!isOpen) return
    error.value = null
    name.value = ''
    directory.value = '.'
    shell.value = shells.value[0] ?? ''
  },
)

async function submit() {
  if (!canSubmit.value || submitting.value) return
  submitting.value = true
  error.value = null
  try {
    const session = await store.createSession({
      name: name.value.trim(),
      directory: directory.value,
      shell: shell.value,
    })
    emit('created', session)
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
        <div class="fixed inset-0 bg-black/60" aria-hidden="true" />
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
            class="w-full max-w-md rounded-xl border border-slate-700 bg-slate-800 p-6 shadow-xl"
          >
            <DialogTitle class="text-lg font-semibold text-slate-100">
              New session
            </DialogTitle>

            <form class="mt-5 flex flex-col gap-4" @submit.prevent="submit">
              <label class="flex flex-col gap-1 text-sm">
                <span class="text-slate-400">Name</span>
                <input
                  v-model="name"
                  type="text"
                  maxlength="64"
                  autofocus
                  placeholder="Backend"
                  class="rounded-md border border-slate-600 bg-slate-900 px-3 py-2 text-slate-100 outline-none focus:border-emerald-500"
                />
              </label>

              <label class="flex flex-col gap-1 text-sm">
                <span class="text-slate-400">Directory</span>
                <DirectoryBrowser v-model="directory" />
              </label>

              <label class="flex flex-col gap-1 text-sm">
                <span class="text-slate-400">Shell</span>
                <select
                  v-model="shell"
                  class="rounded-md border border-slate-600 bg-slate-900 px-3 py-2 text-slate-100 outline-none focus:border-emerald-500"
                >
                  <option v-for="s in shells" :key="s" :value="s">{{ s }}</option>
                </select>
              </label>

              <p v-if="error" class="text-sm text-rose-400">{{ error }}</p>

              <div class="mt-2 flex justify-end gap-3">
                <button
                  type="button"
                  class="rounded-md px-4 py-2 text-sm text-slate-300 hover:bg-slate-700"
                  @click="emit('close')"
                >
                  Cancel
                </button>
                <button
                  type="submit"
                  :disabled="!canSubmit || submitting"
                  class="rounded-md bg-emerald-600 px-4 py-2 text-sm font-medium text-white hover:bg-emerald-500 disabled:cursor-not-allowed disabled:opacity-50"
                >
                  {{ submitting ? 'Creating…' : 'Create' }}
                </button>
              </div>
            </form>
          </DialogPanel>
        </TransitionChild>
      </div>
    </Dialog>
  </TransitionRoot>
</template>
