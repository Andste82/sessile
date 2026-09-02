<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { PlusIcon, PencilIcon, ShieldCheckIcon } from '@heroicons/vue/24/outline'
import { useHostsStore } from '@/stores/hosts'
import HostDialog from '@/components/HostDialog.vue'
import HostKeyTrustDialog from '@/components/HostKeyTrustDialog.vue'
import type { Host, HostKeyErrorDetails } from '@/api/types'

const store = useHostsStore()

const dialogOpen = ref(false)
const editingHost = ref<Host | null>(null)
const armedDeleteId = ref<string | null>(null)
const deleteError = ref<Record<string, string>>({})

// "Verify host key" — hostkeys.go's probe/trust endpoints existed since
// M17, but nothing in the UI ever called probeHostKey; this was the only
// place a user could confirm or re-trust a host's key outside of stumbling
// into it while creating a session.
const verifyingId = ref<string | null>(null)
const verifyMessage = ref<Record<string, string>>({})
const pendingHostKey = ref<{ hostId: string; hostName: string; details: HostKeyErrorDetails } | null>(
  null,
)

onMounted(() => {
  void store.fetchHosts()
})

function openCreate() {
  editingHost.value = null
  dialogOpen.value = true
}

function openEdit(host: Host) {
  editingHost.value = host
  dialogOpen.value = true
}

function onSaved() {
  dialogOpen.value = false
}

function arm(id: string) {
  armedDeleteId.value = id
  delete deleteError.value[id]
}

async function confirmDelete(id: string) {
  armedDeleteId.value = null
  try {
    await store.deleteHost(id)
  } catch (e) {
    deleteError.value[id] = e instanceof Error ? e.message : String(e)
  }
}

function keyLabel(host: Host) {
  return host.trustedHostKeyFingerprint || 'Not yet trusted'
}

async function verifyHostKey(host: Host) {
  verifyingId.value = host.id
  const nextMessages = { ...verifyMessage.value }
  delete nextMessages[host.id]
  verifyMessage.value = nextMessages
  try {
    const probe = await store.probeHostKey(host.id)
    if (probe.status === 'unchanged') {
      verifyMessage.value = { ...verifyMessage.value, [host.id]: 'Host key unchanged — still trusted.' }
      return
    }
    // "new" (never trusted yet) or "changed" — either way, only an explicit
    // trust from here pins it. No status here silently updates the fingerprint.
    pendingHostKey.value = {
      hostId: host.id,
      hostName: host.name,
      details: {
        keyType: probe.keyType,
        fingerprint: probe.fingerprint,
        previousFingerprint: probe.previousFingerprint,
      },
    }
  } catch (e) {
    verifyMessage.value = { ...verifyMessage.value, [host.id]: e instanceof Error ? e.message : String(e) }
  } finally {
    verifyingId.value = null
  }
}
</script>

<template>
  <div class="flex h-full flex-col">
    <header
      class="flex items-center justify-between border-b border-slate-800 bg-slate-900 px-4 py-4 sm:px-6"
    >
      <h1 class="text-lg font-semibold tracking-tight">Hosts</h1>
      <button
        type="button"
        class="flex items-center gap-1.5 rounded-md bg-emerald-600 px-3 py-2 text-sm font-medium text-white hover:bg-emerald-500"
        @click="openCreate"
      >
        <PlusIcon class="h-4 w-4" />
        New host
      </button>
    </header>

    <main class="mx-auto w-full max-w-2xl flex-1 overflow-y-auto p-4 sm:p-6">
      <template v-if="Object.keys(store.grouped).length > 0">
        <section
          v-for="(hostsInGroup, group) in store.grouped"
          :key="group"
          class="mb-4 rounded-lg border border-slate-700 bg-slate-800/50 p-6"
        >
          <h2 class="mb-4 text-sm font-medium uppercase tracking-wide text-slate-400">
            {{ group }}
          </h2>

          <div class="flex flex-col divide-y divide-slate-700/60">
            <div
              v-for="host in hostsInGroup"
              :key="host.id"
              class="flex flex-col gap-2 py-3 first:pt-0 last:pb-0 sm:flex-row sm:items-center sm:justify-between"
            >
              <div class="min-w-0">
                <p class="truncate text-sm text-slate-100">{{ host.name }}</p>
                <p class="truncate text-xs text-slate-500">
                  {{ host.username }}@{{ host.address }}
                </p>
                <p class="mt-1 font-mono text-xs text-slate-600">
                  Host key: {{ keyLabel(host) }}
                </p>
                <p v-if="deleteError[host.id]" class="mt-1 text-xs text-rose-400">
                  {{ deleteError[host.id] }}
                </p>
                <p v-if="verifyMessage[host.id]" class="mt-1 text-xs text-slate-400">
                  {{ verifyMessage[host.id] }}
                </p>
              </div>

              <div class="flex shrink-0 items-center gap-2">
                <button
                  type="button"
                  class="flex items-center gap-1 rounded-md border border-slate-600 px-3 py-1.5 text-xs text-slate-300 hover:bg-slate-700 disabled:cursor-not-allowed disabled:opacity-50"
                  :disabled="verifyingId === host.id"
                  @click="verifyHostKey(host)"
                >
                  <ShieldCheckIcon class="h-3.5 w-3.5" />
                  {{ verifyingId === host.id ? 'Verifying…' : 'Verify host key' }}
                </button>
                <button
                  type="button"
                  class="flex items-center gap-1 rounded-md border border-slate-600 px-3 py-1.5 text-xs text-slate-300 hover:bg-slate-700"
                  @click="openEdit(host)"
                >
                  <PencilIcon class="h-3.5 w-3.5" />
                  Edit
                </button>

                <button
                  v-if="armedDeleteId !== host.id"
                  type="button"
                  class="rounded-md border border-slate-600 px-3 py-1.5 text-xs text-slate-300 hover:border-rose-500 hover:text-rose-400"
                  @click="arm(host.id)"
                >
                  Delete
                </button>
                <template v-else>
                  <button
                    type="button"
                    class="rounded-md bg-rose-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-rose-500"
                    @click="confirmDelete(host.id)"
                  >
                    Confirm delete?
                  </button>
                  <button
                    type="button"
                    class="rounded-md px-3 py-1.5 text-xs text-slate-400 hover:bg-slate-700"
                    @click="armedDeleteId = null"
                  >
                    Cancel
                  </button>
                </template>
              </div>
            </div>
          </div>
        </section>
      </template>
      <div
        v-else-if="!store.loading"
        class="flex flex-col items-center justify-center gap-3 rounded-lg border border-slate-700 bg-slate-800/50 p-10 text-center"
      >
        <p class="text-sm text-slate-400">No hosts yet.</p>
        <button
          type="button"
          class="flex items-center gap-1.5 rounded-md border border-slate-600 px-4 py-2 text-sm text-slate-200 hover:bg-slate-700"
          @click="openCreate"
        >
          <PlusIcon class="h-4 w-4" />
          Add your first host
        </button>
      </div>
      <p v-if="store.error" class="text-sm text-rose-400">{{ store.error }}</p>
    </main>

    <HostDialog :open="dialogOpen" :host="editingHost" @close="dialogOpen = false" @saved="onSaved" />

    <HostKeyTrustDialog
      :open="pendingHostKey !== null"
      :host-id="pendingHostKey?.hostId ?? ''"
      :host-name="pendingHostKey?.hostName ?? ''"
      :details="pendingHostKey?.details ?? null"
      @close="pendingHostKey = null"
      @trusted="pendingHostKey = null"
    />
  </div>
</template>
