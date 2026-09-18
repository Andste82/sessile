<script setup lang="ts">
import { RouterLink, useRoute } from 'vue-router'

// The tabs across the top of the Agent pages (§7): one header for the
// sections the sidebar lists under "Agent", so a phone — which only has one
// "Agent" entry in its bottom nav — still reaches every one of them.
const route = useRoute()

const tabs = [
  { to: '/agent/settings', name: 'agent-settings', label: 'Connections & Git' },
  { to: '/agent/notes', name: 'agent-notes', label: 'Notes' },
]
</script>

<template>
  <nav class="flex gap-1 overflow-x-auto border-b border-slate-800 bg-slate-900 px-4 sm:px-6">
    <RouterLink
      v-for="t in tabs"
      :key="t.to"
      :to="t.to"
      class="whitespace-nowrap border-b-2 px-3 py-2.5 text-sm"
      :class="route.name === t.name ? 'border-emerald-500 text-slate-100' : 'border-transparent text-slate-400 hover:text-slate-200'"
    >
      {{ t.label }}
    </RouterLink>
  </nav>
</template>
