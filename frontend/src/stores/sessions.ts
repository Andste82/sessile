import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { api } from '@/api/client'
import type { AppConfig, CreateSessionBody, Session } from '@/api/types'

// Session list + config store. Polling is added in M4.
export const useSessionsStore = defineStore('sessions', () => {
  const sessions = ref<Session[]>([])
  const config = ref<AppConfig | null>(null)
  const loading = ref(false)
  const error = ref<string | null>(null)

  const byId = computed(
    () => (id: string) => sessions.value.find((s) => s.id === id) ?? null,
  )

  async function fetchConfig() {
    config.value = await api.config()
  }

  async function fetchSessions() {
    loading.value = true
    error.value = null
    try {
      sessions.value = await api.listSessions()
    } catch (e) {
      error.value = e instanceof Error ? e.message : String(e)
    } finally {
      loading.value = false
    }
  }

  async function createSession(body: CreateSessionBody): Promise<Session> {
    const created = await api.createSession(body)
    sessions.value = [created, ...sessions.value.filter((s) => s.id !== created.id)]
    return created
  }

  async function deleteSession(id: string) {
    await api.deleteSession(id)
    sessions.value = sessions.value.filter((s) => s.id !== id)
  }

  async function renameSession(id: string, name: string) {
    const updated = await api.renameSession(id, name)
    sessions.value = sessions.value.map((s) => (s.id === id ? updated : s))
    return updated
  }

  return {
    sessions,
    config,
    loading,
    error,
    byId,
    fetchConfig,
    fetchSessions,
    createSession,
    deleteSession,
    renameSession,
  }
})
