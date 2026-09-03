<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import {
  Dialog,
  DialogPanel,
  DialogTitle,
  TransitionRoot,
  TransitionChild,
} from '@headlessui/vue'
import { useHostsStore } from '@/stores/hosts'
import type { HostKeyErrorDetails } from '@/api/types'

// Shown whenever a connection attempt (session creation, restart, or key
// exchange) comes back host_key_unverified/host_key_changed (§4.5.1). There
// is no auto-trust path anywhere in this component — every accept is an
// explicit click against a fingerprint the user can read.
const props = defineProps<{
  open: boolean
  hostId: string
  hostName: string
  details: HostKeyErrorDetails | null
}>()
const emit = defineEmits<{
  (e: 'close'): void
  (e: 'trusted'): void
}>()

const store = useHostsStore()
const submitting = ref(false)
const error = ref<string | null>(null)

const changed = computed(() => !!props.details?.previousFingerprint)

watch(
  () => props.open,
  (isOpen) => {
    if (!isOpen) return
    error.value = null
  },
)

async function trust() {
  if (!props.details || submitting.value) return
  submitting.value = true
  error.value = null
  try {
    await store.trustHostKey(props.hostId, {
      fingerprint: props.details.fingerprint,
      keyType: props.details.keyType,
    })
    emit('trusted')
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
            <DialogTitle
              class="text-lg font-semibold"
              :class="changed ? 'text-amber-400' : 'text-slate-100'"
            >
              {{ changed ? 'Host key changed' : 'Unknown host key' }}
            </DialogTitle>

            <div v-if="details" class="mt-4 flex flex-col gap-3 text-sm">
              <p class="text-slate-300">
                <span v-if="changed">
                  The SSH key presented by <strong>{{ hostName }}</strong> no longer matches the
                  one this app trusted before. This can happen after a legitimate server
                  reinstall — it can also mean something is intercepting the connection. If
                  you're not sure this change is expected, cancel and confirm the new
                  fingerprint with the server's owner out-of-band before trusting it.
                </span>
                <span v-else>
                  This is the first connection to <strong>{{ hostName }}</strong>. Verify the
                  fingerprint below matches what the server operator expects before trusting it.
                </span>
              </p>

              <div class="rounded-md border border-slate-600 bg-slate-900 p-3 font-mono text-xs">
                <div class="flex justify-between gap-4 text-slate-400">
                  <span>Key type</span>
                  <span class="text-slate-200">{{ details.keyType }}</span>
                </div>
                <div class="mt-1 flex justify-between gap-4 text-slate-400">
                  <span>Fingerprint</span>
                  <span class="break-all text-right text-slate-200">{{ details.fingerprint }}</span>
                </div>
                <div v-if="changed" class="mt-2 border-t border-slate-700 pt-2">
                  <div class="flex justify-between gap-4 text-slate-400">
                    <span>Previously trusted</span>
                    <span class="break-all text-right text-rose-400">{{
                      details.previousFingerprint
                    }}</span>
                  </div>
                </div>
              </div>

              <p v-if="error" class="text-rose-400">{{ error }}</p>
            </div>

            <div class="mt-5 flex justify-end gap-3">
              <button
                type="button"
                class="rounded-md px-4 py-2 text-sm text-slate-300 hover:bg-slate-700"
                @click="emit('close')"
              >
                Cancel
              </button>
              <button
                type="button"
                :disabled="submitting"
                class="rounded-md px-4 py-2 text-sm font-medium text-white disabled:cursor-not-allowed disabled:opacity-50"
                :class="changed ? 'bg-amber-600 hover:bg-amber-500' : 'bg-emerald-600 hover:bg-emerald-500'"
                @click="trust"
              >
                {{ submitting ? 'Trusting…' : 'Trust and connect' }}
              </button>
            </div>
          </DialogPanel>
        </TransitionChild>
      </div>
    </Dialog>
  </TransitionRoot>
</template>

