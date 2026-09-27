<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { PlusIcon, PencilIcon } from '@heroicons/vue/24/outline'
import { useAgentStore } from '@/stores/agent'
import { useHostsStore } from '@/stores/hosts'
import { api } from '@/api/client'
import AgentNav from '@/components/AgentNav.vue'
import ConnectionDialog from '@/components/ConnectionDialog.vue'
import ProfileDialog from '@/components/ProfileDialog.vue'
import GitAccountDialog from '@/components/GitAccountDialog.vue'
import type { Connection, GitAccount, Profile } from '@/api/types'

// Connections, profiles, task defaults and Git accounts (§4.13, §4.16).
const store = useAgentStore()
const hosts = useHostsStore()
const allowLocalHost = ref(false)

const connDialog = ref(false)
const editingConn = ref<Connection | null>(null)
const profileDialog = ref(false)
const editingProfile = ref<Profile | null>(null)
const gitDialog = ref(false)
const editingGit = ref<GitAccount | null>(null)

const armed = ref<string | null>(null)
const actionError = ref<string | null>(null)

onMounted(async () => {
  void hosts.fetchHosts()
  void store.load()
  try {
    allowLocalHost.value = (await api.config()).allowLocalHost
  } catch {
    // The host list still works without this; only "This server" is missing.
  }
})

const s = computed(() => store.settings)

function expiryLabel(c: Connection) {
  if (!c.expires) return ''
  const d = c.expires.slice(0, 10)
  if (c.expired) return `expired ${d}`
  if (c.expiresSoon) return `expires soon (${d})`
  return `expires ${d}`
}

function connectionName(id: string) {
  const c = store.connection(id)
  return c ? c.name : 'missing connection'
}

async function run(fn: () => Promise<void>) {
  actionError.value = null
  armed.value = null
  try {
    await fn()
  } catch (e) {
    actionError.value = e instanceof Error ? e.message : String(e)
  }
}

const defaultHost = computed({
  get: () => s.value?.taskDefaults.hostId ?? '',
  set: (hostId: string) =>
    void run(() => store.saveTaskDefaults({ hostId, profileId: s.value?.taskDefaults.profileId ?? '' })),
})
const defaultProfile = computed({
  get: () => s.value?.taskDefaults.profileId ?? '',
  set: (profileId: string) =>
    void run(() => store.saveTaskDefaults({ hostId: s.value?.taskDefaults.hostId ?? '', profileId })),
})

const sectionCls = 'mb-4 rounded-lg border border-slate-700 bg-slate-800/50 p-6'
const rowCls = 'flex flex-col gap-2 py-3 first:pt-0 last:pb-0 sm:flex-row sm:items-center sm:justify-between'
const btnCls = 'flex items-center gap-1 rounded-md border border-slate-600 px-3 py-1.5 text-xs text-slate-300 hover:bg-slate-700'
const delCls = 'rounded-md border border-slate-600 px-3 py-1.5 text-xs text-slate-300 hover:border-rose-500 hover:text-rose-400'
const confirmCls = 'rounded-md bg-rose-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-rose-500'
const inputCls =
  'rounded-md border border-slate-600 bg-slate-900 px-3 py-2 text-sm text-slate-100 outline-none focus:border-emerald-500'
</script>

