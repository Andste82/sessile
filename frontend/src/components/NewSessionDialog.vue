<script setup lang="ts">
import { ref, watch, computed } from 'vue'
import {
  Dialog,
  DialogPanel,
  DialogTitle,
  TransitionRoot,
  TransitionChild,
} from '@headlessui/vue'
import { useSessionsStore } from '@/stores/sessions'
import { useHostsStore } from '@/stores/hosts'
import { ApiRequestError } from '@/api/client'
import DirectoryBrowser from './DirectoryBrowser.vue'
import GroupInput from './GroupInput.vue'
import SshDirectoryChooser from './SshDirectoryChooser.vue'
import { sshCreateBody, supportsStartDir } from '@/utils/hostBrowse'
import HostKeyTrustDialog from './HostKeyTrustDialog.vue'
import type { CreateSessionBody, HostKeyErrorDetails, Session } from '@/api/types'

const props = defineProps<{ open: boolean }>()
const emit = defineEmits<{
  (e: 'close'): void
  (e: 'created', session: Session): void
}>()

const store = useSessionsStore()
const hostsStore = useHostsStore()

// One picker for the session's target (§12b M18): every configured SSH host,
// plus "This host (local)" as an ordinary entry in the same list when the
// admin has turned allowLocalHost on — not a separate mode switch. localValue
// is the sentinel selection identifies that entry by; it can't collide with a
// real host id (those are UUIDs).
const localValue = '__local__'
const selection = ref('')
const target = computed<'ssh' | 'local'>(() => (selection.value === localValue ? 'local' : 'ssh'))
const hostId = computed(() => (target.value === 'ssh' ? selection.value : ''))

const name = ref('')
const group = ref('')
const directory = ref('.')
const shell = ref('')
const submitting = ref(false)
const error = ref<string | null>(null)

// Set only while a create attempt is blocked on an unrecognized/changed
// host key. Holding the pending request body here (rather than recomputing
// it) means "trust and retry" resends exactly what was refused.
// `purpose` says which action to re-run once the key is trusted: the blocked
// create, or the chooser's directory browse.
const pendingHostKey = ref<{
  hostId: string
  hostName: string
  details: HostKeyErrorDetails
  purpose: 'browse' | 'create'
} | null>(null)
const chooser = ref<InstanceType<typeof SshDirectoryChooser> | null>(null)
// Chosen start directory on the SSH host; "" = the sshd default.
const sshDirectory = ref('')
const selectedHost = computed(() => hosts.value.find((h) => h.id === hostId.value) ?? null)
const startDirSupported = computed(() => (selectedHost.value ? supportsStartDir(selectedHost.value) : true))
const pendingBody = ref<CreateSessionBody | null>(null)

const shells = computed(() => store.config?.shells ?? [])
const allowLocalHost = computed(() => store.config?.allowLocalHost ?? false)
const hosts = computed(() => hostsStore.hosts)

const canSubmit = computed(() => {
  if (name.value.trim().length === 0) return false
  if (target.value === 'ssh') return hostId.value !== ''
  return directory.value !== '' && shell.value !== ''
})

// Reset to defaults whenever the dialog opens; the browser starts at the root.
watch(
  () => props.open,
  (isOpen) => {
    if (!isOpen) {
      // Drop the browse connection now rather than waiting for unmount.
      chooser.value?.stop()
      return
    }
    sshDirectory.value = ''
    error.value = null
    pendingHostKey.value = null
    pendingBody.value = null
    name.value = ''
    // Deliberately not carried over from the last session created: a field
    // that quietly remembers is a field that files a session somewhere the
    // user did not look at.
    group.value = ''
    directory.value = '.'
    shell.value = shells.value[0] ?? ''
    selection.value = ''
    if (hostsStore.hosts.length === 0) void hostsStore.fetchHosts()
  },
)

function bodyFromForm(): CreateSessionBody {
  const common = { name: name.value.trim(), group: group.value.trim() }
  if (target.value === 'ssh') {
    return sshCreateBody(
      common,
      hostId.value,
      startDirSupported.value ? sshDirectory.value : '',
    )
  }
  return { ...common, target: 'local', directory: directory.value, shell: shell.value }
}

async function attemptCreate(body: CreateSessionBody) {
  submitting.value = true
  error.value = null
  try {
    const session = await store.createSession(body)
    pendingHostKey.value = null
    pendingBody.value = null
    emit('created', session)
  } catch (e) {
    const hostKeyDetails = e instanceof ApiRequestError ? e.hostKeyDetails() : null
    if (hostKeyDetails && body.target === 'ssh') {
      const host = hosts.value.find((h) => h.id === body.hostId)
      pendingBody.value = body
      pendingHostKey.value = {
        hostId: body.hostId,
        hostName: host?.name ?? 'this host',
        details: hostKeyDetails,
        purpose: 'create',
      }
    } else {
      error.value = e instanceof Error ? e.message : String(e)
    }
  } finally {
    submitting.value = false
  }
}

