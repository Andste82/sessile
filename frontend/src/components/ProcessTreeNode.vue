<script setup lang="ts">
import { ref } from 'vue'
import { ChevronRightIcon } from '@heroicons/vue/20/solid'
import type { Process } from '@/api/types'

defineProps<{ process: Process; depth: number }>()

const expanded = ref(true)
</script>

<template>
  <li>
    <div
      class="flex items-center gap-1 rounded px-1 py-0.5 font-mono text-xs text-slate-300 hover:bg-slate-800"
      :style="{ paddingLeft: `${depth * 14}px` }"
    >
      <button
        v-if="process.children.length > 0"
        type="button"
        class="flex h-4 w-4 shrink-0 items-center justify-center text-slate-500 hover:text-slate-300"
        :aria-label="expanded ? 'Collapse' : 'Expand'"
        @click="expanded = !expanded"
      >
        <ChevronRightIcon class="h-3 w-3 transition-transform" :class="{ 'rotate-90': expanded }" />
      </button>
      <span v-else class="w-4 shrink-0" />
      <span class="text-slate-500">{{ process.pid }}</span>
      <span class="truncate text-slate-200">{{ process.command }}</span>
    </div>
    <ul v-if="expanded && process.children.length > 0">
      <ProcessTreeNode
        v-for="child in process.children"
        :key="child.pid"
        :process="child"
        :depth="depth + 1"
      />
    </ul>
  </li>
</template>
