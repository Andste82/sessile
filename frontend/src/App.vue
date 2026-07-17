<script setup lang="ts">
import { RouterView } from 'vue-router'
import AppSidebar from '@/components/AppSidebar.vue'
import BottomNav from '@/components/BottomNav.vue'
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
