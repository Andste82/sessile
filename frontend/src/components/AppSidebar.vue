<script setup lang="ts">
import { RouterLink, useRoute } from 'vue-router'
import { HomeIcon, ServerIcon, Cog6ToothIcon, UsersIcon } from '@heroicons/vue/24/outline'
import { useSessionsStore } from '@/stores/sessions'
import { useAuthStore } from '@/stores/auth'
import { useUiStore } from '@/stores/ui'
import StatusDot from './StatusDot.vue'
import GroupHeader from './GroupHeader.vue'

const store = useSessionsStore()
const auth = useAuthStore()
const ui = useUiStore()
const route = useRoute()

function isTerminal(id: string) {
  return route.name === 'terminal' && route.params.id === id
}

// A collapsed group still shows the session currently on screen. Hiding it
// would take the one entry that is actively in use out of the list while its
// terminal is right there — the count in the header says the rest are folded
// away, and this keeps the sidebar agreeing with what the user is looking at.
function visible(group: { name: string; sessions: typeof store.sessions }) {
  if (!group.name || !ui.isGroupCollapsed('sidebar', group.name)) return group.sessions
  return group.sessions.filter((s) => isTerminal(s.id))
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
        to="/hosts"
        class="flex items-center gap-3 rounded-md px-3 py-2.5 text-sm text-slate-300 hover:bg-slate-800"
        :class="{ 'bg-slate-800 text-slate-100': route.name === 'hosts' }"
        title="Hosts"
      >
        <ServerIcon class="h-5 w-5 shrink-0" />
        <span class="hidden lg:inline">Hosts</span>
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
      <RouterLink
        v-if="auth.user?.isAdmin"
        to="/admin/users"
        class="flex items-center gap-3 rounded-md px-3 py-2.5 text-sm text-slate-300 hover:bg-slate-800"
        :class="{ 'bg-slate-800 text-slate-100': route.name === 'admin-users' }"
        title="Users"
      >
        <UsersIcon class="h-5 w-5 shrink-0" />
        <span class="hidden lg:inline">Users</span>
      </RouterLink>
    </nav>

    <!-- Session quick list (wide screens only) -->
    <div class="hidden min-h-0 flex-1 flex-col overflow-y-auto px-2 pb-2 lg:flex">
      <p class="px-3 py-2 text-xs font-medium uppercase tracking-wide text-slate-500">
        Sessions
      </p>
      <template v-for="g in store.grouped" :key="g.name">
        <GroupHeader
          v-if="g.name"
          dense
          :name="g.name"
          :count="g.sessions.length"
          :collapsed="ui.isGroupCollapsed('sidebar', g.name)"
          @toggle="ui.toggleGroup('sidebar', g.name)"
        />
        <RouterLink
          v-for="s in visible(g)"
          :key="s.id"
          :to="`/sessions/${s.id}`"
          class="flex items-center gap-2 rounded-md px-3 py-2 text-sm text-slate-300 hover:bg-slate-800"
          :class="{ 'bg-slate-800 text-slate-100': isTerminal(s.id) }"
        >
          <StatusDot :status="s.status" />
          <span class="truncate">{{ s.name }}</span>
        </RouterLink>
      </template>
      <p
        v-if="store.sessions.length === 0"
        class="px-3 py-2 text-sm text-slate-600"
      >
        None yet
      </p>
    </div>
  </aside>
</template>
