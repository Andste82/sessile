<script setup lang="ts">
import { ref } from 'vue'
import { XMarkIcon } from '@heroicons/vue/20/solid'
import ProcessTreePanel from './ProcessTreePanel.vue'

defineProps<{ sessionId: string }>()
const emit = defineEmits<{ (e: 'close'): void }>()

// "Files" (list/move/copy/delete, download/upload — §4.10 M24-M26) is not
// built yet; this tab exists now so the panel's shape doesn't change out
// from under users once it lands. Process tree is the only working tab.
const tab = ref<'processes' | 'files'>('processes')
const tabCls = (active: boolean) =>
  active
    ? 'border-emerald-400 text-emerald-400'
    : 'border-transparent text-slate-400 hover:text-slate-200'
</script>

<template>
  <div class="flex h-full w-80 shrink-0 flex-col border-l border-slate-800 bg-slate-900">
    <div class="flex items-center justify-between border-b border-slate-800 pr-2">
      <div class="flex">
        <button
          type="button"
          class="border-b-2 px-3 py-2 text-xs font-medium"
          :class="tabCls(tab === 'processes')"
          @click="tab = 'processes'"
        >
          Processes
        </button>
        <button
          type="button"
          class="border-b-2 px-3 py-2 text-xs font-medium"
          :class="tabCls(tab === 'files')"
          @click="tab = 'files'"
        >
          Files
        </button>
      </div>
      <button
        type="button"
        class="flex h-6 w-6 items-center justify-center rounded text-slate-400 hover:bg-slate-800 hover:text-slate-200"
        aria-label="Close panel"
        @click="emit('close')"
      >
        <XMarkIcon class="h-4 w-4" />
      </button>
    </div>

    <ProcessTreePanel v-if="tab === 'processes'" :session-id="sessionId" class="min-h-0 flex-1" />
    <p v-else class="p-3 text-xs text-slate-500">File browser coming soon.</p>
  </div>
</template>
