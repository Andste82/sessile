<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRouter, RouterLink } from 'vue-router'
import { api, ApiRequestError } from '@/api/client'
import { useAgentStore } from '@/stores/agent'
import { useHostsStore } from '@/stores/hosts'
import { useSessionsStore } from '@/stores/sessions'
import { useTasksStore } from '@/stores/tasks'
import AppDialog from './AppDialog.vue'
import ModelPicker from './ModelPicker.vue'
import HostKeyTrustDialog from './HostKeyTrustDialog.vue'
import type { HostKeyErrorDetails, TaskSpec } from '@/api/types'

// The task form (§4.12.1): it sets a task up — host, main repo,
// devcontainer, agent — and optionally carries the first message. What to do
// is otherwise said to the agent in the terminal.
const props = defineProps<{ open: boolean }>()
const emit = defineEmits<{ (e: 'close'): void }>()

const router = useRouter()
const agent = useAgentStore()
const hostsStore = useHostsStore()
const sessions = useSessionsStore()
const tasks = useTasksStore()

const localValue = '__local__'
const name = ref('')
const epic = ref('')
const request = ref('')
const host = ref('')
const repoUrl = ref('')
const repoRef = ref('')
const useDevcontainer = ref(false)
const dcMode = ref<'auto' | 'repo' | 'generic'>('auto')
const dockerSocket = ref(false)
const profileId = ref('')
const mode = ref<'plan' | 'normal'>('plan')
const model = ref('')
const submitting = ref(false)
const error = ref<string | null>(null)
const pendingHostKey = ref<{ hostId: string; hostName: string; details: HostKeyErrorDetails } | null>(null)

const allowLocalHost = computed(() => sessions.config?.allowLocalHost ?? false)
const profiles = computed(() => agent.settings?.profiles ?? [])
const profile = computed(() => profiles.value.find((p) => p.id === profileId.value))
const connection = computed(() => (profile.value ? agent.connection(profile.value.connectionId) : undefined))
const hasRepo = computed(() => repoUrl.value.trim() !== '')
// Epics the user already has (§4.18.3), so related tasks land in one group
// rather than in three spellings of the same name.
const epics = computed(() =>
  [...new Set(Object.values(tasks.tasks).map((t) => t.spec.epic ?? '').filter(Boolean))].sort(),
)

watch(
  () => props.open,
  async (isOpen) => {
    if (!isOpen) return
    error.value = null
    pendingHostKey.value = null
    name.value = ''
    epic.value = ''
    request.value = ''
    repoUrl.value = ''
    repoRef.value = ''
    useDevcontainer.value = false
    dcMode.value = 'auto'
    dockerSocket.value = false
    mode.value = 'plan'
    if (hostsStore.hosts.length === 0) void hostsStore.fetchHosts()
    if (!sessions.config) void sessions.fetchConfig()
    await agent.load()
    const d = agent.settings?.taskDefaults
    host.value = d?.hostId === 'local' ? localValue : (d?.hostId ?? '')
    profileId.value = d?.profileId || profiles.value[0]?.id || ''
  },
)

watch(profileId, () => {
  model.value = profile.value?.model ?? ''
})
watch(hasRepo, (has) => {
  if (!has) useDevcontainer.value = false
})

// Which Git account the clone will use (§4.16), in words.
const gitLine = computed(() => {
  const accounts = agent.settings?.git ?? []
  if (!hasRepo.value) {
    if (accounts.length === 0) return "No main repo; the agent can clone with the host's own git setup."
    return `No main repo; the agent can clone with your Git accounts (${accounts.map((a) => a.host).join(', ')}).`
  }
  let u: URL | null = null
  try {
    u = new URL(repoUrl.value.trim())
  } catch {
    u = null
  }
  if (!u || u.protocol !== 'https:') return "Git: the host's own SSH keys (not an https URL)."
  const acct = accounts.find((a) => a.host.toLowerCase() === u!.host.toLowerCase())
  return acct ? `Git: ${acct.host} as ${acct.username} (sessile Git account).` : "Git: the host's own credentials."
})

