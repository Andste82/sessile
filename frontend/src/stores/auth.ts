import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api } from '@/api/client'
import type { AuthStatus, Credentials, User } from '@/api/types'

// Current user + login/bootstrap/register/logout. The router guard is what
// actually gates navigation (router/index.ts); this store just holds the
// state it reads.
export const useAuthStore = defineStore('auth', () => {
  const user = ref<User | null>(null)
  const status = ref<AuthStatus | null>(null)
  // Set once fetchMe() has resolved (successfully or not), so the router
  // guard can tell "we don't know yet" from "we checked, and there's no
  // one logged in" — the first must wait, the second must redirect.
  const loaded = ref(false)

  async function fetchStatus() {
    status.value = await api.authStatus()
    return status.value
  }

  // Resolves the current session cookie to a user, or to null if there isn't
  // one — a 401 here is an expected outcome, not an error to surface.
  async function fetchMe() {
    try {
      user.value = await api.me()
    } catch {
      user.value = null
    } finally {
      loaded.value = true
    }
    return user.value
  }

  async function bootstrap(creds: Credentials) {
    user.value = await api.bootstrap(creds)
    loaded.value = true
    return user.value
  }

  async function register(creds: Credentials) {
    user.value = await api.register(creds)
    loaded.value = true
    return user.value
  }

  async function login(creds: Credentials) {
    user.value = await api.login(creds)
    loaded.value = true
    return user.value
  }

  async function logout() {
    try {
      await api.logout()
    } finally {
      user.value = null
    }
  }

  // Called by the client's setUnauthorizedHandler (main.ts) on any 401 — the
  // session cookie lapsed or was revoked somewhere else. Clears state without
  // hitting the network again; the router guard sends the app to /login.
  function clear() {
    user.value = null
    loaded.value = true
  }

  return { user, status, loaded, fetchStatus, fetchMe, bootstrap, register, login, logout, clear }
})
