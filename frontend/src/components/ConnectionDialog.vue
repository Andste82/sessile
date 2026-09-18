<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { api } from '@/api/client'
import { useAgentStore } from '@/stores/agent'
import { uuidv4 } from '@/utils/uuid'
import AppDialog from './AppDialog.vue'
import PasswordInput from './PasswordInput.vue'
import type { AgentName, Connection, TestResult } from '@/api/types'

// The guided "Add connection" dialog (§4.13): pick the agent and the kind of
// account, follow the steps to get the token, paste it, test it.
const props = defineProps<{ open: boolean; connection: Connection | null }>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'saved'): void }>()

const store = useAgentStore()

const agents: { value: AgentName; label: string }[] = [
  { value: 'claude', label: 'Claude Code' },
  { value: 'codex', label: 'Codex' },
  { value: 'gemini', label: 'Gemini' },
]

const agent = ref<AgentName>('claude')
const kindId = ref('')
const name = ref('')
const fields = ref<Record<string, string>>({})
const expires = ref('')
const saving = ref(false)
const error = ref<string | null>(null)
const testing = ref(false)
const test = ref<TestResult | null>(null)

const isEdit = computed(() => props.connection !== null)
const kindsForAgent = computed(() => store.kinds.filter((k) => k.agent === agent.value))
const kind = computed(() => store.kind(kindId.value))

function dateIn(days: number) {
  const d = new Date(Date.now() + days * 86400000)
  return d.toISOString().slice(0, 10)
}

watch(
  () => props.open,
  (open) => {
    if (!open) return
    error.value = null
    test.value = null
    const c = props.connection
    if (c) {
      agent.value = c.agent
      kindId.value = c.kind
      name.value = c.name
      fields.value = { ...c.fields }
      expires.value = c.expires ? c.expires.slice(0, 10) : ''
    } else {
      agent.value = 'claude'
      pickKind(kindsForAgent.value[0]?.id ?? '')
    }
  },
)

function pickKind(id: string) {
  kindId.value = id
  fields.value = {}
  test.value = null
  const k = store.kind(id)
  name.value = k?.label ?? ''
  expires.value = k?.defaultExpiryDays ? dateIn(k.defaultExpiryDays) : ''
}

watch(agent, () => {
  if (!isEdit.value && kind.value?.agent !== agent.value) pickKind(kindsForAgent.value[0]?.id ?? '')
})

function secretSaved(f: string) {
  return isEdit.value && props.connection?.kind === kindId.value && props.connection?.secretsSet[f]
}

const canSave = computed(() => {
  const k = kind.value
  if (!k || !name.value.trim()) return false
  return k.fields.every((f) => !f.required || (fields.value[f.name] ?? '').trim() || secretSaved(f.name))
})

// Only what the user typed goes out for secrets; a blank one keeps the saved
// value (the server fills it in for both Test and Save).
function payloadFields() {
  const out: Record<string, string> = {}
  for (const f of kind.value?.fields ?? []) {
    const v = (fields.value[f.name] ?? '').trim()
    if (f.type === 'secret' && !v) continue
    out[f.name] = v
  }
  return out
}

async function runTest() {
  if (!kind.value) return
  testing.value = true
  test.value = null
  try {
    test.value = await api.testConnection({
      id: props.connection?.id,
      kind: kindId.value,
      fields: payloadFields(),
    })
  } catch (e) {
    test.value = { ok: false, error: e instanceof Error ? e.message : String(e) }
  } finally {
    testing.value = false
  }
}