const canSubmit = computed(
  () => name.value.trim() !== '' && host.value !== '' && profileId.value !== '' && !connection.value?.expired,
)

function spec(): TaskSpec {
  const s: TaskSpec = {
    name: name.value.trim(),
    epic: epic.value.trim() || undefined,
    agent: { profileId: profileId.value, model: model.value.trim() || undefined, mode: mode.value },
  }
  if (host.value === localValue) s.target = 'local'
  else s.hostId = host.value
  if (hasRepo.value) {
    s.repo = { url: repoUrl.value.trim(), ref: repoRef.value.trim() || undefined }
    if (useDevcontainer.value) s.devcontainer = { mode: dcMode.value, dockerSocket: dockerSocket.value }
  }
  if (request.value.trim()) s.request = request.value.trim()
  return s
}

async function create() {
  if (!canSubmit.value || submitting.value) return
  submitting.value = true
  error.value = null
  const body = spec()
  try {
    const session = await api.createTask(body)
    sessions.upsertSession(session)
    sessions.openTab(session.id)
    emit('close')
    void router.push(`/sessions/${session.id}`)
  } catch (e) {
    const details = e instanceof ApiRequestError ? e.hostKeyDetails() : null
    if (details && body.hostId) {
      pendingHostKey.value = {
        hostId: body.hostId,
        hostName: hostsStore.hosts.find((h) => h.id === body.hostId)?.name ?? 'this host',
        details,
      }
    } else {
      error.value = e instanceof Error ? e.message : String(e)
    }
  } finally {
    submitting.value = false
  }
}

function retryAfterTrust() {
  pendingHostKey.value = null
  void create()
}

const labelCls = 'flex flex-col gap-1 text-sm'
const inputCls =
  'rounded-md border border-slate-600 bg-slate-900 px-3 py-2 text-slate-100 outline-none focus:border-emerald-500'
</script>

