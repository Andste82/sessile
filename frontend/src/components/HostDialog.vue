<script setup lang="ts">
import { ref, watch, computed } from 'vue'
import {
  Dialog,
  DialogPanel,
  DialogTitle,
  TransitionRoot,
  TransitionChild,
} from '@headlessui/vue'
import { useHostsStore } from '@/stores/hosts'
import type { AuthMethod, Host, HostBody, TargetOS } from '@/api/types'

const props = defineProps<{ open: boolean; host: Host | null }>()
const emit = defineEmits<{
  (e: 'close'): void
  (e: 'saved', host: Host): void
}>()

const store = useHostsStore()
const isEdit = computed(() => props.host !== null)

const name = ref('')
const group = ref('')
const address = ref('')
const username = ref('')
const authMethod = ref<AuthMethod>('password')
const password = ref('')
const privateKey = ref('')
const privateKeyPassphrase = ref('')
const targetOS = ref<TargetOS | ''>('')
const terminalType = ref('bash')
const customCommand = ref('')
const submitting = ref(false)
const error = ref<string | null>(null)

const terminalTypes = ['bash', 'zsh', 'fish', 'cmd', 'powershell', 'custom']
const targetOSes: { value: TargetOS | ''; label: string }[] = [
  { value: '', label: 'Unspecified' },
  { value: 'linux', label: 'Linux' },
  { value: 'darwin', label: 'macOS' },
  { value: 'windows', label: 'Windows' },
  { value: 'other', label: 'Other' },
]

// Reset (create mode) or prefill (edit mode) whenever the dialog opens.
// Secrets are never prefilled — the API never sends them back — the
// password/private-key fields start blank with a "leave blank to keep
// current" hint when one is already stored.
watch(
  () => props.open,
  (isOpen) => {
    if (!isOpen) return
    error.value = null
    password.value = ''
    privateKey.value = ''
    privateKeyPassphrase.value = ''
    const h = props.host
    name.value = h?.name ?? ''
    group.value = h?.group ?? ''
    address.value = h?.address ?? ''
    username.value = h?.username ?? ''
    authMethod.value = h?.authMethod ?? 'password'
    targetOS.value = h?.targetOS ?? ''
    terminalType.value = h?.terminalType || 'bash'
    customCommand.value = h?.customCommand ?? ''
  },
)

const canSubmit = computed(
  () => name.value.trim().length > 0 && address.value.trim().length > 0 && username.value.trim().length > 0,
)

async function submit() {
  if (!canSubmit.value || submitting.value) return
  submitting.value = true
  error.value = null
  try {
    const body: HostBody = {
      name: name.value.trim(),
      group: group.value.trim(),
      address: address.value.trim(),
      username: username.value.trim(),
      authMethod: authMethod.value,
      targetOS: targetOS.value,
      terminalType: terminalType.value,
      customCommand: terminalType.value === 'custom' ? customCommand.value.trim() : '',
    }
    // Omit a blank secret field entirely so an edit that isn't changing the
    // credential doesn't overwrite it with empty — HostBody's fields are
    // optional exactly so this works.
    if (authMethod.value === 'password' && password.value) body.password = password.value
    if (authMethod.value === 'privateKey' && privateKey.value) body.privateKey = privateKey.value
    if (authMethod.value === 'privateKey' && privateKeyPassphrase.value)
      body.privateKeyPassphrase = privateKeyPassphrase.value

    const saved = props.host
      ? await store.updateHost(props.host.id, body)
      : await store.createHost(body)
    emit('saved', saved)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    submitting.value = false
  }
}

const labelCls = 'flex flex-col gap-1 text-sm'
const inputCls =
  'rounded-md border border-slate-600 bg-slate-900 px-3 py-2 text-slate-100 outline-none focus:border-emerald-500'
</script>

