<script setup lang="ts">
import { computed } from 'vue'
import type { Status } from '@/api/types'

const props = defineProps<{ status: Status }>()

const cls = computed(() =>
  props.status === 'running'
    ? 'bg-emerald-400 shadow-[0_0_6px] shadow-emerald-400/60'
    : 'bg-slate-500',
)
</script>

<template>
  <span
    class="inline-block h-2.5 w-2.5 shrink-0 rounded-full"
    :class="cls"
    :title="status"
    :aria-label="status"
  />
</template>
