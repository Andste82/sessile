<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { XMarkIcon } from '@heroicons/vue/20/solid'
import { useSessionsStore } from '@/stores/sessions'
import StatusDot from './StatusDot.vue'
import type { Session } from '@/api/types'

const store = useSessionsStore()
const route = useRoute()
const router = useRouter()

const activeId = computed(() => String(route.params.id))

interface Tab {
  id: string
  name: string
  status: Session['status']
}

const tabs = computed<Tab[]>(() =>
  store.openTabIds.map((id) => {
    const s = store.byId(id)
    return { id, name: s?.name ?? 'session', status: s?.status ?? 'stopped' }
  }),
)

function close(id: string) {
  const idx = store.openTabIds.indexOf(id)
  store.closeTab(id)
  if (id === activeId.value) {
    const next = store.openTabIds[idx] ?? store.openTabIds[idx - 1] ?? null
    router.push(next ? `/sessions/${next}` : '/')
  }
}
</script>

<template>
  <div
    v-if="tabs.length > 0"
    class="flex items-stretch gap-1 overflow-x-auto border-b border-slate-800 bg-slate-900 px-1"
  >
    <button
      v-for="tab in tabs"
      :key="tab.id"
      type="button"
      class="group flex h-11 min-w-0 max-w-[12rem] shrink-0 items-center gap-2 rounded-t-md border-b-2 px-3 text-sm"
      :class="
        tab.id === activeId
          ? 'border-emerald-400 bg-slate-800 text-slate-100'
          : 'border-transparent text-slate-400 hover:bg-slate-800/50'
      "
      @click="router.push(`/sessions/${tab.id}`)"
    >
      <StatusDot :status="tab.status" />
      <span class="truncate">{{ tab.name }}</span>
      <XMarkIcon
        class="h-4 w-4 shrink-0 rounded text-slate-500 opacity-60 hover:bg-slate-700 hover:text-slate-200 group-hover:opacity-100"
        @click.stop="close(tab.id)"
      />
    </button>
  </div>
</template>
