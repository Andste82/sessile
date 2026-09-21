import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { api, ApiRequestError } from '@/api/client'
import { useSessionsStore } from '@/stores/sessions'

// The orchestrator (§4.18): one agent session per user, on the sessile
// server. Opening it is the same action everywhere — the sidebar, the bottom
// nav, the dashboard — so the busy flag and the error live here.

const busy = ref(false)
const error = ref('')

export function useOrchestrator() {
  const router = useRouter()
  const sessions = useSessionsStore()

  async function open() {
    if (busy.value) return
    busy.value = true
    error.value = ''
    try {
      const session = await api.openOrchestrator()
      await sessions.refreshSessions()
      await router.push(`/sessions/${session.id}`)
    } catch (e) {
      // Most often: no agent profile yet. Say so and leave the user where
      // they are; the Agent pages are one click away.
      error.value = e instanceof ApiRequestError ? e.message : 'Could not open the orchestrator'
    } finally {
      busy.value = false
    }
  }

  return { open, busy, error }
}
