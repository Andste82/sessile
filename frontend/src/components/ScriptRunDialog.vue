<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { api } from '@/api/client'
import AppDialog from './AppDialog.vue'
import type { Script, ScriptRunResult } from '@/api/types'

// Test run (§4.15): call one function with the saved settings and see what
// the agent would get back — redacted exactly as the agent would see it.
const props = defineProps<{ open: boolean; script: Script | null }>()
const emit = defineEmits<{ (e: 'close'): void }>()

const fn = ref('')
const input = ref('{}')
const running = ref(false)
const result = ref<ScriptRunResult | null>(null)

const selected = computed(() => props.script?.functions.find((f) => f.name === fn.value))

// A starting input: the schema's required keys, empty.
function skeleton(): string {
  const schema = selected.value?.input as { required?: string[] } | undefined
  const out: Record<string, string> = {}
  for (const k of schema?.required ?? []) out[k] = ''
  return JSON.stringify(out, null, 2)
}

watch(
  () => props.open,
  (open) => {
    if (!open || !props.script) return
    fn.value = props.script.functions[0]?.name ?? ''
    input.value = skeleton()
    result.value = null
  },
)
watch(fn, () => {
  input.value = skeleton()
  result.value = null
})

const parsed = computed(() => {
  try {
    return { ok: true as const, value: JSON.parse(input.value) as unknown }
  } catch (e) {
    return { ok: false as const, error: e instanceof Error ? e.message : String(e) }
  }
})

async function run() {
  if (!props.script || !parsed.value.ok || running.value) return
  running.value = true
  result.value = null
  try {
    result.value = await api.runScript(props.script.name, fn.value, parsed.value.value)
  } catch (e) {
    result.value = { ok: false, error: e instanceof Error ? e.message : String(e) }
  } finally {
    running.value = false
  }
}

const inputCls =
  'rounded-md border border-slate-600 bg-slate-900 px-3 py-2 text-slate-100 outline-none focus:border-emerald-500'
</script>

<template>
  <AppDialog :open="open" :title="script ? `Test run: ${script.name}` : 'Test run'" wide @close="emit('close')">
    <div v-if="script" class="flex flex-col gap-3">
      <select v-model="fn" :class="inputCls">
        <option v-for="f in script.functions" :key="f.name" :value="f.name">
          {{ f.name }}{{ f.effect === 'write' ? ' (write)' : '' }}
        </option>
      </select>
      <p v-if="selected" class="text-xs text-slate-400">{{ selected.description }}</p>
      <p v-if="selected?.effect === 'write'" class="text-xs text-amber-400">
        This function changes something in the service. Running it here does it for real.
      </p>
      <textarea v-model="input" rows="6" :class="inputCls" class="font-mono text-xs" />
      <p v-if="!parsed.ok" class="text-xs text-rose-400">Input is not JSON: {{ parsed.error }}</p>
      <button
        type="button"
        class="self-start rounded-md bg-emerald-600 px-4 py-2 text-sm font-medium text-white hover:bg-emerald-500 disabled:opacity-50"
        :disabled="!parsed.ok || running"
        @click="run"
      >
        {{ running ? 'Running…' : 'Run' }}
      </button>
      <template v-if="result">
        <p v-if="!result.ok" class="whitespace-pre-wrap break-all text-sm text-rose-400">{{ result.error }}</p>
        <pre
          v-else
          class="max-h-80 overflow-auto rounded-md border border-slate-700 bg-slate-900 p-3 text-xs text-slate-200"
          >{{ JSON.stringify(result.output, null, 2) }}</pre
        >
        <details v-if="result.stderr" class="text-xs text-slate-400">
          <summary>stderr</summary>
          <pre class="mt-1 max-h-40 overflow-auto whitespace-pre-wrap">{{ result.stderr }}</pre>
        </details>
      </template>
    </div>
  </AppDialog>
</template>
