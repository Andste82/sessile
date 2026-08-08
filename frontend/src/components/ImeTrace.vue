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

// --- two experiments, to decide why the keyboard sends no space at all -------
//
// The device trace showed a glided word arriving as one insertText with no
// composition around it and no space anywhere — so there is no event being
// lost. The question is why Gboard does not produce one, and there are two
// candidates. Both act on xterm's helper textarea and neither changes how input
// is delivered, so they are safe to flip mid-session.
//
// ctx  — put text back in front of the cursor. A keyboard decides whether a
//        word needs a leading space by asking the editor what precedes it, and
//        that buffer is emptied after every word, so it always answers "start
//        of field". If the next word then arrives as " word", this is it.
// sugg — turn the field's text-assistance hints back on. spellcheck="false"
//        becomes TYPE_TEXT_FLAG_NO_SUGGESTIONS on Android, which makes Gboard
//        treat it as a code field and drop auto-spacing. If the space appears
//        with this on, that is it — and the real fix is not simply leaving
//        suggestions on, since a terminal must not autocorrect `ls`.
const suggest = ref(false)

function textarea(): HTMLTextAreaElement | null {
  return document.querySelector('.xterm-helper-textarea')
}

function seedContext() {
  const ta = textarea()
  if (!ta) return
  ta.value = 'hello'
  ta.setSelectionRange(ta.value.length, ta.value.length)
  ta.focus()
  refresh()
}

function toggleSuggest() {
  const ta = textarea()
  if (!ta) return
  suggest.value = !suggest.value
  if (suggest.value) {
    ta.setAttribute('spellcheck', 'true')
    ta.setAttribute('autocorrect', 'on')
    ta.setAttribute('autocapitalize', 'sentences')
  } else {
    ta.setAttribute('spellcheck', 'false')
    ta.setAttribute('autocorrect', 'off')
    ta.setAttribute('autocapitalize', 'off')
  }
  // The keyboard reads these when the field takes focus, so bounce it.
  ta.blur()
  ta.focus()
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
      <button
        class="rounded px-2 py-0.5 hover:bg-slate-800"
        title="Put text before the cursor, then glide a word"
        @click="seedContext"
      >
        ctx
      </button>
      <button
        class="rounded px-2 py-0.5 hover:bg-slate-800"
        :class="suggest ? 'bg-emerald-800 text-emerald-100' : ''"
        title="Turn the field's text-assistance hints on or off"
        @click="toggleSuggest"
      >
        sugg{{ suggest ? '+' : '-' }}
      </button>
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
