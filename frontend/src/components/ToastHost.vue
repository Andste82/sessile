<script setup lang="ts">
import { useToastsStore } from '@/stores/toasts'

const toasts = useToastsStore()

const kindClass: Record<string, string> = {
  info: 'border-slate-600 text-slate-200',
  warning: 'border-amber-600 text-amber-200',
  error: 'border-rose-600 text-rose-200',
}
</script>

<template>
  <!-- Mounted once in AppShell. polite: toasts never steal focus or
       interrupt what a screen reader is saying. -->
  <div
    aria-live="polite"
    class="pointer-events-none fixed bottom-16 right-4 z-50 flex w-80 max-w-[calc(100vw-2rem)] flex-col gap-2 sm:bottom-4"
  >
    <div
      v-for="t in toasts.toasts"
      :key="t.id"
      class="pointer-events-auto flex items-start gap-2 rounded-md border bg-slate-800 px-3 py-2 text-sm shadow-lg"
      :class="kindClass[t.kind]"
    >
      <span class="min-w-0 flex-1 break-words">{{ t.message }}</span>
      <button
        type="button"
        class="shrink-0 text-slate-400 hover:text-slate-200"
        aria-label="Dismiss"
        @click="toasts.dismiss(t.id)"
      >
        ×
      </button>
    </div>
  </div>
</template>
