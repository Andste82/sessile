<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { api } from '@/api/client'
import type { ModelsResponse } from '@/api/types'

// Model choice for a connection (§4.12.7): the list fetched with the
// connection's own credentials where the vendor allows it, the CLI's aliases
// otherwise, and always a free-text escape. '' means "the default".
const props = defineProps<{ connectionId: string }>()
const model = defineModel<string>({ required: true })

const data = ref<ModelsResponse | null>(null)
const loading = ref(false)
const custom = ref(false)

async function load(refresh = false) {
  if (!props.connectionId) {
    data.value = null
    return
  }
  loading.value = true
  try {
    data.value = await api.connectionModels(props.connectionId, refresh)
  } catch (e) {
    data.value = {
      models: [],
      listed: false,
      error: e instanceof Error ? e.message : String(e),
      defaultModel: '',
      defaultSource: 'cli',
    }
  } finally {
    loading.value = false
  }
  custom.value = !!model.value && !data.value.models.some((m) => m.id === model.value)
}

watch(() => props.connectionId, () => void load(), { immediate: true })

const defaultLabel = computed(() => {
  const d = data.value
  if (d?.defaultSource === 'connection' && d.defaultModel) return `Default (${d.defaultModel}, from the connection)`
  return 'Default (the agent decides)'
})

function onSelect(v: string) {
  if (v === '__custom__') {
    custom.value = true
    model.value = ''
    return
  }
  custom.value = false
  model.value = v
}

const inputCls =
  'rounded-md border border-slate-600 bg-slate-900 px-3 py-2 text-slate-100 outline-none focus:border-emerald-500'
</script>

<template>
  <div class="flex flex-col gap-1">
    <div class="flex gap-2">
      <select
        :value="custom ? '__custom__' : model"
        :class="inputCls"
        class="min-w-0 flex-1"
        :disabled="loading"
        @change="onSelect(($event.target as HTMLSelectElement).value)"
      >
        <option value="">{{ defaultLabel }}</option>
        <option v-for="m in data?.models ?? []" :key="m.id" :value="m.id">
          {{ m.name === m.id ? m.id : `${m.name} (${m.id})` }}
        </option>
        <option value="__custom__">Other…</option>
      </select>
      <button
        type="button"
        class="rounded-md border border-slate-600 px-2 text-xs text-slate-300 hover:bg-slate-700 disabled:opacity-50"
        :disabled="loading || !connectionId"
        title="Fetch the list again"
        @click="load(true)"
      >
        Refresh
      </button>
    </div>
    <input v-if="custom" v-model="model" type="text" placeholder="model id" :class="inputCls" />
    <span v-if="loading" class="text-xs text-slate-500">Loading models…</span>
    <span v-else-if="data?.error" class="text-xs text-amber-400">Couldn't list models: {{ data.error }}</span>
    <span v-else-if="data && !data.listed" class="text-xs text-slate-500">
      This connection can't list models; these are the names the agent itself understands.
    </span>
  </div>
</template>
