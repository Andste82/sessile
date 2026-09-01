<script setup lang="ts">
import { RouterView } from 'vue-router'
import AppSidebar from '@/components/AppSidebar.vue'
import BottomNav from '@/components/BottomNav.vue'
import { useSessionEvents } from '@/composables/useSessionEvents'
import { hasFinePointer } from '@/utils/device'

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

// Desktop chrome (sidebar) vs. phone chrome (bottom nav, and — inside it —
// the on-screen special-key bar's toggle) is chosen by input capability, the
// same signal TerminalView.vue already uses to decide whether to steal focus
// (and pop the soft keyboard) on mount — not by viewport width. A phone held
// in landscape is routinely wider than a narrow desktop window (700-950px is
// typical), so a width breakpoint alone picked the desktop sidebar there,
// which has no way to reach BottomNav's "Keys" button at all. Read once on
// mount: pointer capability doesn't change from a rotation or a resize.
const touchPrimary = !hasFinePointer(window)
</script>

<template>
  <div class="flex h-dvh overflow-hidden bg-slate-900 text-slate-200">
    <!-- Sidebar: icon rail 640–1024px, full at ≥1024px, phones get BottomNav instead. -->
    <AppSidebar v-if="!touchPrimary" />

    <!-- Content: leaves room for the fixed bottom nav on phones. -->
    <main
      class="relative flex min-w-0 flex-1 flex-col overflow-hidden"
      :class="touchPrimary ? 'pb-14' : ''"
    >
      <RouterView />
    </main>

    <!-- Bottom navigation on phones only. -->
    <BottomNav v-if="touchPrimary" />
  </div>
</template>
