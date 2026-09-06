<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import {
  FolderIcon,
  DocumentIcon,
  ArrowPathIcon,
  ArrowUpIcon,
  ArrowUpTrayIcon,
  XMarkIcon,
} from '@heroicons/vue/20/solid'
import { api } from '@/api/client'
import { hostFileDownloadURL } from '@/api/upload'
import { useTransfersStore } from '@/stores/transfers'
import { copyText } from '@/utils/clipboard'
import RowActionsMenu, { type MenuItem } from '@/components/RowActionsMenu.vue'
import type { HostDirEntry } from '@/api/types'

const props = defineProps<{ sessionId: string }>()

const currentPath = ref('') // "" means the target's own default root
const entries = ref<HostDirEntry[]>([])
const loading = ref(false)
const error = ref<string | null>(null)
const movingName = ref<string | null>(null)
const moveTarget = ref('')
const copyingName = ref<string | null>(null)
const copyTarget = ref('')
const confirmingDeleteName = ref<string | null>(null)
const fileInput = ref<HTMLInputElement | null>(null)
const transfers = useTransfersStore()

// Upload and Delete/Copy progress live in the store, not here: this component
// is unmounted whenever the Processes tab is selected or the panel is closed,
// while the work it was showing carries on. See stores/transfers.ts.
const upload = computed(() => transfers.uploads[props.sessionId])
const activeOp = computed(() => transfers.ops[props.sessionId])

// Refresh the listing when an operation finishes while we are mounted. If it
// finishes while we are not, onMounted's load() picks up the result anyway.
watch(
  () => activeOp.value?.status,
  (status, before) => {
    if (before === 'running' && (status === 'ok' || status === 'error')) void load(currentPath.value)
  },
)

// Breadcrumbs are derived from currentPath itself — the server's own
// canonical form of wherever was last listed (§4.10, §6) — rather than a
// separately-tracked client-side stack. For an SSH target that's a real
// absolute path (the target has no sandbox, so it's resolved via SFTP's
// own REALPATH, not a synthetic starting point), which is what makes
// navigating to any ancestor segment — and from there into any sibling,
// not just back into where the panel first opened — actually work. A
// local target stays relative to the sandbox root (§4.5) — "." at the
// top, never absolute — since there's nothing above that root to show.
const isAbsolute = computed(() => currentPath.value.startsWith('/'))
const segments = computed(() => currentPath.value.split('/').filter(Boolean))
const rootLabel = computed(() => (isAbsolute.value ? '/' : 'root'))
const atRoot = computed(() => segments.value.length === 0)

function joinPath(base: string, name: string): string {
  if (base === '' || base === '.') return name
  if (base === '/') return `/${name}`
  return `${base}/${name}`
}

function pathForSegments(count: number): string {
  const parts = segments.value.slice(0, count)
  if (isAbsolute.value) return parts.length === 0 ? '/' : `/${parts.join('/')}`
  return parts.join('/') // '' at the root, which the backend treats as "."
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
  void load(joinPath(currentPath.value, entry.name))
}

// index -1 is the root.
function goToCrumb(index: number) {
  void load(pathForSegments(index + 1))
}

