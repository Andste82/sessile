<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useAgentStore } from '@/stores/agent'
import { uuidv4 } from '@/utils/uuid'
import AppDialog from './AppDialog.vue'
import ModelPicker from './ModelPicker.vue'
import type { Profile } from '@/api/types'

// A profile is what a task picks (§4.13): a connection (which fixes the
// agent) and optionally a model.
const props = defineProps<{ open: boolean; profile: Profile | null }>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'saved'): void }>()

const store = useAgentStore()
const name = ref('')
const connectionId = ref('')
const model = ref('')
const saving = ref(false)
const error = ref<string | null>(null)

const connections = computed(() => store.settings?.connections ?? [])

watch(
  () => props.open,
  (open) => {
    if (!open) return
    error.value = null
    name.value = props.profile?.name ?? ''
    connectionId.value = props.profile?.connectionId ?? connections.value[0]?.id ?? ''
    model.value = props.profile?.model ?? ''
  },
)

watch(connectionId, (id, old) => {
  if (old && id !== old) model.value = ''
  if (!name.value.trim() || name.value === suggested(old)) name.value = suggested(id)
})

function suggested(id: string | undefined) {
  const c = id ? store.connection(id) : undefined
  return c ? `${c.agent} (${c.name})` : ''
}

async function submit() {
  if (!name.value.trim() || !connectionId.value || saving.value) return
  saving.value = true
  error.value = null
  try {
    await store.saveProfile({
      id: props.profile?.id ?? uuidv4(),
      name: name.value.trim(),
      connectionId: connectionId.value,
      model: model.value.trim(),
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
  <AppDialog :open="open" :title="profile ? 'Edit profile' : 'Add profile'" @close="emit('close')">
    <form class="flex flex-col gap-4" @submit.prevent="submit">
      <label :class="labelCls">
        <span class="text-slate-400">Connection</span>
        <select v-model="connectionId" :class="inputCls">
          <option v-for="c in connections" :key="c.id" :value="c.id">{{ c.agent }}: {{ c.name }}</option>
        </select>
      </label>
      <label :class="labelCls">
        <span class="text-slate-400">Name</span>
        <input v-model="name" type="text" maxlength="64" :class="inputCls" />
      </label>
      <div :class="labelCls">
        <span class="text-slate-400">Model</span>
        <ModelPicker v-model="model" :connection-id="connectionId" />
      </div>
      <p v-if="error" class="text-sm text-rose-400">{{ error }}</p>
      <div class="mt-2 flex justify-end gap-3">
        <button type="button" class="rounded-md px-4 py-2 text-sm text-slate-300 hover:bg-slate-700" @click="emit('close')">
          Cancel
        </button>
        <button
          type="submit"
          :disabled="!name.trim() || !connectionId || saving"
          class="rounded-md bg-emerald-600 px-4 py-2 text-sm font-medium text-white hover:bg-emerald-500 disabled:cursor-not-allowed disabled:opacity-50"
        >
          {{ saving ? 'Saving…' : 'Save' }}
        </button>
      </div>
    </form>
  </AppDialog>
</template>
