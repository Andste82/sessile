<script setup lang="ts">
import type { Mods, ModName, SpecialKey } from '@/utils/keys'

defineProps<{ mods: Mods }>()
const emit = defineEmits<{
  (e: 'mod', name: ModName): void
  (e: 'key', key: SpecialKey): void
}>()

// Sticky modifiers, highlighted while armed.
const modifiers: { label: string; name: ModName }[] = [
  { label: 'Ctrl', name: 'ctrl' },
  { label: 'Alt', name: 'alt' },
  { label: 'Shift', name: 'shift' },
]

const primary: { label: string; key: SpecialKey }[] = [
  { label: 'Esc', key: 'Escape' },
  { label: 'Tab', key: 'Tab' },
]

const arrows: { label: string; key: SpecialKey }[] = [
  { label: '←', key: 'Left' },
  { label: '↑', key: 'Up' },
  { label: '↓', key: 'Down' },
  { label: '→', key: 'Right' },
]

const nav: { label: string; key: SpecialKey }[] = [
  { label: 'Home', key: 'Home' },
  { label: 'End', key: 'End' },
  { label: 'PgUp', key: 'PageUp' },
  { label: 'PgDn', key: 'PageDown' },
  { label: 'Del', key: 'Delete' },
]

const fKeys: SpecialKey[] = [
  'F1',
  'F2',
  'F3',
  'F4',
  'F5',
  'F6',
  'F7',
  'F8',
  'F9',
  'F10',
  'F11',
  'F12',
]

// Text labels, not symbols — tried Unicode key-cap glyphs (⇥ ⇧ ⇱ ⇲ ⇞ ⇟ ⌦)
// first, but they rendered as unknown-glyph boxes on a real Android device:
// this component's font stack (style.css's global sans stack) isn't the one
// this project bundled Noto Sans Symbols for (that's scoped to the terminal
// buffer only, for issue #46), so there is no guarantee those codepoints are
// covered on an arbitrary phone. Text is the safe choice.
//
// w-10, not padding: every button in the row is this one width regardless
// of its label's length — padding alone only makes each button as wide as
// its own content demands, which is what made "Shift" and "→" different
// sizes before. text-xs is small enough that "Shift" and "PgDn" (the widest
// labels) still fit this width without wrapping, which is what keeps the
// row narrow enough to reach past Del without scrolling. F-keys are still a
// swipe away on the right for the rare time one is needed.
const btn =
  'w-10 shrink-0 rounded-md border border-slate-700 bg-slate-800 py-2 text-center text-xs font-medium text-slate-200 active:bg-slate-700'
</script>

<template>
  <!-- pointerdown.prevent keeps focus in the terminal so the soft keyboard
       stays open while sending keys. -->
  <div class="select-none border-t border-slate-800 bg-slate-950/95">
    <div class="flex items-stretch gap-1 overflow-x-auto p-1.5">
      <button
        v-for="k in primary"
        :key="k.key"
        type="button"
        :class="btn"
        @pointerdown.prevent="emit('key', k.key)"
      >
        {{ k.label }}
      </button>

      <span class="w-px shrink-0 self-stretch bg-slate-700" />

      <button
        v-for="m in modifiers"
        :key="m.name"
        type="button"
        class="w-10 shrink-0 rounded-md border py-2 text-center text-xs font-medium"
        :class="
          mods[m.name]
            ? 'border-emerald-500 bg-emerald-600 text-white'
            : 'border-slate-700 bg-slate-800 text-slate-200 active:bg-slate-700'
        "
        @pointerdown.prevent="emit('mod', m.name)"
      >
        {{ m.label }}
      </button>

      <span class="w-px shrink-0 self-stretch bg-slate-700" />

      <button
        v-for="k in arrows"
        :key="k.key"
        type="button"
        :class="btn"
        @pointerdown.prevent="emit('key', k.key)"
      >
        {{ k.label }}
      </button>

      <span class="w-px shrink-0 self-stretch bg-slate-700" />

      <button
        v-for="k in nav"
        :key="k.key"
        type="button"
        :class="btn"
        @pointerdown.prevent="emit('key', k.key)"
      >
        {{ k.label }}
      </button>

      <span class="w-px shrink-0 self-stretch bg-slate-700" />

      <button
        v-for="k in fKeys"
        :key="k"
        type="button"
        :class="btn"
        @pointerdown.prevent="emit('key', k)"
      >
        {{ k }}
      </button>
    </div>
  </div>
</template>
