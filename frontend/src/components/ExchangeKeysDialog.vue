<script setup lang="ts">
import { ref, watch } from 'vue'
import {
  Dialog,
  DialogPanel,
  DialogTitle,
  TransitionRoot,
  TransitionChild,
} from '@headlessui/vue'
import { useHostsStore } from '@/stores/hosts'
import { ApiRequestError } from '@/api/client'
import HostKeyTrustDialog from './HostKeyTrustDialog.vue'
import type { ExchangeKeysResponse, Host, HostKeyErrorDetails } from '@/api/types'

// One-time credentials modal for §4.5.2's passwordless-login setup: the
// password entered here is sent once to the exchange-keys endpoint and is
// never stored by this app — it's cleared from this component's own state
// the moment the request settles, success or failure.
const props = defineProps<{ open: boolean; host: Host | null }>()
const emit = defineEmits<{
  (e: 'close'): void
  (e: 'exchanged', result: ExchangeKeysResponse): void
}>()

const store = useHostsStore()

const username = ref('')
const password = ref('')
const submitting = ref(false)
const error = ref<string | null>(null)
const result = ref<ExchangeKeysResponse | null>(null)

const pendingHostKey = ref<HostKeyErrorDetails | null>(null)

watch(
  () => props.open,
  (isOpen) => {
    if (!isOpen) return
    error.value = null
    result.value = null
    pendingHostKey.value = null
    username.value = props.host?.username ?? ''
    password.value = ''
  },
)

// A host-key trust prompt (unknown/changed key) interrupts this attempt
// without ending it — the password stays in memory just long enough to
// retry once the user trusts the key, matching what session creation does
// for the same 409 (§4.5.1). Every other outcome — success or a real
// failure — clears it immediately: there is no other reason to hold onto
// it after this one exchange concludes.
async function attempt() {
  if (!props.host || submitting.value) return
  submitting.value = true
  error.value = null
  try {
    const res = await store.exchangeKeys(props.host.id, {
      username: username.value.trim(),
      password: password.value,
    })
    result.value = res
    password.value = ''
    emit('exchanged', res)
  } catch (e) {
    const details = e instanceof ApiRequestError ? e.hostKeyDetails() : null
    if (details) {
      pendingHostKey.value = details
    } else {
      error.value = e instanceof Error ? e.message : String(e)
      password.value = ''
    }
  } finally {
    submitting.value = false
  }
}

function submit() {
  if (username.value.trim() === '' || password.value === '') return
  void attempt()
}

function retryAfterTrust() {
  pendingHostKey.value = null
  void attempt()
}

const inputCls =
  'rounded-md border border-slate-600 bg-slate-900 px-3 py-2 text-slate-100 outline-none focus:border-emerald-500'
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
              Exchange SSH keys
            </DialogTitle>

            <div v-if="result" class="mt-4 flex flex-col gap-4">
              <p class="text-sm text-emerald-400">
                Passwordless login configured. Future connections to
                <strong>{{ host?.name }}</strong> use the generated key — the stored password
                was cleared.
              </p>
              <div class="flex justify-end">
                <button
                  type="button"
                  class="rounded-md bg-emerald-600 px-4 py-2 text-sm font-medium text-white hover:bg-emerald-500"
                  @click="emit('close')"
                >
                  Done
                </button>
              </div>
            </div>

            <form v-else class="mt-5 flex flex-col gap-4" @submit.prevent="submit">
              <p class="text-sm text-slate-400">
                Enter SSH credentials for <strong>{{ host?.name }}</strong> once. The server
                generates a new key, installs it remotely, and never stores this password.
              </p>

              <label class="flex flex-col gap-1 text-sm">
                <span class="text-slate-400">Username</span>
                <input v-model="username" type="text" autofocus autocomplete="off" :class="inputCls" />
              </label>

              <label class="flex flex-col gap-1 text-sm">
                <span class="text-slate-400">Password</span>
                <input
                  v-model="password"
                  type="password"
                  autocomplete="off"
                  :class="inputCls"
                />
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
                  :disabled="submitting || username.trim() === '' || password === ''"
                  class="rounded-md bg-emerald-600 px-4 py-2 text-sm font-medium text-white hover:bg-emerald-500 disabled:cursor-not-allowed disabled:opacity-50"
                >
                  {{ submitting ? 'Exchanging…' : 'Exchange keys' }}
                </button>
              </div>
            </form>
          </DialogPanel>
        </TransitionChild>
      </div>
    </Dialog>
  </TransitionRoot>

  <HostKeyTrustDialog
    :open="pendingHostKey !== null"
    :host-id="host?.id ?? ''"
    :host-name="host?.name ?? ''"
    :details="pendingHostKey"
    @close="pendingHostKey = null"
    @trusted="retryAfterTrust"
  />
</template>
