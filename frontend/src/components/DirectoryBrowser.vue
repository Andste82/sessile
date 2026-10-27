<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import {
  FolderIcon,
  ArrowUturnUpIcon,
  HomeIcon,
} from '@heroicons/vue/24/outline'
import { api, isAbort, type ApiRequestError } from '@/api/client'
import type { DirectoriesResponse } from '@/api/types'
import { joinRemote, remoteCrumbs } from '@/utils/path'

// v-model is the selected directory: whatever folder is currently open is the
// one a new session will start in ("." = root). Navigating updates it (#7).
//
// By default it browses the local workspace (relative paths). With `loader`
// and `absolute` it browses an SSH host instead (absolute paths, "/" root).
const props = defineProps<{
  modelValue: string
  loader?: (path: string) => Promise<DirectoriesResponse>
  absolute?: boolean
  noManual?: boolean // hide the built-in path field when the parent supplies its own
}>()
const emit = defineEmits<{
  (e: 'update:modelValue', v: string): void
  (e: 'host-key', err: ApiRequestError): void
}>()

const entries = ref<string[]>([])
const parent = ref<string | null>(null)
const loading = ref(false)
// Shown only after a short delay so a fast listing does not flash a spinner.
const showSpinner = ref(false)
const error = ref<string | null>(null)
const manual = ref('')

const SPINNER_DELAY_MS = 150
let spinnerTimer: ReturnType<typeof setTimeout> | null = null
// Only the latest load may apply its result.
let seq = 0

function clearSpinnerTimer() {
  if (spinnerTimer) clearTimeout(spinnerTimer)
  spinnerTimer = null
}

async function load(path: string) {
  const mine = ++seq
  loading.value = true
  error.value = null
  showSpinner.value = false
  clearSpinnerTimer()
  spinnerTimer = setTimeout(() => {
    if (mine === seq) showSpinner.value = true
  }, SPINNER_DELAY_MS)
  try {
    const res = await (props.loader ? props.loader(path) : api.directories(path))
    if (mine !== seq) return
    entries.value = res.directories
    parent.value = res.parent
    // Adopt the server's cleaned form (e.g. trailing slashes removed).
    if (res.path !== props.modelValue) emit('update:modelValue', res.path)
  } catch (e) {
    if (mine !== seq || isAbort(e)) return
    if ((e as ApiRequestError).hostKeyDetails?.()) emit('host-key', e as ApiRequestError)
    else error.value = e instanceof Error ? e.message : String(e)
  } finally {
    if (mine === seq) {
      loading.value = false
      showSpinner.value = false
      clearSpinnerTimer()
    }
  }
}

watch(() => props.modelValue, (p) => load(p), { immediate: true })

onBeforeUnmount(() => {
  seq++
  clearSpinnerTimer()
})

function openChild(name: string) {
  if (props.absolute) {
    emit('update:modelValue', joinRemote(props.modelValue, name))
    return
  }
  const base = props.modelValue === '.' ? '' : `${props.modelValue}/`
  emit('update:modelValue', base + name)
}

function goUp() {
  if (parent.value !== null) emit('update:modelValue', parent.value)
}

function retry() {
  void load(props.modelValue)
}

function submitManual() {
  const v = manual.value.trim()
  if (v) emit('update:modelValue', v)
  manual.value = ''
}

const rootPath = computed(() => (props.absolute ? '/' : '.'))
const atRoot = computed(() => props.modelValue === rootPath.value)

// Breadcrumb segments with the path to jump to for each.
const crumbs = computed(() => {
  if (props.absolute) return remoteCrumbs(props.modelValue)
  if (props.modelValue === '.' || props.modelValue === '') return []
  const segs = props.modelValue.split('/')
  return segs.map((name, i) => ({ name, path: segs.slice(0, i + 1).join('/') }))
})
</script>

<template>
  <div class="rounded-md border border-slate-600 bg-slate-900">
    <!-- Breadcrumb -->
    <div class="flex items-center gap-1 overflow-x-auto border-b border-slate-700 px-2 py-1.5 text-xs">
      <button
        type="button"
        class="flex shrink-0 items-center gap-1 rounded px-1.5 py-0.5 hover:bg-slate-700"
        :class="atRoot ? 'text-emerald-400' : 'text-slate-300'"
        @click="emit('update:modelValue', rootPath)"
      >
        <HomeIcon class="h-3.5 w-3.5" /> {{ absolute ? '/' : 'root' }}
      </button>
      <template v-for="c in crumbs" :key="c.path">
        <span class="shrink-0 text-slate-600">/</span>
        <button
          type="button"
          class="shrink-0 rounded px-1.5 py-0.5 text-slate-300 hover:bg-slate-700"
          @click="emit('update:modelValue', c.path)"
        >
          {{ c.name }}
        </button>
      </template>
    </div>

    <!-- Listing -->
    <div class="max-h-48 overflow-y-auto p-1">
      <div v-if="error" class="px-2 py-3 text-sm">
        <p role="alert" class="text-rose-400">{{ error }}</p>
        <button
          type="button"
          class="mt-2 rounded bg-slate-700 px-2 py-1 text-xs text-slate-200 hover:bg-slate-600"
          @click="retry"
        >
          Retry
        </button>
      </div>
      <p
        v-else-if="showSpinner"
        class="flex items-center gap-2 px-2 py-3 text-sm text-slate-400"
        role="status"
      >
        <span
          class="h-4 w-4 animate-spin rounded-full border-2 border-slate-600 border-t-emerald-400"
          aria-hidden="true"
        />
        Loading…
      </p>
      <template v-else>
        <button
          v-if="parent !== null"
          type="button"
          class="flex w-full items-center gap-2 rounded px-2 py-1.5 text-sm text-slate-300 hover:bg-slate-700"
          @click="goUp"
        >
          <ArrowUturnUpIcon class="h-4 w-4 shrink-0 text-slate-500" />
          <span class="text-slate-400">..</span>
        </button>
        <button
          v-for="d in entries"
          :key="d"
          type="button"
          class="flex w-full items-center gap-2 rounded px-2 py-1.5 text-sm text-slate-200 hover:bg-slate-700"
          @click="openChild(d)"
        >
          <FolderIcon class="h-4 w-4 shrink-0 text-slate-500" />
          <span class="truncate">{{ d }}</span>
        </button>
        <p
          v-if="!loading && parent === null && entries.length === 0"
          class="px-2 py-3 text-sm text-slate-500"
        >
          No subdirectories.
        </p>
        <p
          v-else-if="!loading && entries.length === 0"
          class="px-2 py-2 text-xs text-slate-500"
        >
          No subdirectories here.
        </p>
      </template>
    </div>

    <!-- Manual path entry for advanced users -->
    <div v-if="!noManual" class="flex items-center gap-2 border-t border-slate-700 px-2 py-1.5">
      <input
        v-model="manual"
        type="text"
        placeholder="Type a path…"
        class="min-w-0 flex-1 rounded bg-slate-800 px-2 py-1 font-mono text-xs text-slate-100 outline-none focus:ring-1 focus:ring-emerald-500"
        @keyup.enter.prevent="submitManual"
      />
      <button
        type="button"
        class="shrink-0 rounded bg-slate-700 px-2 py-1 text-xs text-slate-200 hover:bg-slate-600"
        @click="submitManual"
      >
        Go
      </button>
    </div>

    <!-- Current selection -->
    <div class="border-t border-slate-700 px-3 py-2 text-xs text-slate-400">
      Starts in:
      <span class="font-mono text-emerald-400">{{ modelValue === '.' ? 'root' : modelValue }}</span>
    </div>
  </div>
</template>