function submit() {
  if (!canSubmit.value || submitting.value) return
  void attemptCreate(bodyFromForm())
}

function retryAfterTrust() {
  const purpose = pendingHostKey.value?.purpose
  const body = pendingBody.value
  pendingHostKey.value = null
  if (purpose === 'browse') void chooser.value?.retry()
  else if (body) void attemptCreate(body)
}

function onBrowseHostKey(details: HostKeyErrorDetails) {
  pendingHostKey.value = {
    hostId: hostId.value,
    hostName: selectedHost.value?.name ?? 'this host',
    details,
    purpose: 'browse',
  }
}

// Dismissing the trust dialog leaves a browse attempt in its error state
// (with Retry); a blocked create just stays blocked, as before.
function closeHostKeyDialog() {
  const purpose = pendingHostKey.value?.purpose
  pendingHostKey.value = null
  if (purpose === 'browse') chooser.value?.dismissTrust()
}

// A host key dialog opened for browsing is moot once the host changes.
watch(selection, () => {
  if (pendingHostKey.value?.purpose === 'browse') pendingHostKey.value = null
  sshDirectory.value = ''
})
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

      <div class="fixed inset-0 flex items-center justify-center p-4">
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
            class="max-h-[calc(100dvh-2rem)] w-full max-w-md overflow-y-auto rounded-xl border border-slate-700 bg-slate-800 p-6 shadow-xl"
          >
            <DialogTitle class="text-lg font-semibold text-slate-100">
              New session
            </DialogTitle>

            <form class="mt-5 flex flex-col gap-4" @submit.prevent="submit">
              <label class="flex flex-col gap-1 text-sm">
                <span class="text-slate-400">Name</span>
                <input
                  v-model="name"
                  type="text"
                  maxlength="64"
                  autofocus
                  placeholder="Backend"
                  class="rounded-md border border-slate-600 bg-slate-900 px-3 py-2 text-slate-100 outline-none focus:border-emerald-500"
                />
              </label>

              <label class="flex flex-col gap-1 text-sm">
                <span class="text-slate-400">Group <span class="text-slate-500">(optional)</span></span>
                <GroupInput v-model="group" />
              </label>

              <label class="flex flex-col gap-1 text-sm">
                <span class="text-slate-400">Host</span>
                <select
                  v-model="selection"
                  class="rounded-md border border-slate-600 bg-slate-900 px-3 py-2 text-slate-100 outline-none focus:border-emerald-500"
                >
                  <option value="" disabled>
                    {{ hosts.length || allowLocalHost ? 'Select a host…' : 'No hosts configured yet' }}
                  </option>
                  <option v-if="allowLocalHost" :value="localValue">This host (local)</option>
                  <option v-for="h in hosts" :key="h.id" :value="h.id">
                    {{ h.group ? `${h.group} / ${h.name}` : h.name }}
                  </option>
                </select>
              </label>
              <p v-if="hosts.length === 0 && !allowLocalHost" class="text-xs text-slate-400">
                Add a host on the Hosts page before starting a session.
              </p>

              <template v-if="target === 'ssh' && hostId">
                <SshDirectoryChooser
                  v-if="startDirSupported"
                  ref="chooser"
                  v-model="sshDirectory"
                  :host-id="hostId"
                  :host-name="selectedHost?.name ?? 'host'"
                  @host-key="onBrowseHostKey"
                />
                <p v-else class="text-xs text-slate-400">
                  A start directory is not supported for Windows hosts; the session starts in the
                  host's default directory.
                </p>
              </template>

              <template v-if="target === 'local'">
                <label class="flex flex-col gap-1 text-sm">
                  <span class="text-slate-400">Directory</span>
                  <DirectoryBrowser v-model="directory" />
                </label>

                <label class="flex flex-col gap-1 text-sm">
                  <span class="text-slate-400">Shell</span>
                  <select
                    v-model="shell"
                    class="rounded-md border border-slate-600 bg-slate-900 px-3 py-2 text-slate-100 outline-none focus:border-emerald-500"
                  >
                    <option v-for="s in shells" :key="s" :value="s">{{ s }}</option>
                  </select>
                </label>
              </template>

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
                  {{ submitting ? 'Creating…' : 'Create' }}
                </button>
              </div>
            </form>
          </DialogPanel>
        </TransitionChild>
      </div>
    </Dialog>
  </TransitionRoot>

  <HostKeyTrustDialog
    :open="pendingHostKey !== null"
    :host-id="pendingHostKey?.hostId ?? ''"
    :host-name="pendingHostKey?.hostName ?? ''"
    :details="pendingHostKey?.details ?? null"
    @close="closeHostKeyDialog"
    @trusted="retryAfterTrust"
  />
</template>
