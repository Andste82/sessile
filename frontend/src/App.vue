<script setup lang="ts">
import { onMounted, onUnmounted } from 'vue'
import { RouterView } from 'vue-router'
import AppSidebar from '@/components/AppSidebar.vue'
import BottomNav from '@/components/BottomNav.vue'
import { useDocumentTitle } from '@/composables/useDocumentTitle'
import { useSessionsStore } from '@/stores/sessions'

const store = useSessionsStore()

useDocumentTitle()

// Polling lives here rather than on the dashboard because the session list is
// on screen everywhere: the sidebar and the terminal tab bar both draw status
// dots from it. Polled only from the dashboard, those dots kept whatever the
// last visit left behind — a backend restart went unnoticed until the user
// clicked the session.
onMounted(() => store.startPolling(5000))
onUnmounted(() => store.stopPolling())
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
