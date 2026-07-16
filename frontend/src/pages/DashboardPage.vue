<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api } from '@/api/client'
import type { AppConfig } from '@/api/types'

const health = ref<string>('…')
const config = ref<AppConfig | null>(null)
const error = ref<string | null>(null)

onMounted(async () => {
  try {
    health.value = (await api.health()).status
    config.value = await api.config()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
})
</script>

<template>
  <main class="mx-auto flex min-h-full max-w-3xl flex-col gap-6 p-8">
    <header class="flex items-center gap-3">
      <span class="font-mono text-2xl text-emerald-400">&gt;_</span>
      <h1 class="text-2xl font-semibold tracking-tight">sessile</h1>
      <span class="text-sm text-slate-400">terminal session manager</span>
    </header>

    <section class="rounded-lg border border-slate-700 bg-slate-800/50 p-6">
      <h2 class="mb-4 text-sm font-medium uppercase tracking-wide text-slate-400">
        Backend status
      </h2>
      <dl class="grid grid-cols-[8rem_1fr] gap-y-2 text-sm">
        <dt class="text-slate-400">Health</dt>
        <dd>
          <span
            class="rounded px-2 py-0.5 text-xs font-medium"
            :class="
              health === 'ok'
                ? 'bg-emerald-500/15 text-emerald-400'
                : 'bg-amber-500/15 text-amber-400'
            "
            >{{ health }}</span
          >
        </dd>
        <template v-if="config">
          <dt class="text-slate-400">Root</dt>
          <dd class="font-mono text-slate-200">{{ config.root }}</dd>
          <dt class="text-slate-400">Shells</dt>
          <dd class="font-mono text-slate-200">{{ config.shells.join(', ') }}</dd>
          <dt class="text-slate-400">Version</dt>
          <dd class="font-mono text-slate-200">{{ config.version }}</dd>
        </template>
      </dl>
      <p v-if="error" class="mt-4 text-sm text-rose-400">{{ error }}</p>
    </section>

    <p class="text-sm text-slate-500">
      Scaffold (M0) is running. Session management UI arrives in later
      milestones.
    </p>
  </main>
</template>
