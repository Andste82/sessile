import { ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useSessionsStore } from '@/stores/sessions'

const BRAND = 'Sessile'

// The name of the session on screen, as the terminal page knows it.
//
// The page always has the session: it takes it from the list when it is there
// and fetches it by id when it is not. Reading the list alone was the weak
// link — the title had nothing to show until a poll had filled it in, and
// nothing at all if that request never landed, which is a page-wide failure
// showing up as a permanently generic window title.
const activeSessionName = ref<string | null>(null)

/**
 * setActiveSessionName tells the title which session is on screen. The terminal
 * page calls it with the session it has, and with null when it goes away.
 */
export function setActiveSessionName(name: string | null) {
  activeSessionName.value = name
}

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

// activeName answers what the session on the terminal route is called, from
// whichever source knows first.
//
// The page's own copy leads because it is the one that cannot be missing: it is
// the session the page is showing. The list is the fallback, and still worth
// consulting — it is what carries a rename through, and it answers on the first
// render of a session already in it, before the page has set anything.
function activeName(
  route: { name: unknown; params: Record<string, unknown> },
  byId: (id: string) => { name: string } | null,
): string | null {
  if (route.name !== 'terminal') return null
  return activeSessionName.value ?? byId(String(route.params.id))?.name ?? null
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
