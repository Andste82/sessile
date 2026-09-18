<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { api } from '@/api/client'
import { useAgentStore } from '@/stores/agent'
import AppDialog from './AppDialog.vue'
import PasswordInput from './PasswordInput.vue'
import type { Script, ScriptCheck, ScriptSettingsBody } from '@/api/types'

// The Configure form (§4.15.3), generated from the script's settings. Secrets
// are write-only; a blank one keeps the saved value. A secret can instead take
// one of the user's Git account tokens, so that token is kept once.
const props = defineProps<{ open: boolean; script: Script | null }>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'saved', s: Script): void }>()

const agent = useAgentStore()
const values = ref<Record<string, string>>({})
const git = ref<Record<string, string>>({})
const saving = ref(false)
const error = ref<string | null>(null)
const testing = ref(false)
const check = ref<ScriptCheck | null>(null)

const gitHosts = computed(() => agent.settings?.git.map((g) => g.host) ?? [])

watch(
  () => props.open,
  (open) => {
    if (!open || !props.script) return
    error.value = null
    check.value = null
    const v: Record<string, string> = {}
    const g: Record<string, string> = {}
    for (const s of props.script.settings) {
      v[s.name] = s.type === 'secret' ? '' : (s.value ?? '')
      g[s.name] = s.git ?? ''
    }
    values.value = v
    git.value = g
    if (!agent.settings) void agent.load()
  },
)

function body(): ScriptSettingsBody {
  const out: ScriptSettingsBody = { values: {}, git: {} }
  for (const s of props.script?.settings ?? []) {
    const v = values.value[s.name] ?? ''
    if (s.type === 'secret') {
      out.git[s.name] = git.value[s.name] ?? ''
      if (!git.value[s.name] && v) out.values[s.name] = v
    } else {
      out.values[s.name] = v
    }
  }
  return out
}

async function test() {
  if (!props.script) return
  testing.value = true
  check.value = null
  try {
    check.value = await api.checkScript(props.script.name, body())
  } catch (e) {
    check.value = { ok: false, message: e instanceof Error ? e.message : String(e), at: '' }
  } finally {
    testing.value = false
  }
}

async function save() {
  if (!props.script || saving.value) return
  saving.value = true
  error.value = null
  try {
    emit('saved', await api.putScriptSettings(props.script.name, body()))
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    saving.value = false
  }
}

const labelCls = 'flex flex-col gap-1 text-sm'
const inputCls =
  'rounded-md border border-slate-600 bg-slate-900 px-3 py-2 text-slate-100 outline-none focus:border-emerald-500'
</script>

<template>
  <AppDialog :open="open" :title="script ? `Configure ${script.name}` : 'Configure'" @close="emit('close')">
    <form v-if="script" class="flex flex-col gap-4" @submit.prevent="save">
      <p v-if="script.description" class="text-sm text-slate-400">{{ script.description }}</p>
      <p v-if="script.settings.length === 0" class="text-sm text-slate-400">This script has no settings.</p>
      <template v-for="s in script.settings" :key="s.name">
        <label v-if="s.type === 'bool'" class="flex items-center gap-2 text-sm text-slate-200">
          <input
            type="checkbox"
            class="accent-emerald-400"
            :checked="values[s.name] === 'true'"
            @change="values[s.name] = ($event.target as HTMLInputElement).checked ? 'true' : 'false'"
          />
          {{ s.label }}
        </label>
        <div v-else :class="labelCls">
          <span class="text-slate-400">
            {{ s.label }}<span v-if="!s.required" class="text-slate-500"> (optional)</span>
            <span v-if="s.context" class="text-xs text-slate-500"> · shown to the agent</span>
          </span>
          <select v-if="s.type === 'choice'" v-model="values[s.name]" :class="inputCls">
            <option value="">—</option>
            <option v-for="o in s.options" :key="o" :value="o">{{ o }}</option>
          </select>
          <template v-else-if="s.type === 'secret'">
            <select v-if="gitHosts.length" v-model="git[s.name]" :class="inputCls">
              <option value="">Enter a value</option>
              <option v-for="h in gitHosts" :key="h" :value="h">Use my Git account token: {{ h }}</option>
            </select>
            <PasswordInput
              v-if="!git[s.name]"
              v-model="values[s.name]"
              autocomplete="off"
              :placeholder="s.set ? 'Leave blank to keep the saved value' : ''"
            />
          </template>
          <input v-else v-model="values[s.name]" type="text" :class="inputCls" />
          <span v-if="s.help" class="text-xs text-slate-500">{{ s.help }}</span>
        </div>
      </template>

      <div v-if="script.check" class="flex flex-col gap-1">
        <button
          type="button"
          class="self-start rounded-md border border-slate-600 px-3 py-1.5 text-xs text-slate-200 hover:bg-slate-700 disabled:opacity-50"
          :disabled="testing"
          @click="test"
        >
          {{ testing ? 'Testing…' : 'Test connection' }}
        </button>
        <p v-if="check" class="whitespace-pre-wrap break-all text-xs" :class="check.ok ? 'text-emerald-400' : 'text-rose-400'">
          {{ check.message }}
        </p>
      </div>
      <p v-if="error" class="text-sm text-rose-400">{{ error }}</p>
      <div class="mt-2 flex justify-end gap-3">
        <button type="button" class="rounded-md px-4 py-2 text-sm text-slate-300 hover:bg-slate-700" @click="emit('close')">
          Cancel
        </button>
        <button
          type="submit"
          :disabled="saving"
          class="rounded-md bg-emerald-600 px-4 py-2 text-sm font-medium text-white hover:bg-emerald-500 disabled:opacity-50"
        >
          {{ saving ? 'Saving…' : 'Save' }}
        </button>
      </div>
    </form>
  </AppDialog>
</template>
