<script setup lang="ts">
import { onMounted } from 'vue'
import { useSessionsStore } from '@/stores/sessions'

const store = useSessionsStore()

onMounted(() => {
  if (!store.config) store.fetchConfig()
})
</script>

<template>
  <div class="flex h-full flex-col">
    <header class="border-b border-slate-800 bg-slate-900 px-4 py-4 sm:px-6">
      <h1 class="text-lg font-semibold tracking-tight">Settings</h1>
    </header>

    <main class="mx-auto w-full max-w-2xl flex-1 overflow-y-auto p-4 sm:p-6">
      <section class="rounded-lg border border-slate-700 bg-slate-800/50 p-6">
        <h2 class="mb-4 text-sm font-medium uppercase tracking-wide text-slate-400">
          Server configuration
        </h2>
        <dl v-if="store.config" class="grid grid-cols-[7rem_1fr] gap-y-3 text-sm">
          <dt class="text-slate-400">Root</dt>
          <dd class="break-all font-mono text-slate-200">{{ store.config.root }}</dd>
          <dt class="text-slate-400">Shells</dt>
          <dd class="font-mono text-slate-200">{{ store.config.shells.join(', ') }}</dd>
          <dt class="text-slate-400">Version</dt>
          <dd class="font-mono text-slate-200">{{ store.config.version }}</dd>
        </dl>
        <p v-else class="text-sm text-slate-500">Loading…</p>
      </section>

      <p class="mt-4 text-xs text-slate-500">
        Configuration is read-only and set via server flags / environment
        variables. Authentication and multi-user support arrive in v0.4.
      </p>
    </main>
  </div>
</template>
