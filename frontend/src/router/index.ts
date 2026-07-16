import { createRouter, createWebHistory } from 'vue-router'

const routes = [
  {
    path: '/',
    name: 'dashboard',
    component: () => import('@/pages/DashboardPage.vue'),
  },
  {
    path: '/sessions/:id',
    name: 'terminal',
    component: () => import('@/pages/TerminalPage.vue'),
  },
  {
    path: '/settings',
    name: 'settings',
    component: () => import('@/pages/SettingsPage.vue'),
  },
]

export const router = createRouter({
  history: createWebHistory(),
  routes,
})
