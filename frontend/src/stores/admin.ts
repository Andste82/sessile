import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api } from '@/api/client'
import type { User } from '@/api/types'

// Admin-only user management (§12b M11's API surface). Kept separate from
// stores/auth.ts, which only ever holds the current user — this holds every
// account, and only an admin's session can populate it at all.
export const useAdminStore = defineStore('admin', () => {
  const users = ref<User[]>([])
  const loading = ref(false)
  const error = ref<string | null>(null)

  async function fetchUsers() {
    loading.value = true
    error.value = null
    try {
      users.value = await api.listUsers()
    } catch (e) {
      error.value = e instanceof Error ? e.message : String(e)
    } finally {
      loading.value = false
    }
  }

  async function deleteUser(id: string) {
    await api.deleteUser(id)
    users.value = users.value.filter((u) => u.id !== id)
  }

  async function setAdmin(id: string, isAdmin: boolean) {
    const updated = await api.setUserAdmin(id, isAdmin)
    users.value = users.value.map((u) => (u.id === id ? updated : u))
    return updated
  }

  return { users, loading, error, fetchUsers, deleteUser, setAdmin }
})
