<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import {
  HomeIcon,
  CommandLineIcon,
  ServerIcon,
  Cog6ToothIcon,
  Squares2X2Icon,
  UsersIcon,
} from '@heroicons/vue/24/outline'
import { useSessionsStore } from '@/stores/sessions'
import { useUiStore } from '@/stores/ui'
import { useAuthStore } from '@/stores/auth'

const store = useSessionsStore()
const ui = useUiStore()
const auth = useAuthStore()
const route = useRoute()
const router = useRouter()

// The "Terminal" tab targets the current terminal, else the most recent tab.
const terminalTarget = computed(() => {
  if (route.name === 'terminal') return String(route.params.id)
  return store.openTabIds[store.openTabIds.length - 1] ?? null
})

function goTerminal() {
  if (terminalTarget.value) router.push(`/sessions/${terminalTarget.value}`)
  else router.push('/')
}
</script>

<template>
  <nav
    class="fixed inset-x-0 bottom-0 z-20 flex h-14 items-stretch border-t border-slate-800 bg-slate-900/95 backdrop-blur"
  >
    <RouterLink
      to="/"
      class="flex flex-1 flex-col items-center justify-center gap-0.5 text-xs"
      :class="route.name === 'dashboard' ? 'text-emerald-400' : 'text-slate-400'"
    >
      <HomeIcon class="h-6 w-6" />
      Dashboard
    </RouterLink>
    <button
      type="button"
      class="flex flex-1 flex-col items-center justify-center gap-0.5 text-xs"
      :class="route.name === 'terminal' ? 'text-emerald-400' : 'text-slate-400'"
      @click="goTerminal"
    >
      <CommandLineIcon class="h-6 w-6" />
      Terminal
    </button>
    <button
      v-if="route.name === 'terminal'"
      type="button"
      class="flex flex-1 flex-col items-center justify-center gap-0.5 text-xs"
      :class="ui.keyBarOpen ? 'text-emerald-400' : 'text-slate-400'"
      @click="ui.toggleKeyBar()"
    >
      <Squares2X2Icon class="h-6 w-6" />
      Keys
    </button>
    <RouterLink
      to="/hosts"
      class="flex flex-1 flex-col items-center justify-center gap-0.5 text-xs"
      :class="route.name === 'hosts' ? 'text-emerald-400' : 'text-slate-400'"
    >
      <ServerIcon class="h-6 w-6" />
      Hosts
    </RouterLink>
    <RouterLink
      to="/settings"
      class="flex flex-1 flex-col items-center justify-center gap-0.5 text-xs"
      :class="route.name === 'settings' ? 'text-emerald-400' : 'text-slate-400'"
    >
      <Cog6ToothIcon class="h-6 w-6" />
      Settings
    </RouterLink>
    <RouterLink
      v-if="auth.user?.isAdmin"
      to="/admin/users"
      class="flex flex-1 flex-col items-center justify-center gap-0.5 text-xs"
      :class="route.name === 'admin-users' ? 'text-emerald-400' : 'text-slate-400'"
    >
      <UsersIcon class="h-6 w-6" />
      Users
    </RouterLink>
  </nav>
</template>
