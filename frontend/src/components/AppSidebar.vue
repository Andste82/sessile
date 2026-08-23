<script setup lang="ts">
import { RouterLink, useRoute } from 'vue-router'
import { HomeIcon, Cog6ToothIcon } from '@heroicons/vue/24/outline'
import { useSessionsStore } from '@/stores/sessions'
import StatusDot from './StatusDot.vue'

const store = useSessionsStore()
const route = useRoute()

function isTerminal(id: string) {
  return route.name === 'terminal' && route.params.id === id
}
</script>

<template>
  <aside
    class="flex w-16 shrink-0 flex-col border-r border-slate-800 bg-slate-900 lg:w-64"
  >
    <!-- Brand -->
    <RouterLink
      to="/"
      class="flex h-14 items-center gap-2 px-4 text-emerald-400"
      title="sessile"
    >
      <span class="font-mono text-xl">&gt;_</span>
      <span class="hidden text-lg font-semibold tracking-tight text-slate-100 lg:inline"
        >sessile</span
      >
    </RouterLink>

    <!-- Primary nav -->
    <nav class="flex flex-col gap-1 px-2 py-2">
      <RouterLink
        to="/"
        class="flex items-center gap-3 rounded-md px-3 py-2.5 text-sm text-slate-300 hover:bg-slate-800"
        :class="{ 'bg-slate-800 text-slate-100': route.name === 'dashboard' }"
        title="Dashboard"
      >
        <HomeIcon class="h-5 w-5 shrink-0" />
        <span class="hidden lg:inline">Dashboard</span>
      </RouterLink>
      <RouterLink
        to="/settings"
        class="flex items-center gap-3 rounded-md px-3 py-2.5 text-sm text-slate-300 hover:bg-slate-800"
        :class="{ 'bg-slate-800 text-slate-100': route.name === 'settings' }"
        title="Settings"
      >
        <Cog6ToothIcon class="h-5 w-5 shrink-0" />
        <span class="hidden lg:inline">Settings</span>
      </RouterLink>
    </nav>

    <!-- Session quick list (wide screens only) -->
    <div class="hidden min-h-0 flex-1 flex-col overflow-y-auto px-2 pb-2 lg:flex">
      <p class="px-3 py-2 text-xs font-medium uppercase tracking-wide text-slate-500">
        Sessions
      </p>
      <RouterLink
        v-for="s in store.sessions"
        :key="s.id"
        :to="`/sessions/${s.id}`"
        class="flex items-center gap-2 rounded-md px-3 py-2 text-sm text-slate-300 hover:bg-slate-800"
        :class="{ 'bg-slate-800 text-slate-100': isTerminal(s.id) }"
      >
        <StatusDot :status="s.status" />
        <span class="truncate">{{ s.name }}</span>
      </RouterLink>
      <p
        v-if="store.sessions.length === 0"
        class="px-3 py-2 text-sm text-slate-600"
      >
        None yet
      </p>
    </div>
  </aside>
</template>
