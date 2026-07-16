<script setup lang="ts">
// Fleshed out in M3 (frontend terminal). Placeholder for now.
import { useRoute } from 'vue-router'
const route = useRoute()
</script>

<template>
  <main class="flex min-h-full items-center justify-center p-8 text-slate-400">
    Terminal for session {{ route.params.id }} — coming in M3.
  </main>
</template>
