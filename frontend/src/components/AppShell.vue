<script setup lang="ts">
import { RouterView } from 'vue-router'
import AppSidebar from '@/components/AppSidebar.vue'
import BottomNav from '@/components/BottomNav.vue'
import { useSessionEvents } from '@/composables/useSessionEvents'

// The authenticated app chrome: sidebar, bottom nav, and the session-events
// socket. Split out of App.vue so useSessionEvents only ever runs once the
// router guard has confirmed a valid session — mounting it on /login would
// try (and fail, on repeat, with backoff) to open /ws/events before anyone
// is logged in.
//
// The session list is kept live here rather than on the dashboard because it
// is on screen everywhere: the sidebar and the terminal tab bar both draw
// indicators from it. Fed only from the dashboard, those indicators kept
// whatever the last visit left behind — a backend restart went unnoticed
// until the user clicked the session.
//
// This replaces the app-wide 5 s poll with the event channel (§5.1) and keeps
// polling as its fallback; see useSessionEvents.
useSessionEvents()
</script>

<template>
  <div class="flex h-dvh overflow-hidden bg-slate-900 text-slate-200">
    <!-- Sidebar: icon rail 640–1024px, full at ≥1024px, hidden below 640px. -->
    <AppSidebar class="hidden sm:flex" />

    <!-- Content: leaves room for the fixed bottom nav on phones. -->
    <main class="relative flex min-w-0 flex-1 flex-col overflow-hidden pb-14 sm:pb-0">
      <RouterView />
    </main>

    <!-- Bottom navigation on phones only. -->
    <BottomNav class="sm:hidden" />
  </div>
</template>