<template>
  <TransitionRoot :show="open" as="template">
    <Dialog class="relative z-50" @close="emit('close')">
      <TransitionChild
        as="template"
        enter="duration-150 ease-out"
        enter-from="opacity-0"
        enter-to="opacity-100"
        leave="duration-100 ease-in"
        leave-from="opacity-100"
        leave-to="opacity-0"
      >
        <div class="fixed inset-0 bg-black/60" aria-hidden="true" />
      </TransitionChild>

      <div class="fixed inset-0 flex items-center justify-center overflow-y-auto p-4">
        <TransitionChild
          as="template"
          enter="duration-150 ease-out"
          enter-from="opacity-0 scale-95"
          enter-to="opacity-100 scale-100"
          leave="duration-100 ease-in"
          leave-from="opacity-100 scale-100"
          leave-to="opacity-0 scale-95"
        >
          <DialogPanel
            class="w-full max-w-md rounded-xl border border-slate-700 bg-slate-800 p-6 shadow-xl"
          >
            <DialogTitle class="text-lg font-semibold text-slate-100">
              {{ isEdit ? 'Edit host' : 'New host' }}
            </DialogTitle>

            <form class="mt-5 flex flex-col gap-4" @submit.prevent="submit">
              <div class="grid grid-cols-2 gap-3">
                <label :class="labelCls">
                  <span class="text-slate-400">Name</span>
                  <input v-model="name" type="text" maxlength="64" autofocus placeholder="prod-db" :class="inputCls" />
                </label>
                <label :class="labelCls">
                  <span class="text-slate-400">Group</span>
                  <input v-model="group" type="text" maxlength="64" placeholder="Production" :class="inputCls" />
                </label>
              </div>

              <label :class="labelCls">
                <span class="text-slate-400">Address</span>
                <input v-model="address" type="text" placeholder="db.example.com:22" :class="inputCls" />
              </label>

              <label :class="labelCls">
                <span class="text-slate-400">Username</span>
                <input v-model="username" type="text" placeholder="deploy" :class="inputCls" />
              </label>

              <div class="flex flex-col gap-2">
                <span class="text-sm text-slate-400">Authentication</span>
                <div class="flex gap-4 text-sm text-slate-200">
                  <label class="flex cursor-pointer items-center gap-2">
                    <input v-model="authMethod" type="radio" value="password" class="accent-emerald-400" />
                    Password
                  </label>
                  <label class="flex cursor-pointer items-center gap-2">
                    <input v-model="authMethod" type="radio" value="privateKey" class="accent-emerald-400" />
                    Private key
                  </label>
                </div>
              </div>

              <label v-if="authMethod === 'password'" :class="labelCls">
                <span class="text-slate-400">Password</span>
                <input
                  v-model="password"
                  type="password"
                  autocomplete="new-password"
                  :placeholder="isEdit && host?.hasPassword ? 'Leave blank to keep the current password' : ''"
                  :class="inputCls"
                />
              </label>

              <template v-else>
                <label :class="labelCls">
                  <span class="text-slate-400">Private key</span>
                  <textarea
                    v-model="privateKey"
                    rows="4"
                    :placeholder="isEdit && host?.hasPrivateKey ? 'Leave blank to keep the current key' : '-----BEGIN OPENSSH PRIVATE KEY-----'"
                    class="rounded-md border border-slate-600 bg-slate-900 px-3 py-2 font-mono text-xs text-slate-100 outline-none focus:border-emerald-500"
                  />
                </label>
                <label :class="labelCls">
                  <span class="text-slate-400">Passphrase (optional)</span>
                  <input v-model="privateKeyPassphrase" type="password" autocomplete="new-password" :class="inputCls" />
                </label>
              </template>

              <div class="grid grid-cols-2 gap-3">
                <label :class="labelCls">
                  <span class="text-slate-400">Target OS</span>
                  <select v-model="targetOS" :class="inputCls">
                    <option v-for="o in targetOSes" :key="o.value" :value="o.value">{{ o.label }}</option>
                  </select>
                </label>
                <label :class="labelCls">
                  <span class="text-slate-400">Terminal</span>
                  <select v-model="terminalType" :class="inputCls">
                    <option v-for="t in terminalTypes" :key="t" :value="t">{{ t }}</option>
                  </select>
                </label>
              </div>

              <label v-if="terminalType === 'custom'" :class="labelCls">
                <span class="text-slate-400">Command</span>
                <input v-model="customCommand" type="text" placeholder="tmux new -A -s main" :class="inputCls" />
              </label>

              <p v-if="error" class="text-sm text-rose-400">{{ error }}</p>

              <div class="mt-2 flex justify-end gap-3">
                <button
                  type="button"
                  class="rounded-md px-4 py-2 text-sm text-slate-300 hover:bg-slate-700"
                  @click="emit('close')"
                >
                  Cancel
                </button>
                <button
                  type="submit"
                  :disabled="!canSubmit || submitting"
                  class="rounded-md bg-emerald-600 px-4 py-2 text-sm font-medium text-white hover:bg-emerald-500 disabled:cursor-not-allowed disabled:opacity-50"
                >
                  {{ submitting ? 'Saving…' : isEdit ? 'Save' : 'Create' }}
                </button>
              </div>
            </form>
          </DialogPanel>
        </TransitionChild>
      </div>
    </Dialog>
  </TransitionRoot>
</template>
