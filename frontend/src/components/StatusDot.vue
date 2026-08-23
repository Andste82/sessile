<script setup lang="ts">
import { computed } from 'vue'
import type { Status } from '@/api/types'

const props = defineProps<{ status: Status }>()

const label = computed(() => (props.status === 'running' ? 'running' : 'stopped'))

// Emerald for a live session, slate for a dead one.
const dotClass = computed(() =>
  props.status === 'running'
    ? 'bg-emerald-400 shadow-[0_0_6px] shadow-emerald-400/60'
    : 'bg-slate-500',
)
</script>

<template>
  <!--
    One 10px box in every state so nothing beside it shifts when the state
    changes.
  -->
  <span
    class="inline-flex h-2.5 w-2.5 shrink-0 items-center justify-center"
    :title="label"
    :aria-label="label"
    role="img"
  >
    <span class="h-2.5 w-2.5 rounded-full" :class="dotClass" aria-hidden="true" />
  </span>
</template>
