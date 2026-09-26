<script setup lang="ts">
import { ChevronRightIcon, ChatBubbleLeftRightIcon } from '@heroicons/vue/20/solid'

// The line that separates one group of sessions from what is above it, in both
// places that list sessions. The rule is the top border rather than a separate
// element, so the label sits *in* the line rather than under it.
//
// Only named groups get one of these. Ungrouped sessions have no header at
// all (§4.11), which is what keeps the dashboard of someone who never uses
// groups looking exactly as it did before.
// orchestrator: show the button that opens this group's own orchestrator
// (§4.18) — one per group, so its log is the group's.
defineProps<{
  name: string
  count: number
  collapsed: boolean
  dense?: boolean
  orchestrator?: boolean
  busy?: boolean
}>()
const emit = defineEmits<{ (e: 'toggle'): void; (e: 'orchestrator'): void }>()
</script>

<template>
  <div
    class="group/header flex w-full items-center gap-1.5 border-t border-slate-800 text-xs font-medium uppercase tracking-wide text-slate-500"
    :class="dense ? 'px-3 pb-1 pt-3' : 'px-1 pb-2 pt-4'"
  >
    <button
      type="button"
      class="flex min-w-0 flex-1 items-center gap-1.5 text-left transition hover:text-slate-300"
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
    <button
      v-if="orchestrator"
      type="button"
      class="shrink-0 rounded p-0.5 text-slate-600 opacity-0 transition hover:bg-slate-800 hover:text-emerald-400 focus:opacity-100 group-hover/header:opacity-100 disabled:opacity-40"
      :disabled="busy"
      :title="`Orchestrator for ${name}`"
      @click.stop="emit('orchestrator')"
    >
      <ChatBubbleLeftRightIcon class="h-3.5 w-3.5" />
    </button>
  </div>
</template>
