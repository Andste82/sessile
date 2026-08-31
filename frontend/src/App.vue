<script setup lang="ts">
import { RouterView, useRoute } from 'vue-router'
import AppShell from '@/components/AppShell.vue'
import { useDocumentTitle } from '@/composables/useDocumentTitle'

useDocumentTitle()

const route = useRoute()
</script>

<template>
  <!-- /login renders bare — no sidebar, no bottom nav, no session-events
       socket (see AppShell.vue's comment). Everything else gets the full
       app chrome, which the router guard has already confirmed a session
       for by the time this renders. -->
  <RouterView v-if="route.meta.public" />
  <AppShell v-else />
</template>
