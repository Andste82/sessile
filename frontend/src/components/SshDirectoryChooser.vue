<script setup lang="ts">
import { onBeforeUnmount, reactive, ref, watch } from 'vue'
import { api, isAbort, type ApiRequestError } from '@/api/client'
import type { HostKeyErrorDetails } from '@/api/types'
import { createHostBrowse, type BrowseState } from '@/utils/hostBrowse'
import DirectoryBrowser from './DirectoryBrowser.vue'

// Start-directory picker for an SSH session (§4.10). v-model is the chosen
// absolute path, "" meaning "whatever the host's sshd starts in" (today's
// behaviour). The manual path field always works, connected or not, and
// nothing here ever gates creating the session: the server re-checks the
// directory anyway.
const props = defineProps<{ hostId: string; hostName: string; modelValue: string }>()
const emit = defineEmits<{
  (e: 'update:modelValue', v: string): void
  // The browse connection hit an unknown/changed host key. The parent shows
  // the trust dialog, then calls retry() or dismissTrust().
  (e: 'host-key', details: HostKeyErrorDetails): void
}>()

const view = reactive<BrowseState>({
  status: 'idle',
  hostId: '',
  path: '',
  error: '',
  hostKey: null,
})

const browse = createHostBrowse(
  {
    fetchDirs: (hostId, path, signal) => api.hostDirectories(hostId, path, { signal }),
    closeBrowse: (hostId) => api.closeHostBrowse(hostId),
    isAbort,
  },
  (s) => {
    const prev = view.status
    Object.assign(view, s)
    if (s.status === 'needsTrust' && s.hostKey && prev !== 'needsTrust') emit('host-key', s.hostKey)
  },
)

// What the browser is showing; separate from the selection so typing in the
// manual field does not fire a listing per keystroke.
const browsePath = ref('')
const manual = ref(props.modelValue)

watch(
  () => props.modelValue,
  (v) => {
    manual.value = v
  },
)

watch(
  () => view.status,
  (s) => {
    if (s !== 'ready') return
    browsePath.value = view.path
    // Offer the home directory as the selection unless the user already typed one.
    if (props.modelValue === '') emit('update:modelValue', view.path)
  },
)

watch(
  () => props.hostId,
  (id) => {
    browsePath.value = ''
    if (id) void browse.start(id)
    else browse.stop()
  },
  { immediate: true },
)

function onBrowsed(p: string) {
  browsePath.value = p
  emit('update:modelValue', p)
}

function onManual() {
  const v = manual.value.trim()
  emit('update:modelValue', v)
  if (v.startsWith('/') && view.status === 'ready') browsePath.value = v
}

function onHostKey(err: ApiRequestError) {
  browse.reportHostKey(err)
}

defineExpose({
  retry: () => browse.retry(),
  dismissTrust: () => browse.dismissTrust(),
  stop: () => browse.stop(),
})

onBeforeUnmount(() => browse.stop())
</script>

<template>
  <div class="flex flex-col gap-2 text-sm">
    <label class="flex flex-col gap-1">
      <span class="text-slate-400">
        Start directory <span class="text-slate-500">(optional, absolute path)</span>
      </span>
      <input
        v-model="manual"
        type="text"
        maxlength="4096"
        placeholder="Host default (leave empty)"
        class="rounded-md border border-slate-600 bg-slate-900 px-3 py-2 font-mono text-xs text-slate-100 outline-none focus:border-emerald-500"
        @change="onManual"
        @keyup.enter.prevent="onManual"
        @blur="onManual"
      />
    </label>

    <p
      v-if="view.status === 'connecting'"
      class="flex items-center gap-2 text-xs text-slate-400"
      role="status"
    >
      <span
        class="h-4 w-4 animate-spin rounded-full border-2 border-slate-600 border-t-emerald-400"
        aria-hidden="true"
      />
      Connecting to {{ hostName }}…
    </p>

    <div v-else-if="view.status === 'error'" class="text-xs">
      <p role="alert" class="text-rose-400">{{ view.error }}</p>
      <button
        type="button"
        class="mt-2 rounded bg-slate-700 px-2 py-1 text-slate-200 hover:bg-slate-600"
        @click="browse.retry()"
      >
        Retry
      </button>
      <span class="ml-2 text-slate-500">You can still type a path above.</span>
    </div>

    <p v-else-if="view.status === 'needsTrust'" class="text-xs text-slate-400">
      Waiting for you to confirm the host key…
    </p>

    <DirectoryBrowser
      v-else-if="view.status === 'ready'"
      :model-value="browsePath"
      :loader="(p: string) => browse.load(p)"
      absolute
      no-manual
      @update:model-value="onBrowsed"
      @host-key="onHostKey"
    />
  </div>
</template>