async function submit() {
  if (!canSave.value || saving.value) return
  saving.value = true
  error.value = null
  try {
    await store.saveConnection({
      id: props.connection?.id ?? uuidv4(),
      name: name.value.trim(),
      kind: kindId.value,
      fields: payloadFields(),
      expires: expires.value,
    })
    emit('saved')
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
  <AppDialog :open="open" :title="isEdit ? 'Edit connection' : 'Add connection'" wide @close="emit('close')">
    <form class="flex flex-col gap-4" @submit.prevent="submit">
      <div v-if="!isEdit" class="flex flex-col gap-2">
        <span class="text-sm text-slate-400">Agent</span>
        <div class="flex gap-2">
          <button
            v-for="a in agents"
            :key="a.value"
            type="button"
            class="rounded-md border px-3 py-1.5 text-sm"
            :class="agent === a.value ? 'border-emerald-500 text-emerald-300' : 'border-slate-600 text-slate-300 hover:bg-slate-700'"
            @click="agent = a.value"
          >
            {{ a.label }}
          </button>
        </div>
      </div>

      <div class="flex flex-col gap-2">
        <span class="text-sm text-slate-400">Account</span>
        <label
          v-for="k in kindsForAgent"
          :key="k.id"
          class="flex cursor-pointer items-start gap-2 text-sm text-slate-200"
          :class="isEdit && k.id !== kindId ? 'opacity-60' : ''"
        >
          <input
            type="radio"
            class="mt-1 accent-emerald-400"
            :value="k.id"
            :checked="kindId === k.id"
            @change="pickKind(k.id)"
          />
          <span>
            {{ k.label }}
            <span v-if="k.enterprise" class="ml-1 text-xs text-slate-500">enterprise</span>
          </span>
        </label>
      </div>

      <ol
        v-if="kind"
        class="list-decimal space-y-1 rounded-md border border-slate-700 bg-slate-900/60 py-3 pl-8 pr-3 text-xs text-slate-300"
      >
        <li v-for="(step, i) in kind.steps" :key="i">{{ step }}</li>
      </ol>

      <label :class="labelCls">
        <span class="text-slate-400">Name</span>
        <input v-model="name" type="text" maxlength="64" :class="inputCls" />
      </label>

      <template v-if="kind">
        <label v-for="f in kind.fields" :key="f.name" :class="labelCls">
          <span class="text-slate-400">
            {{ f.label }}<span v-if="!f.required" class="text-slate-500"> (optional)</span>
          </span>
          <PasswordInput
            v-if="f.type === 'secret'"
            v-model="fields[f.name]"
            autocomplete="off"
            :placeholder="secretSaved(f.name) ? 'Leave blank to keep the saved value' : ''"
          />
          <input v-else v-model="fields[f.name]" type="text" :placeholder="f.help ?? ''" :class="inputCls" />
        </label>
      </template>

      <label :class="labelCls">
        <span class="text-slate-400">Expires <span class="text-slate-500">(optional)</span></span>
        <input v-model="expires" type="date" :class="inputCls" />
      </label>

      <div class="flex flex-col gap-1">
        <button
          type="button"
          class="self-start rounded-md border border-slate-600 px-3 py-1.5 text-xs text-slate-200 hover:bg-slate-700 disabled:opacity-50"
          :disabled="!kind || testing"
          @click="runTest"
        >
          {{ testing ? 'Testing…' : 'Test' }}
        </button>
        <p v-if="test?.ok" class="text-xs text-emerald-400">{{ test.detail }}</p>
        <p v-else-if="test" class="text-xs text-rose-400">{{ test.error }}</p>
      </div>

      <p v-if="error" class="text-sm text-rose-400">{{ error }}</p>

      <div class="mt-2 flex justify-end gap-3">
        <button type="button" class="rounded-md px-4 py-2 text-sm text-slate-300 hover:bg-slate-700" @click="emit('close')">
          Cancel
        </button>
        <button
          type="submit"
          :disabled="!canSave || saving"
          class="rounded-md bg-emerald-600 px-4 py-2 text-sm font-medium text-white hover:bg-emerald-500 disabled:cursor-not-allowed disabled:opacity-50"
        >
          {{ saving ? 'Saving…' : 'Save' }}
        </button>
      </div>
    </form>
  </AppDialog>
</template>
