import { createRouter, createWebHistory } from 'vue-router'
import { useAuthStore } from '@/stores/auth'

const routes = [
  {
    path: '/login',
    name: 'login',
    component: () => import('@/pages/LoginPage.vue'),
    // No sidebar/nav, no session events, no auth required — see App.vue and
    // the beforeEach guard below.
    meta: { title: 'Log in', public: true },
  },
  {
    path: '/',
    name: 'dashboard',
    component: () => import('@/pages/DashboardPage.vue'),
    // Titles here are what the sidebar calls the view, spelled the same way —
    // the window title is how you tell tabs apart, so it has to match the
    // label you clicked to get here. This route is "Dashboard" in the nav.
    meta: { title: 'Dashboard' },
  },
  {
    path: '/sessions/:id',
    name: 'terminal',
    component: () => import('@/pages/TerminalPage.vue'),
    meta: { title: 'Terminal' },
  },
  {
    path: '/settings',
    name: 'settings',
    component: () => import('@/pages/SettingsPage.vue'),
    meta: { title: 'Settings' },
  },
  {
    path: '/admin/users',
    name: 'admin-users',
    component: () => import('@/pages/UsersPage.vue'),
    meta: { title: 'Users', adminOnly: true },
  },
]

export const router = createRouter({
  history: createWebHistory(),
  routes,
})

// Every route but /login requires a session cookie (PROJECT_PLAN.md §10);
// adminOnly routes (added by later milestones) additionally require
// auth.user.isAdmin. fetchMe() only runs once per page load — its result is
// cached on the store (auth.loaded) rather than re-checked on every
// navigation, since the cookie itself is what actually expires, not this
// client-side flag; a 401 on any later request clears it via
// setUnauthorizedHandler (main.ts) and this guard runs again on the next nav.
router.beforeEach(async (to) => {
  const auth = useAuthStore()
  if (!auth.loaded) {
    await auth.fetchMe()
  }
  if (to.meta.public) return true
  if (!auth.user) return { name: 'login', query: { redirect: to.fullPath } }
  if (to.meta.adminOnly && !auth.user.isAdmin) return { name: 'dashboard' }
  return true
})

// The window title is not set here: on the terminal route it also depends on
// the active session's name, which arrives after navigation. useDocumentTitle
// (installed in App.vue) watches both.
