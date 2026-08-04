<script setup lang="ts">
import type { ScrollDebug } from '@/composables/useTerminal'

// Diagnostics for issue #64, shown only when the page was opened with
// ?debug=scroll. The bug survives on a phone, where there is no console to read
// and no way to reproduce it on purpose, so the numbers a touch scroll depends
// on are put on the screen next to the terminal that is getting them wrong.
//
// What to read, when a swipe moves one line instead of tracking the finger:
//   pitch far from the row height (~font size + a little) -> we are measuring
//     the terminal wrongly, and every drag is divided by the wrong number.
//   asked ≠ moved -> xterm refused the scroll: the top or bottom of the
//     backlog, or the alternate screen, which has none.
//   unasked > 0 -> something other than this gesture moved the buffer, which
//     is what an undone scroll looks like.
//   bytes large -> the session was writing during the swipe, so suspect load
//     rather than the gesture.
defineProps<{ stats: ScrollDebug }>()
</script>

<template>
  <div
    class="pointer-events-none absolute right-1 top-1 z-10 rounded bg-slate-950/85 px-2 py-1 font-mono text-[10px] leading-tight text-emerald-300 shadow"
  >
    <div>rows {{ stats.rows }} · screen {{ stats.screenH }}px</div>
    <div>pitch {{ stats.pitch }}px</div>
    <div>ydisp {{ stats.ydisp }} / {{ stats.ybase }}</div>
    <div>moves {{ stats.moves }} · drag {{ stats.dragPx }}px</div>
    <div>asked {{ stats.linesAsked }} · moved {{ stats.linesMoved }}</div>
    <div :class="stats.unasked > 0 ? 'text-rose-400' : ''">
      unasked {{ stats.unasked }} · bytes {{ stats.bytes }}
    </div>
  </div>
</template>
