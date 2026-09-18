<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { PlusIcon } from '@heroicons/vue/24/outline'
import { api } from '@/api/client'
import AgentNav from '@/components/AgentNav.vue'
import type { Note, NoteContext } from '@/api/types'

// Notes (§4.14): markdown context every task gets. "Always" notes go straight
// into the agent's instructions; the rest wait in the task's notes/ folder.
const notes = ref<Note[]>([])
const selected = ref<string | null>(null)
const creating = ref(false)
const slug = ref('')
const context = ref<NoteContext>('on-demand')
const body = ref('')
const saved = ref('')
const savedContext = ref<NoteContext>('on-demand')
const warnings = ref<string[]>([])
const error = ref<string | null>(null)
const saving = ref(false)
const armedDelete = ref(false)

const dirty = computed(() => body.value !== saved.value || context.value !== savedContext.value)
const slugValid = computed(() => /^[a-z0-9][a-z0-9-]{0,63}$/.test(slug.value))

async function refresh() {
  try {
    notes.value = await api.listNotes()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

onMounted(refresh)

async function open(n: Note) {
  error.value = null
  armedDelete.value = false
  creating.value = false
  try {
    const full = await api.getNote(n.slug)
    selected.value = full.slug
    slug.value = full.slug
    context.value = full.context
    body.value = full.body ?? ''
    saved.value = body.value
    savedContext.value = full.context
    warnings.value = full.warnings ?? []
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

function startNew() {
  selected.value = null
  creating.value = true
  slug.value = ''
  context.value = 'on-demand'
  body.value = ''
  saved.value = ''
  warnings.value = []
  error.value = null
}

async function save() {
  if (!slugValid.value || saving.value) return
  saving.value = true
  error.value = null
  try {
    const n = await api.putNote(slug.value, context.value, body.value)
    saved.value = body.value
    savedContext.value = context.value
    warnings.value = n.warnings ?? []
    selected.value = n.slug
    creating.value = false
    await refresh()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    saving.value = false
  }
}

async function remove() {
  if (!selected.value) return
  try {
    await api.deleteNote(selected.value)
    selected.value = null
    armedDelete.value = false
    body.value = saved.value = ''
    await refresh()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

const inputCls =
  'rounded-md border border-slate-600 bg-slate-900 px-3 py-2 text-sm text-slate-100 outline-none focus:border-emerald-500'
</script>

<template>
  <div class="flex h-full flex-col">
    <header class="border-b border-slate-800 bg-slate-900 px-4 py-4 sm:px-6">
      <h1 class="text-lg font-semibold tracking-tight">Agent</h1>
    </header>
    <AgentNav />

    <main class="mx-auto flex w-full max-w-5xl flex-1 flex-col gap-4 overflow-y-auto p-4 sm:flex-row sm:p-6">
      <aside class="flex shrink-0 flex-col gap-2 sm:w-56">
        <button
          type="button"
          class="flex items-center gap-1.5 rounded-md border border-slate-600 px-3 py-2 text-sm text-slate-200 hover:bg-slate-800"
          @click="startNew"
        >
          <PlusIcon class="h-4 w-4" /> New note
        </button>
        <button
          v-for="n in notes"
          :key="n.slug"
          type="button"
          class="flex flex-col rounded-md px-3 py-2 text-left text-sm hover:bg-slate-800"
          :class="selected === n.slug ? 'bg-slate-800 text-slate-100' : 'text-slate-300'"
          @click="open(n)"
        >
          <span class="truncate">{{ n.title }}</span>
          <span class="text-xs text-slate-500">
            {{ n.slug }}<span v-if="n.context === 'always'"> · always</span>
          </span>
        </button>
        <p v-if="notes.length === 0" class="px-3 text-xs text-slate-500">
          No notes yet. Notes give your tasks context: which repo is what, how your team works, your ticket workflow.
        </p>
      </aside>

      <section v-if="selected || creating" class="flex min-w-0 flex-1 flex-col gap-3">
        <div class="flex flex-wrap items-end gap-3">
          <label class="flex flex-col gap-1 text-sm">
            <span class="text-slate-400">Name</span>
            <input v-model="slug" type="text" :disabled="!creating" placeholder="repos" :class="inputCls" />
          </label>
          <label class="flex flex-col gap-1 text-sm">
            <span class="text-slate-400">Give to tasks</span>
            <select v-model="context" :class="inputCls">
              <option value="on-demand">When relevant (in notes/)</option>
              <option value="always">Always (in the instructions)</option>
            </select>
          </label>
        </div>
        <p v-if="creating && slug && !slugValid" class="text-xs text-amber-400">
          Lowercase letters, digits and dashes.
        </p>
        <textarea
          v-model="body"
          rows="18"
          placeholder="# Repos&#10;&#10;- https://github.com/games-on-whales/wolf: the streaming server"
          class="min-h-64 flex-1 rounded-md border border-slate-600 bg-slate-900 px-3 py-2 font-mono text-sm text-slate-100 outline-none focus:border-emerald-500"
        />
        <p v-for="w in warnings" :key="w" class="text-xs text-amber-400">{{ w }}</p>
        <p v-if="error" class="text-sm text-rose-400">{{ error }}</p>
        <div class="flex items-center gap-3">
          <button
            type="button"
            class="rounded-md bg-emerald-600 px-4 py-2 text-sm font-medium text-white hover:bg-emerald-500 disabled:opacity-50"
            :disabled="!slugValid || saving || (!creating && !dirty)"
            @click="save"
          >
            {{ saving ? 'Saving…' : 'Save' }}
          </button>
          <span v-if="dirty" class="text-xs text-slate-500">Unsaved changes</span>
          <template v-if="selected">
            <button
              v-if="!armedDelete"
              type="button"
              class="ml-auto rounded-md border border-slate-600 px-3 py-1.5 text-xs text-slate-300 hover:border-rose-500 hover:text-rose-400"
              @click="armedDelete = true"
            >
              Delete
            </button>
            <button
              v-else
              type="button"
              class="ml-auto rounded-md bg-rose-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-rose-500"
              @click="remove"
            >
              Confirm delete?
            </button>
          </template>
        </div>
      </section>
      <p v-else class="flex-1 text-sm text-slate-500">Pick a note, or start a new one.</p>
    </main>
  </div>
</template>
