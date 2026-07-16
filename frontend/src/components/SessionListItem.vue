<script setup lang="ts">
import { RouterLink } from 'vue-router'
import { FolderIcon, UsersIcon, TrashIcon } from '@heroicons/vue/24/outline'
import StatusDot from './StatusDot.vue'
import type { Session } from '@/api/types'
import { relativeTime } from '@/utils/time'

defineProps<{ session: Session }>()
const emit = defineEmits<{ (e: 'delete', id: string): void }>()
</script>

<template>
  <RouterLink
    :to="`/sessions/${session.id}`"
    class="group flex flex-col gap-3 rounded-lg border border-slate-700 bg-slate-800/50 p-4 transition hover:border-slate-500 hover:bg-slate-800"
  >
    <div class="flex items-center gap-2">
      <StatusDot :status="session.status" />
      <span class="truncate font-medium text-slate-100">{{ session.name }}</span>
      <span class="ml-auto font-mono text-xs text-slate-400">{{ session.shell }}</span>
      <button
        class="rounded p-1 text-slate-500 opacity-0 transition hover:bg-slate-700 hover:text-rose-400 group-hover:opacity-100"
        title="Delete session"
        @click.prevent.stop="emit('delete', session.id)"
      >
        <TrashIcon class="h-4 w-4" />
      </button>
    </div>
    <div class="flex items-center gap-4 text-xs text-slate-400">
      <span class="flex min-w-0 items-center gap-1">
        <FolderIcon class="h-4 w-4 shrink-0" />
        <span class="truncate font-mono">{{ session.directory }}</span>
      </span>
      <span class="flex items-center gap-1">
        <UsersIcon class="h-4 w-4" />{{ session.clientCount }}
      </span>
      <span class="ml-auto whitespace-nowrap">{{ relativeTime(session.lastActivity) }}</span>
    </div>
  </RouterLink>
</template>
