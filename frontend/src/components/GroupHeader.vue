<script setup lang="ts">
import { ChevronRightIcon } from '@heroicons/vue/20/solid'

// The line that separates one group of sessions from what is above it, in both
// places that list sessions. The rule is the top border rather than a separate
// element, so the label sits *in* the line rather than under it.
//
// Only named groups get one of these. Ungrouped sessions have no header at
// all (§4.11), which is what keeps the dashboard of someone who never uses
// groups looking exactly as it did before.
defineProps<{ name: string; count: number; collapsed: boolean; dense?: boolean }>()
const emit = defineEmits<{ (e: 'toggle'): void }>()
</script>

<template>
  <button
    type="button"
    class="flex w-full items-center gap-1.5 border-t border-slate-800 text-left text-xs font-medium uppercase tracking-wide text-slate-500 transition hover:text-slate-300"
    :class="dense ? 'px-3 pb-1 pt-3' : 'px-1 pb-2 pt-4'"
    :aria-expanded="!collapsed"
    @click="emit('toggle')"
  >
    <ChevronRightIcon
      class="h-3.5 w-3.5 shrink-0 transition-transform"
      :class="{ 'rotate-90': !collapsed }"
    />
    <span class="truncate">{{ name }}</span>
    <span class="text-slate-600">{{ count }}</span>
  </button>
</template>
