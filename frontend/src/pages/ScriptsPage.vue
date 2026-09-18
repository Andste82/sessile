<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { ArrowUpTrayIcon } from '@heroicons/vue/24/outline'
import { api, ApiRequestError } from '@/api/client'
import AgentNav from '@/components/AgentNav.vue'
import ScriptConfigureDialog from '@/components/ScriptConfigureDialog.vue'
import ScriptRunDialog from '@/components/ScriptRunDialog.vue'
import type { Script, ScriptList } from '@/api/types'

// Script extensions (§4.15): zip in, configure, test, and the task agent gets
// every ready script's functions as tools. Editing happens outside: download
// the zip, change it, upload it again.
const list = ref<ScriptList | null>(null)
const error = ref<string | null>(null)
const busy = ref<string | null>(null)
const armed = ref<string | null>(null)
const configuring = ref<Script | null>(null)
const configureOpen = ref(false)
const running = ref<Script | null>(null)
const runOpen = ref(false)
const fileInput = ref<HTMLInputElement | null>(null)
const pendingUpdate = ref<{ file: File; installed: string; uploaded: string } | null>(null)

async function refresh() {
  try {
    list.value = await api.listScripts()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

// A script's venv is built in the background after an install; poll while
// one is preparing so its status settles without a reload.
let timer: number | undefined
onMounted(async () => {
  await refresh()
  timer = window.setInterval(() => {
    if (list.value?.scripts.some((s) => s.status === 'preparing')) void refresh()
  }, 2000)
})
onUnmounted(() => window.clearInterval(timer))

async function upload(file: File, opts: { as?: string; update?: boolean } = {}) {
  error.value = null
  busy.value = 'upload'
  try {
    const s = await api.uploadScript(file, opts)
    pendingUpdate.value = null
    await refresh()
    if (s.settings.length) openConfigure(s)
  } catch (e) {
    if (e instanceof ApiRequestError && e.code === 'script_exists') {
      pendingUpdate.value = {
        file,
        installed: String(e.details?.installed ?? ''),
        uploaded: String(e.details?.uploaded ?? ''),
      }
    } else {
      error.value = e instanceof Error ? e.message : String(e)
    }
  } finally {
    busy.value = null
  }
}

function onPick(ev: Event) {
  const input = ev.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (file) void upload(file)
}

function installAs() {
  const u = pendingUpdate.value
  if (!u) return
  const name = window.prompt('Install under which name? (lowercase letters, digits and dashes)')
  if (name) void upload(u.file, { as: name })
}

function openConfigure(s: Script) {
  configuring.value = s
  configureOpen.value = true
}

function openRun(s: Script) {
  running.value = s
  runOpen.value = true
}

async function act(name: string, fn: () => Promise<unknown>) {
  busy.value = name
  error.value = null
  armed.value = null
  try {
    await fn()
    await refresh()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = null
  }
}

const statusLabel: Record<Script['status'], string> = {
  preparing: 'preparing',
  needs_setup: 'needs setup',
  check_failed: 'check failed',
  ready: 'ready',
}
const statusCls: Record<Script['status'], string> = {
  preparing: 'text-slate-400 border-slate-600',
  needs_setup: 'text-amber-400 border-amber-600/60',
  check_failed: 'text-rose-400 border-rose-600/60',
  ready: 'text-emerald-400 border-emerald-600/60',
}
const btnCls = 'rounded-md border border-slate-600 px-3 py-1.5 text-xs text-slate-300 hover:bg-slate-700 disabled:opacity-50'
</script>

<template>
  <div class="flex h-full flex-col">
    <header class="border-b border-slate-800 bg-slate-900 px-4 py-4 sm:px-6">
      <h1 class="text-lg font-semibold tracking-tight">Agent</h1>
    </header>
    <AgentNav />

    <main class="mx-auto w-full max-w-3xl flex-1 overflow-y-auto p-4 sm:p-6">
      <p v-if="list && !list.allowed" class="mb-4 rounded-md border border-amber-700/60 p-3 text-sm text-amber-300">
        Scripts are turned off on this server by its administrator. You can still see and configure your scripts, but
        not install or run them, and tasks don't get them as tools.
      </p>

      <div class="mb-4 flex flex-wrap items-center gap-3">
        <button
          type="button"
          class="flex items-center gap-1.5 rounded-md bg-emerald-600 px-3 py-2 text-sm font-medium text-white hover:bg-emerald-500 disabled:opacity-50"
          :disabled="busy === 'upload' || (list !== null && !list.allowed)"
          @click="fileInput?.click()"
        >
          <ArrowUpTrayIcon class="h-4 w-4" /> {{ busy === 'upload' ? 'Installing…' : 'Upload extension' }}
        </button>
        <input ref="fileInput" type="file" accept=".zip,application/zip" class="hidden" @change="onPick" />
        <span class="text-xs text-slate-500">
          A zip with meta.json, main.py and an optional requirements.txt. Scripts run on the sessile server.
        </span>
      </div>

      <div v-if="pendingUpdate" class="mb-4 flex flex-wrap items-center gap-2 rounded-md border border-slate-600 p-3 text-sm">
        <span class="text-slate-300">
          This script is already installed ({{ pendingUpdate.installed }}). Update it to {{ pendingUpdate.uploaded }}? Its
          settings are kept.
        </span>
        <button type="button" :class="btnCls" @click="upload(pendingUpdate.file, { update: true })">Update</button>
        <button type="button" :class="btnCls" @click="installAs">Install as…</button>
        <button type="button" :class="btnCls" @click="pendingUpdate = null">Cancel</button>
      </div>

      <p v-if="error" class="mb-4 whitespace-pre-wrap text-sm text-rose-400">{{ error }}</p>

      <section
        v-for="s in list?.scripts ?? []"
        :key="s.name"
        class="mb-4 rounded-lg border border-slate-700 bg-slate-800/50 p-5"
      >
        <div class="flex flex-wrap items-center gap-2">
          <h2 class="font-medium text-slate-100">{{ s.name }}</h2>
          <span class="text-xs text-slate-500">{{ s.version }}</span>
          <span class="rounded border px-1.5 text-[11px]" :class="statusCls[s.status]">{{ statusLabel[s.status] }}</span>
        </div>
        <p v-if="s.description" class="mt-1 text-sm text-slate-400">{{ s.description }}</p>
        <p v-if="s.missing.length" class="mt-1 text-xs text-amber-400">Missing: {{ s.missing.join(', ') }}</p>
        <p v-if="s.lastCheck && !s.lastCheck.ok" class="mt-1 break-all text-xs text-rose-400">{{ s.lastCheck.message }}</p>
        <p v-if="s.venv === 'failed'" class="mt-1 break-all text-xs text-rose-400">Venv: {{ s.venvError }}</p>
        <ul class="mt-3 flex flex-wrap gap-1.5">
          <li
            v-for="f in s.functions"
            :key="f.name"
            class="rounded bg-slate-900 px-2 py-0.5 font-mono text-xs text-slate-300"
            :title="f.description"
          >
            {{ f.name }}<span v-if="f.effect === 'write'" class="text-amber-400"> (write)</span>
          </li>
        </ul>
        <div class="mt-4 flex flex-wrap gap-2">
          <button type="button" :class="btnCls" @click="openConfigure(s)">Configure</button>
          <button type="button" :class="btnCls" :disabled="!list?.allowed || s.status === 'needs_setup'" @click="openRun(s)">
            Test run
          </button>
          <a :href="`/api/agent/scripts/${s.name}/zip`" :class="btnCls">Download zip</a>
          <button
            type="button"
            :class="btnCls"
            :disabled="!list?.allowed || busy === s.name"
            @click="act(s.name, () => api.rebuildScript(s.name))"
          >
            {{ busy === s.name ? 'Rebuilding…' : 'Rebuild venv' }}
          </button>
          <button
            v-if="armed !== s.name"
            type="button"
            class="ml-auto rounded-md border border-slate-600 px-3 py-1.5 text-xs text-slate-300 hover:border-rose-500 hover:text-rose-400"
            @click="armed = s.name"
          >
            Remove
          </button>
          <button
            v-else
            type="button"
            class="ml-auto rounded-md bg-rose-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-rose-500"
            @click="act(s.name, () => api.removeScript(s.name))"
          >
            Remove, with its settings?
          </button>
        </div>
      </section>

      <p v-for="(why, name) in list?.broken ?? {}" :key="name" class="mb-2 text-xs text-rose-400">
        {{ name }}: {{ why }}
      </p>
      <p v-if="list && list.scripts.length === 0" class="text-sm text-slate-500">
        No scripts yet. A script's functions become tools your task agents can call — read a ticket, check a build,
        search an artifact store.
      </p>
    </main>

    <ScriptConfigureDialog
      :open="configureOpen"
      :script="configuring"
      @close="configureOpen = false"
      @saved="configureOpen = false; refresh()"
    />
    <ScriptRunDialog :open="runOpen" :script="running" @close="runOpen = false" />
  </div>
</template>
