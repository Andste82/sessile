<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { FolderIcon, DocumentIcon, ArrowPathIcon, PencilIcon } from '@heroicons/vue/20/solid'
import { api } from '@/api/client'
import type { HostDirEntry } from '@/api/types'

const props = defineProps<{ sessionId: string }>()

const currentPath = ref('') // "" means the target's own default root
const entries = ref<HostDirEntry[]>([])
const loading = ref(false)
const error = ref<string | null>(null)
const movingName = ref<string | null>(null)
const moveTarget = ref('')

// Client-side breadcrumb — simpler than asking the server for a parent on
// every response, and the server already told us the canonical form of
// whatever path we asked for last.
const crumbs = ref<string[]>([])

function joinPath(base: string, name: string): string {
  if (base === '' || base === '.') return name
  return `${base}/${name}`
}

async function load(path: string) {
  loading.value = true
  error.value = null
  try {
    const res = await api.listHostFiles(props.sessionId, path)
    currentPath.value = res.path
    entries.value = [...res.entries].sort((a, b) => {
      if (a.isDir !== b.isDir) return a.isDir ? -1 : 1
      return a.name.localeCompare(b.name)
    })
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

function open(entry: HostDirEntry) {
  if (!entry.isDir) return
  crumbs.value = [...crumbs.value, entry.name]
  void load(joinPath(currentPath.value, entry.name))
}

function goToCrumb(index: number) {
  // index -1 is the root.
  const target = crumbs.value.slice(0, index + 1)
  crumbs.value = target
  void load(target.length === 0 ? '' : target.join('/'))
}

function startMove(entry: HostDirEntry) {
  movingName.value = entry.name
  moveTarget.value = joinPath(currentPath.value, entry.name)
}

function cancelMove() {
  movingName.value = null
  moveTarget.value = ''
}

async function confirmMove() {
  if (movingName.value === null) return
  const src = joinPath(currentPath.value, movingName.value)
  const dst = moveTarget.value.trim()
  if (!dst || dst === src) {
    cancelMove()
    return
  }
  try {
    await api.moveHostFile(props.sessionId, src, dst)
    cancelMove()
    await load(currentPath.value)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let value = bytes / 1024
  let i = 0
  while (value >= 1024 && i < units.length - 1) {
    value /= 1024
    i++
  }
  return `${value.toFixed(1)} ${units[i]}`
}

onMounted(() => load(''))
watch(
  () => props.sessionId,
  () => {
    crumbs.value = []
    void load('')
  },
)
</script>

<template>
  <div class="flex h-full flex-col">
    <div class="flex items-center justify-between border-b border-slate-800 px-3 py-2">
      <div class="flex min-w-0 flex-wrap items-center gap-0.5 text-xs text-slate-400">
        <button type="button" class="rounded px-1 hover:bg-slate-800 hover:text-slate-200" @click="goToCrumb(-1)">
          root
        </button>
        <template v-for="(c, i) in crumbs" :key="i">
          <span class="text-slate-600">/</span>
          <button type="button" class="rounded px-1 hover:bg-slate-800 hover:text-slate-200" @click="goToCrumb(i)">
            {{ c }}
          </button>
        </template>
      </div>
      <button
        type="button"
        class="flex h-6 w-6 shrink-0 items-center justify-center rounded text-slate-400 hover:bg-slate-800 hover:text-slate-200 disabled:opacity-50"
        :disabled="loading"
        title="Refresh"
        @click="load(currentPath)"
      >
        <ArrowPathIcon class="h-3.5 w-3.5" :class="{ 'animate-spin': loading }" />
      </button>
    </div>

    <div class="min-h-0 flex-1 overflow-y-auto">
      <p v-if="error" class="p-3 text-xs text-rose-400">{{ error }}</p>
      <p v-else-if="!loading && entries.length === 0" class="p-3 text-xs text-slate-500">Empty directory.</p>
      <ul v-else class="divide-y divide-slate-800/60">
        <li v-for="entry in entries" :key="entry.name" class="group px-2 py-1.5">
          <div class="flex items-center gap-2 text-xs">
            <FolderIcon v-if="entry.isDir" class="h-4 w-4 shrink-0 text-slate-500" />
            <DocumentIcon v-else class="h-4 w-4 shrink-0 text-slate-600" />
            <button
              type="button"
              class="min-w-0 flex-1 truncate text-left text-slate-200 disabled:cursor-default"
              :disabled="!entry.isDir"
              :class="{ 'hover:text-emerald-400': entry.isDir }"
              @click="open(entry)"
            >
              {{ entry.name }}
            </button>
            <span v-if="!entry.isDir" class="shrink-0 text-slate-500">{{ formatSize(entry.size) }}</span>
            <button
              type="button"
              class="flex h-5 w-5 shrink-0 items-center justify-center rounded text-slate-500 opacity-0 hover:bg-slate-800 hover:text-slate-200 group-hover:opacity-100"
              title="Move / rename"
              @click="startMove(entry)"
            >
              <PencilIcon class="h-3 w-3" />
            </button>
          </div>
          <div v-if="movingName === entry.name" class="mt-1 flex items-center gap-1.5 pl-6">
            <input
              v-model="moveTarget"
              type="text"
              class="min-w-0 flex-1 rounded border border-slate-600 bg-slate-950 px-1.5 py-0.5 text-xs text-slate-100 outline-none focus:border-emerald-500"
              placeholder="new/path/for/this"
              @keyup.enter="confirmMove"
              @keyup.escape="cancelMove"
            />
            <button
              type="button"
              class="rounded bg-emerald-600 px-1.5 py-0.5 text-xs text-white hover:bg-emerald-500"
              @click="confirmMove"
            >
              Move
            </button>
            <button type="button" class="rounded px-1.5 py-0.5 text-xs text-slate-400 hover:bg-slate-800" @click="cancelMove">
              Cancel
            </button>
          </div>
        </li>
      </ul>
    </div>
  </div>
</template>
