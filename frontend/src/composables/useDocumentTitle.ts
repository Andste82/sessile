import { watch } from 'vue'
import { useRoute } from 'vue-router'
import { useSessionsStore } from '@/stores/sessions'

const BRAND = 'Sessile'

// documentTitleFor builds the window title. A session name wins over the route
// title: with several tabs open, "Sessile — Terminal" everywhere told the user
// nothing about which terminal they were looking at.
export function documentTitleFor(
  routeTitle: string | null | undefined,
  sessionName: string | null | undefined,
): string {
  const name = sessionName?.trim()
  if (name) return `${BRAND} • ${name}`
  return routeTitle ? `${BRAND} — ${routeTitle}` : BRAND
}

// useDocumentTitle keeps document.title in sync with the active route and, on
// the terminal route, with the active session's name in the store. Deriving it
// from the store rather than pushing it from TerminalPage means a rename or a
// switch between tabs updates the title on its own — and it does not matter
// whether the session was activated from the tab bar or the sidebar, since both
// go through the router.
export function useDocumentTitle() {
  const route = useRoute()
  const store = useSessionsStore()

  watch(
    () => {
      const sessionName =
        route.name === 'terminal' ? store.byId(String(route.params.id))?.name : null
      return documentTitleFor(route.meta.title as string | undefined, sessionName)
    },
    (title) => {
      document.title = title
    },
    { immediate: true },
  )
}
