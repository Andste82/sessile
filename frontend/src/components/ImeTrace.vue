<script setup lang="ts">
import { ref } from 'vue'
import type { ImeTrace } from '@/utils/imeTrace'

// Diagnostics for issue #82, shown only when the page was opened with
// ?debug=ime — see utils/imeTrace.ts for what a trace is meant to answer.
//
// It is a panel with a copy button rather than a live readout like the scroll
// overlay was: this fault is a sequence, not a number, and it has to leave the
// phone to be read. Everything is one tap away because the device that has the
// bug has no console and an awkward keyboard.
const props = defineProps<{ trace: ImeTrace }>()

const open = ref(true)
const copied = ref(false)
const text = ref('')

function refresh() {
  text.value = props.trace.format()
}

async function copy() {
  refresh()
  try {
    await navigator.clipboard.writeText(text.value)
    copied.value = true
    setTimeout(() => (copied.value = false), 1500)
  } catch {
    // Clipboard permission is not a given on mobile; the text is on screen and
    // selectable either way, which is the fallback that always works.
    copied.value = false
  }
}

function clear() {
  props.trace.clear()
  refresh()
}

refresh()
</script>

<template>
  <div
    class="absolute inset-x-1 bottom-1 z-20 rounded border border-emerald-700 bg-slate-950/95 text-[10px] text-emerald-300 shadow-lg"
  >
    <div class="flex items-center gap-2 border-b border-emerald-900 px-2 py-1">
      <span class="font-mono font-bold">ime trace</span>
      <button class="rounded px-2 py-0.5 hover:bg-slate-800" @click="refresh">refresh</button>
      <button class="rounded px-2 py-0.5 hover:bg-slate-800" @click="copy">
        {{ copied ? 'copied' : 'copy' }}
      </button>
      <button class="rounded px-2 py-0.5 hover:bg-slate-800" @click="clear">clear</button>
      <button class="ml-auto rounded px-2 py-0.5 hover:bg-slate-800" @click="open = !open">
        {{ open ? 'hide' : 'show' }}
      </button>
    </div>
    <!-- select-text, because copying via the clipboard API may be refused and
         selecting the log by hand has to stay possible. -->
    <pre
      v-if="open"
      class="max-h-48 touch-auto overflow-auto px-2 py-1 font-mono leading-tight break-all whitespace-pre-wrap select-text"
      >{{ text }}</pre
    >
  </div>
</template>
