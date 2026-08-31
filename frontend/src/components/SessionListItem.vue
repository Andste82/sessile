<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink } from 'vue-router'
import { ArrowPathIcon, FolderIcon, ServerIcon, TrashIcon, UsersIcon } from '@heroicons/vue/24/outline'
import StatusDot from './StatusDot.vue'
import type { Session } from '@/api/types'
import { displayCommand, displayDirectory, displayTitle } from '@/utils/session'
import { relativeTime } from '@/utils/time'

const props = defineProps<{ session: Session }>()
const emit = defineEmits<{
  (e: 'delete', id: string): void
  (e: 'restart', id: string): void
}>()

const command = computed(() => displayCommand(props.session))
const directory = computed(() => displayDirectory(props.session))
const title = computed(() => displayTitle(props.session))
const isSSH = computed(() => props.session.targetType === 'ssh')
// The header badge that names the shell for a local session has nothing to
// show there for an SSH one — directory and shell are both "" by design
// (§6) — so it names the target kind instead; the host itself gets the row
// below, in the slot the local directory otherwise fills.
const targetBadge = computed(() => (isSSH.value ? 'ssh' : props.session.shell))
</script>

<template>
  <RouterLink
    :to="`/sessions/${session.id}`"
    class="group flex flex-col gap-3 rounded-lg border border-slate-700 bg-slate-800/50 p-4 transition hover:border-slate-500 hover:bg-slate-800"
  >
    <div class="flex items-center gap-2">
      <StatusDot :status="session.status" />
      <span class="truncate font-medium text-slate-100">{{ session.name }}</span>
      <span class="ml-auto font-mono text-xs text-slate-400">{{ targetBadge }}</span>
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
      What is running in the session: the foreground program as the kernel names
      it (§4.7), and under it the title that program gave itself (§4.8). Each
      row keeps its height while it is unknown, so the grid does not reflow each
      time a session changes what it is doing.
    -->
    <div class="flex flex-col gap-0.5 text-xs">
      <span class="min-h-4 truncate font-mono text-slate-400">{{ command }}</span>
      <span class="min-h-4 truncate text-slate-500" :title="title || undefined">{{ title }}</span>
    </div>

    <div class="flex items-center gap-4 text-xs text-slate-400">
      <span v-if="isSSH" class="flex min-w-0 items-center gap-1">
        <ServerIcon class="h-4 w-4 shrink-0" />
        <span class="truncate font-mono">{{ session.hostDisplayName }}</span>
      </span>
      <span v-else class="flex min-w-0 items-center gap-1">
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
