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

// activeName answers what the session on the terminal route is called.
//
// The list is the only source, and it is the right one: it is polled, so a
// rename from another browser reaches it, and the tab bar beside the title is
// drawn from it — reading them from different places is how they come to
// disagree. It is also never behind the page, because the page puts what it
// fetched into the list (TerminalPage.loadSession → upsertSession) rather than
// keeping it to itself.
function activeName(
  route: { name: unknown; params: Record<string, unknown> },
  byId: (id: string) => { name: string } | null,
): string | null {
  if (route.name !== 'terminal') return null
  return byId(String(route.params.id))?.name ?? null
}

// useDocumentTitle keeps document.title in sync with the active route and, on
// the terminal route, with the name of the session on screen. Deriving it here
// rather than having each page assign document.title means a rename or a switch
// between tabs updates the title on its own — and it does not matter whether the
// session was activated from the tab bar or the sidebar, since both go through
// the router.
export function useDocumentTitle() {
  const route = useRoute()
  const store = useSessionsStore()

  watch(
    () => {
      const sessionName = activeName(route, store.byId)
      return documentTitleFor(route.meta.title as string | undefined, sessionName)
    },
    (title) => {
      document.title = title
    },
    { immediate: true },
  )
}
