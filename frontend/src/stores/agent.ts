import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { api } from '@/api/client'
import type {
  AgentSettings,
  AgentSettingsBody,
  Connection,
  ConnectionKind,
  Profile,
  TaskDefaults,
} from '@/api/types'

// A connection as the dialog edits it: every non-secret field, and only the
// secrets the user actually typed (an absent secret keeps the saved one).
export interface ConnectionDraft {
  id: string
  name: string
  kind: string
  fields: Record<string, string>
  expires: string // YYYY-MM-DD, '' for none
}

export interface GitDraft {
  id: string
  host: string
  name: string
  email: string
  username: string
  token?: string
}

// The agent settings document (§4.13, §4.16). The server takes the whole
// document on every save, so each action rebuilds it from what is loaded,
// with secrets left out unless this edit set one.
export const useAgentStore = defineStore('agent', () => {
  const settings = ref<AgentSettings | null>(null)
  const kinds = ref<ConnectionKind[]>([])
  const loading = ref(false)
  const error = ref<string | null>(null)

  async function load() {
    loading.value = true
    error.value = null
    try {
      const [s, k] = await Promise.all([api.agentSettings(), api.connectionKinds()])
      settings.value = s
      kinds.value = k
    } catch (e) {
      error.value = e instanceof Error ? e.message : String(e)
    } finally {
      loading.value = false
    }
  }

  function kind(id: string): ConnectionKind | undefined {
    return kinds.value.find((k) => k.id === id)
  }

  function connection(id: string): Connection | undefined {
    return settings.value?.connections.find((c) => c.id === id)
  }

  function baseBody(s: AgentSettings): AgentSettingsBody {
    return {
      connections: s.connections.map((c) => ({
        id: c.id,
        name: c.name,
        kind: c.kind,
        fields: { ...c.fields },
        expires: c.expires ?? '',
      })),
      profiles: s.profiles.map((p) => ({ id: p.id, name: p.name, connectionId: p.connectionId, model: p.model })),
      taskDefaults: { ...s.taskDefaults },
      git: s.git.map((g) => ({ id: g.id, host: g.host, name: g.name, email: g.email, username: g.username })),
    }
  }

  async function save(edit: (body: AgentSettingsBody) => void) {
    if (!settings.value) await load()
    if (!settings.value) throw new Error(error.value ?? 'agent settings are not loaded')
    const body = baseBody(settings.value)
    edit(body)
    settings.value = await api.putAgentSettings(body)
  }

  function upsert<T extends { id: string }>(list: T[], item: T) {
    const i = list.findIndex((x) => x.id === item.id)
    if (i >= 0) list[i] = item
    else list.push(item)
  }

  const saveConnection = (d: ConnectionDraft) =>
    save((b) => upsert(b.connections, { id: d.id, name: d.name, kind: d.kind, fields: d.fields, expires: d.expires }))

  // Profiles on a deleted connection go with it: a profile without a
  // connection can't start anything, and the server refuses the dangling
  // reference anyway.
  const deleteConnection = (id: string) =>
    save((b) => {
      const gone = new Set(b.profiles.filter((p) => p.connectionId === id).map((p) => p.id))
      b.connections = b.connections.filter((c) => c.id !== id)
      b.profiles = b.profiles.filter((p) => !gone.has(p.id))
      if (gone.has(b.taskDefaults.profileId)) b.taskDefaults.profileId = ''
    })

  const saveProfile = (p: Omit<Profile, 'agent'>) =>
    save((b) => upsert(b.profiles, { id: p.id, name: p.name, connectionId: p.connectionId, model: p.model }))

  const deleteProfile = (id: string) =>
    save((b) => {
      b.profiles = b.profiles.filter((p) => p.id !== id)
      if (b.taskDefaults.profileId === id) b.taskDefaults.profileId = ''
    })

  const saveGit = (g: GitDraft) => save((b) => upsert(b.git, { ...g }))

  const deleteGit = (id: string) =>
    save((b) => {
      b.git = b.git.filter((g) => g.id !== id)
    })

  const saveTaskDefaults = (d: TaskDefaults) =>
    save((b) => {
      b.taskDefaults = { ...d }
    })

  const profilesByAgent = computed(() => {
    const out: Record<string, Profile[]> = {}
    for (const p of settings.value?.profiles ?? []) (out[p.agent] ??= []).push(p)
    return out
  })

  return {
    settings,
    kinds,
    loading,
    error,
    load,
    kind,
    connection,
    saveConnection,
    deleteConnection,
    saveProfile,
    deleteProfile,
    saveGit,
    deleteGit,
    saveTaskDefaults,
    profilesByAgent,
  }
})

