<script setup lang="ts">
import { computed } from 'vue'
import type { Activity, Status } from '@/api/types'
import { indicatorFor, indicatorLabel } from '@/utils/activity'

// activity is optional so the component still renders for a caller that only
// has a status — a session the store has not seen an update for yet.
const props = withDefaults(defineProps<{ status: Status; activity?: Activity }>(), {
  activity: '',
})

const indicator = computed(() => indicatorFor(props.status, props.activity))
const label = computed(() => indicatorLabel(indicator.value))

// Emerald for a live session and slate for a dead one are what this component
// already meant; busy adds motion to the same green rather than a new colour,
// and waiting borrows the amber the tab bar already uses for "needs a moment".
const dotClass = computed(() => {
  switch (indicator.value) {
    case 'busy':
      return 'bg-emerald-400 shadow-[0_0_6px] shadow-emerald-400/60 animate-pulse'
    case 'idle':
      return 'bg-emerald-400 shadow-[0_0_6px] shadow-emerald-400/60'
    default:
      return 'bg-slate-500'
  }
})
</script>

<template>
  <!--
    One 10px box in every state so nothing beside it shifts when the state
    changes. Waiting is a glyph rather than a colour because it is the only
    state that asks the viewer to do something, and a fourth shade of dot is
    not a difference you notice out of the corner of your eye.
  -->
  <span
    class="inline-flex h-2.5 w-2.5 shrink-0 items-center justify-center"
    :title="label"
    :aria-label="label"
    role="img"
  >
    <span
      v-if="indicator === 'waiting'"
      class="text-[13px] leading-none font-bold text-amber-400"
      aria-hidden="true"
      >?</span
    >
    <span v-else class="h-2.5 w-2.5 rounded-full" :class="dotClass" aria-hidden="true" />
  </span>
</template>
