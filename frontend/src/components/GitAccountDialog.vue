<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { api } from '@/api/client'
import { useAgentStore } from '@/stores/agent'
import { uuidv4 } from '@/utils/uuid'
import AppDialog from './AppDialog.vue'
import PasswordInput from './PasswordInput.vue'
import type { GitAccount, TestResult } from '@/api/types'

// One git identity per git host (§4.16): used for clones, commits and pushes
// in tasks, through environment-only git config — never written to disk on
// the target.
const props = defineProps<{ open: boolean; account: GitAccount | null }>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'saved'): void }>()

const store = useAgentStore()
const host = ref('github.com')
const name = ref('')
const email = ref('')
const username = ref('')
const token = ref('')
const saving = ref(false)
const error = ref<string | null>(null)
const testing = ref(false)
const test = ref<TestResult | null>(null)

const isEdit = computed(() => props.account !== null)

watch(
  () => props.open,
  (open) => {
    if (!open) return
    const a = props.account
    host.value = a?.host ?? 'github.com'
    name.value = a?.name ?? ''
    email.value = a?.email ?? ''
    username.value = a?.username ?? ''
    token.value = ''
    error.value = null
    test.value = null
  },
)

const canSave = computed(
  () => host.value.trim() && username.value.trim() && (token.value.trim() || props.account?.hasToken),
)

async function runTest() {
  testing.value = true
  test.value = null
  try {
    test.value = await api.testGitAccount({
      id: props.account?.id,
      host: host.value.trim(),
      token: token.value.trim() || undefined,
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
    await store.saveGit({
      id: props.account?.id ?? uuidv4(),
      host: host.value.trim(),
      name: name.value.trim(),
      email: email.value.trim(),
      username: username.value.trim(),
      token: token.value.trim() || undefined,
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
  <AppDialog :open="open" :title="isEdit ? 'Edit Git account' : 'Add Git account'" @close="emit('close')">
    <form class="flex flex-col gap-4" @submit.prevent="submit">
      <label :class="labelCls">
        <span class="text-slate-400">Git host</span>
        <input v-model="host" type="text" placeholder="github.com" :class="inputCls" />
      </label>
      <div class="grid grid-cols-2 gap-3">
        <label :class="labelCls">
          <span class="text-slate-400">Name (commits)</span>
          <input v-model="name" type="text" :class="inputCls" />
        </label>
        <label :class="labelCls">
          <span class="text-slate-400">E-mail (commits)</span>
          <input v-model="email" type="email" :class="inputCls" />
        </label>
      </div>
      <label :class="labelCls">
        <span class="text-slate-400">Username</span>
        <input v-model="username" type="text" :class="inputCls" />
      </label>
      <label :class="labelCls">
        <span class="text-slate-400">Token</span>
        <PasswordInput
          v-model="token"
          autocomplete="off"
          :placeholder="account?.hasToken ? 'Leave blank to keep the saved token' : ''"
        />
        <span class="text-xs text-slate-500">
          A personal access token that can read and push the repos you work on (GitHub: a fine-grained or classic
          token with repo access). It is only ever handed to git as environment, never written into a repo or config.
        </span>
      </label>
      <div class="flex flex-col gap-1">
        <button
          type="button"
          class="self-start rounded-md border border-slate-600 px-3 py-1.5 text-xs text-slate-200 hover:bg-slate-700 disabled:opacity-50"
          :disabled="testing || !host.trim()"
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