<template>
  <AppDialog :open="open" title="New task" wide @close="emit('close')">
    <form class="flex flex-col gap-4" @submit.prevent="create">
      <div class="grid gap-3 sm:grid-cols-[2fr_1fr]">
        <label :class="labelCls">
          <span class="text-slate-400">Name</span>
          <input v-model="name" type="text" maxlength="64" autofocus placeholder="DBG-142 print crash" :class="inputCls" />
        </label>
        <label :class="labelCls">
          <span class="text-slate-400">Epic <span class="text-slate-500">(optional)</span></span>
          <input
            v-model="epic"
            type="text"
            maxlength="64"
            list="task-epics"
            placeholder="Tasks"
            :class="inputCls"
          />
          <datalist id="task-epics">
            <option v-for="e in epics" :key="e" :value="e" />
          </datalist>
        </label>
      </div>

      <label :class="labelCls">
        <span class="text-slate-400">Request <span class="text-slate-500">(optional)</span></span>
        <textarea
          v-model="request"
          rows="3"
          placeholder="Fix DBG-142, the print crash on Android 14. Leave empty to tell the agent in the terminal."
          :class="inputCls"
        />
      </label>

      <div class="grid gap-3 sm:grid-cols-2">
        <label :class="labelCls">
          <span class="text-slate-400">Host</span>
          <select v-model="host" :class="inputCls">
            <option value="" disabled>Pick a host</option>
            <option v-if="allowLocalHost" :value="localValue">This server</option>
            <option v-for="h in hostsStore.hosts" :key="h.id" :value="h.id">{{ h.name }}</option>
          </select>
        </label>
        <label :class="labelCls">
          <span class="text-slate-400">Agent profile</span>
          <select v-model="profileId" :class="inputCls">
            <option v-for="p in profiles" :key="p.id" :value="p.id">{{ p.name }}</option>
          </select>
        </label>
      </div>
      <p v-if="profiles.length === 0 && agent.settings" class="text-xs text-amber-400">
        No agent profiles yet —
        <RouterLink to="/agent/settings" class="underline" @click="emit('close')">add a connection and a profile</RouterLink>
        first.
      </p>
      <p v-if="connection?.expired" class="text-xs text-rose-400">
        This profile's connection has expired.
        <RouterLink to="/agent/settings" class="underline" @click="emit('close')">Renew it</RouterLink>.
      </p>
      <p v-else-if="connection?.expiresSoon" class="text-xs text-amber-400">This profile's connection expires soon.</p>

      <div class="grid gap-3 sm:grid-cols-[1fr_8rem]">
        <label :class="labelCls">
          <span class="text-slate-400">Main repo <span class="text-slate-500">(optional)</span></span>
          <input v-model="repoUrl" type="text" placeholder="https://github.com/owner/repo" :class="inputCls" />
        </label>
        <label :class="labelCls">
          <span class="text-slate-400">Branch</span>
          <input v-model="repoRef" type="text" placeholder="default" :disabled="!hasRepo" :class="inputCls" />
        </label>
      </div>
      <p class="text-xs text-slate-500">{{ gitLine }}</p>

      <div class="flex flex-col gap-2 rounded-md border border-slate-700 p-3">
        <label class="flex items-center gap-2 text-sm text-slate-200" :class="!hasRepo ? 'opacity-50' : ''">
          <input v-model="useDevcontainer" type="checkbox" class="accent-emerald-400" :disabled="!hasRepo" />
          Run in the repo's devcontainer
        </label>
        <p v-if="!hasRepo" class="text-xs text-slate-500">A devcontainer needs the main repo: its config lives there.</p>
        <template v-if="useDevcontainer">
          <label :class="labelCls">
            <span class="text-slate-400">Config</span>
            <select v-model="dcMode" :class="inputCls">
              <option value="auto">The repo's, or sessile's generic one if it has none</option>
              <option value="repo">The repo's only</option>
              <option value="generic">sessile's generic one</option>
            </select>
          </label>
          <label class="flex items-center gap-2 text-sm text-slate-200">
            <input v-model="dockerSocket" type="checkbox" class="accent-emerald-400" />
            Give the container the host's Docker socket
          </label>
          <p v-if="dockerSocket" class="text-xs text-amber-400">
            The Docker socket is root on the host for anything in the container. Only for repos whose dev workflow runs
            docker itself.
          </p>
        </template>
      </div>

      <div class="grid gap-3 sm:grid-cols-2">
        <div :class="labelCls">
          <span class="text-slate-400">Mode</span>
          <div class="flex gap-4 text-sm text-slate-200">
            <label class="flex items-center gap-2"><input v-model="mode" type="radio" value="plan" class="accent-emerald-400" /> Plan first</label>
            <label class="flex items-center gap-2"><input v-model="mode" type="radio" value="normal" class="accent-emerald-400" /> Normal</label>
          </div>
        </div>
        <div v-if="profile" :class="labelCls">
          <span class="text-slate-400">Model</span>
          <ModelPicker v-model="model" :connection-id="profile.connectionId" />
        </div>
      </div>

      <p v-if="error" class="text-sm text-rose-400">{{ error }}</p>

      <div class="mt-2 flex justify-end gap-3">
        <button type="button" class="rounded-md px-4 py-2 text-sm text-slate-300 hover:bg-slate-700" @click="emit('close')">
          Cancel
        </button>
        <button
          type="submit"
          :disabled="!canSubmit || submitting"
          class="rounded-md bg-emerald-600 px-4 py-2 text-sm font-medium text-white hover:bg-emerald-500 disabled:cursor-not-allowed disabled:opacity-50"
        >
          {{ submitting ? 'Starting…' : 'Start task' }}
        </button>
      </div>
    </form>
  </AppDialog>

  <HostKeyTrustDialog
    :open="pendingHostKey !== null"
    :host-id="pendingHostKey?.hostId ?? ''"
    :host-name="pendingHostKey?.hostName ?? ''"
    :details="pendingHostKey?.details ?? null"
    @close="pendingHostKey = null"
    @trusted="retryAfterTrust"
  />
</template>