function goUp() {
  if (atRoot.value) return
  void load(pathForSegments(segments.value.length - 1))
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

function startCopy(entry: HostDirEntry) {
  copyingName.value = entry.name
  copyTarget.value = joinPath(currentPath.value, `${entry.name}-copy`)
}

function cancelCopy() {
  copyingName.value = null
  copyTarget.value = ''
}

async function confirmCopy() {
  if (copyingName.value === null) return
  const src = joinPath(currentPath.value, copyingName.value)
  const dst = copyTarget.value.trim()
  const name = copyingName.value
  if (!dst || dst === src) {
    cancelCopy()
    return
  }
  cancelCopy()
  try {
    await transfers.startOp(props.sessionId, 'copy', name, () =>
      api.copyHostFile(props.sessionId, src, dst),
    )
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

function armDelete(entry: HostDirEntry) {
  confirmingDeleteName.value = entry.name
}

function cancelDelete() {
  confirmingDeleteName.value = null
}

async function confirmDelete(entry: HostDirEntry) {
  confirmingDeleteName.value = null
  try {
    await transfers.startOp(props.sessionId, 'delete', entry.name, () =>
      api.deleteHostFile(props.sessionId, joinPath(currentPath.value, entry.name)),
    )
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

function downloadURL(entry: HostDirEntry): string {
  return hostFileDownloadURL(props.sessionId, joinPath(currentPath.value, entry.name))
}

function pickUploadFile() {
  fileInput.value?.click()
}

async function onFileSelected(e: Event) {
  const input = e.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = '' // allow re-selecting the same file next time
  if (!file) return

  try {
    const stored = await transfers.startUpload(
      props.sessionId,
      joinPath(currentPath.value, file.name),
      file,
    )
    // Cancelled uploads leave nothing behind — the server removes its own
    // staging file — so there is nothing new to list.
    if (stored) await load(currentPath.value)
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  }
}

function cancelUpload() {
  transfers.cancelUpload(props.sessionId)
}

// Actions for one row, as menu items rather than a strip of icon buttons —
// see RowActionsMenu for why.
//
// "Copy path" means different things per target type, so the items differ.
// An SSH session's paths are absolute (the target is unsandboxed, and the
// server resolves them through SFTP's REALPATH), so both an absolute and a
// current-directory-relative form are meaningful. A local session's paths are
// relative to the sandbox root and the absolute path on the server is
// deliberately never exposed by the API — there, one item is all there is,
// and offering two that produce identical text would be worse than one.
function menuItems(entry: HostDirEntry): MenuItem[] {
  const items: MenuItem[] = []
  if (!entry.isDir) items.push({ key: 'download', label: 'Download' })
  items.push({ key: 'move', label: 'Move / rename' })
  items.push({ key: 'copy', label: 'Copy to…' })
  items.push({ key: 'copy-path', label: isAbsolute.value ? 'Copy absolute path' : 'Copy path' })
  if (isAbsolute.value) items.push({ key: 'copy-relative', label: 'Copy relative path' })
  items.push({ key: 'delete', label: 'Delete', danger: true })
  return items
}

function onMenuSelect(entry: HostDirEntry, key: string) {
  switch (key) {
    case 'download':
      // A menu item can't be an <a download>, so trigger the same native
      // navigation the link did: same URL, same browser download manager.
      triggerDownload(downloadURL(entry))
      break
    case 'move':
      startMove(entry)
      break
    case 'copy':
      startCopy(entry)
      break
    case 'copy-path':
      copyToClipboard(joinPath(currentPath.value, entry.name))
      break
    case 'copy-relative':
      copyToClipboard(entry.name)
      break
    case 'delete':
      armDelete(entry)
      break
  }
}

function triggerDownload(url: string) {
  const a = document.createElement('a')
  a.href = url
  a.download = ''
  document.body.appendChild(a)
  a.click()
  a.remove()
}

const copied = ref<string | null>(null)

function copyToClipboard(text: string) {
  if (copyText(text)) {
    copied.value = text
    setTimeout(() => {
      if (copied.value === text) copied.value = null
    }, 1500)
  } else {
    error.value = 'Could not copy to the clipboard.'
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
  () => load(''),
)
</script>

<template>
  <div class="flex h-full flex-col">
    <div class="flex items-center justify-between border-b border-slate-800 px-3 py-2">
      <div class="flex min-w-0 items-center gap-0.5">
        <button
          type="button"
          class="flex h-5 w-5 shrink-0 items-center justify-center rounded text-slate-400 hover:bg-slate-800 hover:text-slate-200 disabled:opacity-30"
          :disabled="atRoot"
          title="Up one level"
          @click="goUp"
        >
          <ArrowUpIcon class="h-3.5 w-3.5" />
        </button>
        <div class="flex min-w-0 flex-wrap items-center gap-0.5 text-xs text-slate-400">
          <button type="button" class="rounded px-1 hover:bg-slate-800 hover:text-slate-200" @click="goToCrumb(-1)">
            {{ rootLabel }}
          </button>
          <template v-for="(seg, i) in segments" :key="i">
            <span class="text-slate-600">/</span>
            <button type="button" class="rounded px-1 hover:bg-slate-800 hover:text-slate-200" @click="goToCrumb(i)">
              {{ seg }}
            </button>
          </template>
        </div>
      </div>
      <div class="flex shrink-0 items-center gap-1">
        <input ref="fileInput" type="file" class="hidden" @change="onFileSelected" />
        <button
          type="button"
          class="flex h-6 w-6 items-center justify-center rounded text-slate-400 hover:bg-slate-800 hover:text-slate-200 disabled:opacity-50"
          :disabled="!!upload"
          title="Upload a file here"
          @click="pickUploadFile"
        >
          <ArrowUpTrayIcon class="h-3.5 w-3.5" />
        </button>
        <button
          type="button"
          class="flex h-6 w-6 items-center justify-center rounded text-slate-400 hover:bg-slate-800 hover:text-slate-200 disabled:opacity-50"
          :disabled="loading"
          title="Refresh"
          @click="load(currentPath)"
        >
          <ArrowPathIcon class="h-3.5 w-3.5" :class="{ 'animate-spin': loading }" />
        </button>
      </div>
    </div>

    <div v-if="upload" class="border-b border-slate-800 px-3 py-2 text-xs">
      <div class="flex items-center justify-between gap-2 text-slate-400">
        <span class="truncate">
          Uploading {{ upload.name
          }}<template v-if="upload.status === 'error'"> — failed</template
          ><template v-else-if="upload.status === 'cancelled'"> — cancelled</template>
        </span>
        <div class="flex shrink-0 items-center gap-1">
          <span v-if="upload.total > 0" class="tabular-nums">{{ formatSize(upload.loaded) }}/{{ formatSize(upload.total) }}</span>
          <button
            v-if="upload.status === 'running'"
            type="button"
            class="flex h-8 w-8 items-center justify-center rounded text-slate-400 hover:bg-slate-800 hover:text-rose-400"
            aria-label="Cancel upload"
            title="Cancel upload"
            @click="cancelUpload"
          >
            <XMarkIcon class="h-4 w-4" />
          </button>
        </div>
      </div>
      <div class="mt-1 h-1 overflow-hidden rounded-full bg-slate-800">
        <div
          class="h-full rounded-full transition-all"
          :class="upload.status === 'running' ? 'bg-emerald-500' : upload.status === 'error' ? 'bg-rose-500' : 'bg-slate-600'"
          :style="{ width: upload.total > 0 ? `${Math.min(100, (upload.loaded / upload.total) * 100)}%` : '0%' }"
        />
      </div>
    </div>

    <div v-if="activeOp" class="border-b border-slate-800 px-3 py-2 text-xs">
      <div class="flex items-center justify-between text-slate-400">
        <span class="truncate">
          {{ activeOp.kind === 'delete' ? 'Deleting' : 'Copying' }} {{ activeOp.entryName }}<template v-if="activeOp.status === 'error'"> — failed</template>
        </span>
        <span v-if="activeOp.total > 0" class="shrink-0 tabular-nums">{{ activeOp.done }}/{{ activeOp.total }}</span>
      </div>
      <div class="mt-1 h-1 overflow-hidden rounded-full bg-slate-800">
        <div
          class="h-full rounded-full transition-all"
          :class="activeOp.status === 'error' ? 'bg-rose-500' : 'bg-emerald-500'"
          :style="{ width: activeOp.total > 0 ? `${Math.min(100, (activeOp.done / activeOp.total) * 100)}%` : '100%' }"
        />
      </div>
      <p v-if="activeOp.status === 'error'" class="mt-1 text-rose-400">{{ activeOp.message }}</p>
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
            <span v-if="!entry.isDir" class="shrink-0 text-slate-500">{{ entry.isRegular ? formatSize(entry.size) : 'special file' }}</span>

            <template v-if="confirmingDeleteName === entry.name">
              <button
                type="button"
                class="flex min-h-[2.75rem] items-center rounded bg-rose-600 px-3 text-xs text-white hover:bg-rose-500"
                @click="confirmDelete(entry)"
              >
                Delete
              </button>
              <button
                type="button"
                class="flex min-h-[2.75rem] items-center rounded px-3 text-xs text-slate-400 hover:bg-slate-800"
                @click="cancelDelete"
              >
                Cancel
              </button>
            </template>
            <RowActionsMenu
              v-else
              :items="menuItems(entry)"
              :label="entry.name"
              @select="onMenuSelect(entry, $event)"
            />
          </div>

          <p v-if="copied === joinPath(currentPath, entry.name) || copied === entry.name" class="pl-6 text-[11px] text-emerald-400">
            Copied to clipboard.
          </p>

          <div v-if="movingName === entry.name" class="mt-1 flex items-center gap-1.5 pl-6">
            <input
              v-model="moveTarget"
              type="text"
              class="min-w-0 flex-1 rounded border border-slate-600 bg-slate-950 px-1.5 py-1.5 text-xs text-slate-100 outline-none focus:border-emerald-500"
              placeholder="new/path/for/this"
              @keyup.enter="confirmMove"
              @keyup.escape="cancelMove"
            />
            <button
              type="button"
              class="flex min-h-[2.75rem] items-center rounded bg-emerald-600 px-3 text-xs text-white hover:bg-emerald-500"
              @click="confirmMove"
            >
              Move
            </button>
            <button
              type="button"
              class="flex min-h-[2.75rem] items-center rounded px-3 text-xs text-slate-400 hover:bg-slate-800"
              @click="cancelMove"
            >
              Cancel
            </button>
          </div>

          <div v-if="copyingName === entry.name" class="mt-1 flex items-center gap-1.5 pl-6">
            <input
              v-model="copyTarget"
              type="text"
              class="min-w-0 flex-1 rounded border border-slate-600 bg-slate-950 px-1.5 py-1.5 text-xs text-slate-100 outline-none focus:border-emerald-500"
              placeholder="path/for/the/copy"
              @keyup.enter="confirmCopy"
              @keyup.escape="cancelCopy"
            />
            <button
              type="button"
              class="flex min-h-[2.75rem] items-center rounded bg-emerald-600 px-3 text-xs text-white hover:bg-emerald-500"
              @click="confirmCopy"
            >
              Copy
            </button>
            <button
              type="button"
              class="flex min-h-[2.75rem] items-center rounded px-3 text-xs text-slate-400 hover:bg-slate-800"
              @click="cancelCopy"
            >
              Cancel
            </button>
          </div>
        </li>
      </ul>
    </div>
  </div>
</template>
