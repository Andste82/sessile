<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import {
  FolderIcon,
  ArrowUturnUpIcon,
  HomeIcon,
} from '@heroicons/vue/24/outline'
import { api } from '@/api/client'

// v-model is the selected directory: whatever folder is currently open is the
// one a new session will start in ("." = root). Navigating updates it (#7).
const props = defineProps<{ modelValue: string }>()
const emit = defineEmits<{ (e: 'update:modelValue', v: string): void }>()

const entries = ref<string[]>([])
const parent = ref<string | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)
const manual = ref('')

async function load(path: string) {
  loading.value = true
  error.value = null
  try {
    const res = await api.directories(path)
    entries.value = res.directories
    parent.value = res.parent
    // Adopt the server's cleaned form (e.g. trailing slashes removed).
    if (res.path !== props.modelValue) emit('update:modelValue', res.path)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

watch(() => props.modelValue, (p) => load(p), { immediate: true })

function openChild(name: string) {
  const base = props.modelValue === '.' ? '' : `${props.modelValue}/`
  emit('update:modelValue', base + name)
}

function goUp() {
  if (parent.value !== null) emit('update:modelValue', parent.value)
}

function submitManual() {
  const v = manual.value.trim()
  if (v) emit('update:modelValue', v)
  manual.value = ''
}

// Breadcrumb segments with the path to jump to for each.
const crumbs = computed(() => {
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
        :class="modelValue === '.' ? 'text-emerald-400' : 'text-slate-300'"
        @click="emit('update:modelValue', '.')"
      >
        <HomeIcon class="h-3.5 w-3.5" /> root
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
      <p v-if="error" class="px-2 py-3 text-sm text-rose-400">{{ error }}</p>
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
    <div class="flex items-center gap-2 border-t border-slate-700 px-2 py-1.5">
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
