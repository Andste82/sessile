import { createRouter, createWebHistory } from 'vue-router'

const routes = [
  {
    path: '/',
    name: 'dashboard',
    component: () => import('@/pages/DashboardPage.vue'),
    meta: { title: 'Sessions' },
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
]

export const router = createRouter({
  history: createWebHistory(),
  routes,
})

// The window title is not set here: on the terminal route it also depends on
// the active session's name, which arrives after navigation. useDocumentTitle
// (installed in App.vue) watches both.