<template>
  <div class="flex h-full flex-col">
    <header class="border-b border-slate-800 bg-slate-900 px-4 py-4 sm:px-6">
      <h1 class="text-lg font-semibold tracking-tight">Agent</h1>
    </header>
    <AgentNav />

    <main class="mx-auto w-full max-w-2xl flex-1 overflow-y-auto p-4 sm:p-6">
      <p v-if="store.error" class="mb-4 text-sm text-rose-400">{{ store.error }}</p>
      <p v-if="actionError" class="mb-4 text-sm text-rose-400">{{ actionError }}</p>

      <template v-if="s">
        <!-- Connections -->
        <section :class="sectionCls">
          <div class="mb-4 flex items-center justify-between">
            <h2 class="text-sm font-medium uppercase tracking-wide text-slate-400">Connections</h2>
            <button type="button" :class="btnCls" @click="editingConn = null; connDialog = true">
              <PlusIcon class="h-3.5 w-3.5" /> Add connection
            </button>
          </div>
          <p class="mb-3 text-xs text-slate-500">
            How your coding agents log in: a token or API key per agent and account. Tasks hand it to the agent as
            environment; sessile never uses it to talk to a model itself.
          </p>
          <div class="flex flex-col divide-y divide-slate-700/60">
            <div v-for="c in s.connections" :key="c.id" :class="rowCls">
              <div class="min-w-0">
                <p class="truncate text-sm text-slate-100">{{ c.name }}</p>
                <p class="truncate text-xs text-slate-500">
                  {{ c.agent }} · {{ store.kind(c.kind)?.label ?? c.kind }}
                  <span v-if="expiryLabel(c)" :class="c.expired ? 'text-rose-400' : c.expiresSoon ? 'text-amber-400' : ''">
                    · {{ expiryLabel(c) }}
                  </span>
                </p>
              </div>
              <div class="flex shrink-0 items-center gap-2">
                <button type="button" :class="btnCls" @click="editingConn = c; connDialog = true">
                  <PencilIcon class="h-3.5 w-3.5" /> Edit
                </button>
                <button v-if="armed !== c.id" type="button" :class="delCls" @click="armed = c.id">Delete</button>
                <button v-else type="button" :class="confirmCls" @click="run(() => store.deleteConnection(c.id))">
                  Delete, with its profiles?
                </button>
              </div>
            </div>
            <p v-if="s.connections.length === 0" class="text-sm text-slate-500">No connections yet.</p>
          </div>
        </section>

        <!-- Profiles -->
        <section :class="sectionCls">
          <div class="mb-4 flex items-center justify-between">
            <h2 class="text-sm font-medium uppercase tracking-wide text-slate-400">Profiles</h2>
            <button
              type="button"
              :class="btnCls"
              :disabled="s.connections.length === 0"
              @click="editingProfile = null; profileDialog = true"
            >
              <PlusIcon class="h-3.5 w-3.5" /> Add profile
            </button>
          </div>
          <p class="mb-3 text-xs text-slate-500">What a task picks: an agent with one of its connections and a model.</p>
          <div class="flex flex-col divide-y divide-slate-700/60">
            <div v-for="p in s.profiles" :key="p.id" :class="rowCls">
              <div class="min-w-0">
                <p class="truncate text-sm text-slate-100">{{ p.name }}</p>
                <p class="truncate text-xs text-slate-500">
                  {{ p.agent }} · {{ connectionName(p.connectionId) }} · {{ p.model || 'default model' }}
                </p>
              </div>
              <div class="flex shrink-0 items-center gap-2">
                <button type="button" :class="btnCls" @click="editingProfile = p; profileDialog = true">
                  <PencilIcon class="h-3.5 w-3.5" /> Edit
                </button>
                <button v-if="armed !== p.id" type="button" :class="delCls" @click="armed = p.id">Delete</button>
                <button v-else type="button" :class="confirmCls" @click="run(() => store.deleteProfile(p.id))">
                  Confirm delete?
                </button>
              </div>
            </div>
            <p v-if="s.profiles.length === 0" class="text-sm text-slate-500">
              {{ s.connections.length ? 'No profiles yet.' : 'Add a connection first.' }}
            </p>
          </div>
        </section>

        <!-- Task defaults -->
        <section :class="sectionCls">
          <h2 class="mb-4 text-sm font-medium uppercase tracking-wide text-slate-400">Task defaults</h2>
          <div class="grid gap-3 sm:grid-cols-2">
            <label class="flex flex-col gap-1 text-sm">
              <span class="text-slate-400">Host</span>
              <select v-model="defaultHost" :class="inputCls">
                <option value="">None</option>
                <option v-if="allowLocalHost" value="local">This server</option>
                <option v-for="h in hosts.hosts" :key="h.id" :value="h.id">{{ h.name }}</option>
              </select>
            </label>
            <label class="flex flex-col gap-1 text-sm">
              <span class="text-slate-400">Profile</span>
              <select v-model="defaultProfile" :class="inputCls">
                <option value="">None</option>
                <option v-for="p in s.profiles" :key="p.id" :value="p.id">{{ p.name }}</option>
              </select>
            </label>
          </div>
        </section>

        <!-- Git accounts -->
        <section :class="sectionCls">
          <div class="mb-4 flex items-center justify-between">
            <h2 class="text-sm font-medium uppercase tracking-wide text-slate-400">Git accounts</h2>
            <button type="button" :class="btnCls" @click="editingGit = null; gitDialog = true">
              <PlusIcon class="h-3.5 w-3.5" /> Add Git account
            </button>
          </div>
          <p class="mb-3 text-xs text-slate-500">
            Used by tasks to clone, commit and push over https. Without one, git uses whatever the host has set up.
          </p>
          <div class="flex flex-col divide-y divide-slate-700/60">
            <div v-for="g in s.git" :key="g.id" :class="rowCls">
              <div class="min-w-0">
                <p class="truncate text-sm text-slate-100">{{ g.host }}</p>
                <p class="truncate text-xs text-slate-500">
                  {{ g.username }}<span v-if="g.email"> · {{ g.name }} &lt;{{ g.email }}&gt;</span>
                </p>
              </div>
              <div class="flex shrink-0 items-center gap-2">
                <button type="button" :class="btnCls" @click="editingGit = g; gitDialog = true">
                  <PencilIcon class="h-3.5 w-3.5" /> Edit
                </button>
                <button v-if="armed !== g.id" type="button" :class="delCls" @click="armed = g.id">Delete</button>
                <button v-else type="button" :class="confirmCls" @click="run(() => store.deleteGit(g.id))">
                  Confirm delete?
                </button>
              </div>
            </div>
            <p v-if="s.git.length === 0" class="text-sm text-slate-500">No Git accounts yet.</p>
          </div>
        </section>
      </template>
    </main>

    <ConnectionDialog :open="connDialog" :connection="editingConn" @close="connDialog = false" @saved="connDialog = false" />
    <ProfileDialog :open="profileDialog" :profile="editingProfile" @close="profileDialog = false" @saved="profileDialog = false" />
    <GitAccountDialog :open="gitDialog" :account="editingGit" @close="gitDialog = false" @saved="gitDialog = false" />
  </div>
</template>
