<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink } from 'vue-router'
import { ArrowPathIcon, FolderIcon, TrashIcon, UsersIcon } from '@heroicons/vue/24/outline'
import StatusDot from './StatusDot.vue'
import type { Session } from '@/api/types'
import { displayCommand, displayDirectory } from '@/utils/session'
import { relativeTime } from '@/utils/time'

const props = defineProps<{ session: Session }>()
const emit = defineEmits<{
  (e: 'delete', id: string): void
  (e: 'restart', id: string): void
}>()

const command = computed(() => displayCommand(props.session))
const directory = computed(() => displayDirectory(props.session))
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
        v-if="session.status === 'stopped'"
        class="rounded p-1 text-slate-500 opacity-100 transition hover:bg-slate-700 hover:text-emerald-400 sm:opacity-0 sm:group-hover:opacity-100"
        title="Restart session"
        @click.prevent.stop="emit('restart', session.id)"
      >
        <ArrowPathIcon class="h-4 w-4" />
      </button>
      <button
        class="rounded p-1 text-slate-500 opacity-100 transition hover:bg-slate-700 hover:text-rose-400 sm:opacity-0 sm:group-hover:opacity-100"
        title="Delete session"
        @click.prevent.stop="emit('delete', session.id)"
      >
        <TrashIcon class="h-4 w-4" />
      </button>
    </div>

    <!--
      What is running in the session. The row keeps its height even when the
      foreground is unknown, so the grid does not reflow each time a session
      changes.
    -->
    <div class="flex min-h-4 items-center gap-2 text-xs">
      <span class="truncate font-mono text-slate-400">{{ command }}</span>
    </div>

    <div class="flex items-center gap-4 text-xs text-slate-400">
      <span class="flex min-w-0 items-center gap-1">
        <FolderIcon class="h-4 w-4 shrink-0" />
        <span class="truncate font-mono">{{ directory }}</span>
      </span>
      <!-- Only worth the space once a session is mirrored somewhere else. -->
      <span
        v-if="session.clientCount > 1"
        class="flex shrink-0 items-center gap-1"
        :title="`${session.clientCount} browsers attached`"
      >
        <UsersIcon class="h-4 w-4" />
        {{ session.clientCount }}
      </span>
      <span class="ml-auto whitespace-nowrap">{{ relativeTime(session.lastActivity) }}</span>
    </div>
  </RouterLink>
</template>
