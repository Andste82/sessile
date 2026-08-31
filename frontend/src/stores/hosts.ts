import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { api } from '@/api/client'
import type { Host, HostBody, HostKeyProbeResponse } from '@/api/types'

// Per-user SSH host list (§12b M14's API surface). Grouped for the Hosts
// page and, later, the new-session host picker (M18) — both want "hosts by
// group," not a flat list.
export const useHostsStore = defineStore('hosts', () => {
  const hosts = ref<Host[]>([])
  const loading = ref(false)
  const error = ref<string | null>(null)

  async function fetchHosts() {
    loading.value = true
    error.value = null
    try {
      hosts.value = await api.listHosts()
    } catch (e) {
      error.value = e instanceof Error ? e.message : String(e)
    } finally {
      loading.value = false
    }
  }

  async function createHost(body: HostBody) {
    const created = await api.createHost(body)
    hosts.value = [...hosts.value, created]
    return created
  }

  async function updateHost(id: string, body: HostBody) {
    const updated = await api.updateHost(id, body)
    hosts.value = hosts.value.map((h) => (h.id === id ? updated : h))
    return updated
  }

  async function deleteHost(id: string) {
    await api.deleteHost(id)
    hosts.value = hosts.value.filter((h) => h.id !== id)
  }

  function probeHostKey(id: string): Promise<HostKeyProbeResponse> {
    return api.probeHostKey(id)
  }

  // Pins the fingerprint on the host, then refreshes it locally so callers
  // (the Hosts page, or a retry after §4.5.1's trust dialog) see the new
  // pin without a full refetch.
  async function trustHostKey(id: string, body: { fingerprint: string; keyType: string }) {
    await api.trustHostKey(id, body)
    const updated = await api.getHost(id)
    hosts.value = hosts.value.map((h) => (h.id === id ? updated : h))
    return updated
  }

  // Grouped by Host.group, "Ungrouped" for an empty one, insertion order
  // otherwise preserved within each group.
  const grouped = computed(() => {
    const out: Record<string, Host[]> = {}
    for (const h of hosts.value) {
      const key = h.group || 'Ungrouped'
      ;(out[key] ??= []).push(h)
    }
    return out
  })

  return {
    hosts,
    loading,
    error,
    grouped,
    fetchHosts,
    createHost,
    updateHost,
    deleteHost,
    probeHostKey,
    trustHostKey,
  }
})
