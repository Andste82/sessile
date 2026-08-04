<script setup lang="ts">
import { onMounted } from 'vue'
import { useSessionsStore } from '@/stores/sessions'
import { useUiStore, minFontSize, maxFontSize, defaultFontSize } from '@/stores/ui'

const store = useSessionsStore()
const ui = useUiStore()

onMounted(() => {
  // Deliberately not awaited: the page renders while the config loads.
  if (!store.config) void store.fetchConfig()
})

// The store clamps, so the buttons can step past either end without checking —
// they only need to disable themselves so the edge is visible.
function stepFontSize(by: number) {
  ui.setTerminalFontSize(ui.terminalFontSize + by)
}

function onFontSizeInput(e: Event) {
  ui.setTerminalFontSize((e.target as HTMLInputElement).value)
}

const stepBtn =
  'flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-slate-700 bg-slate-800 text-lg leading-none text-slate-200 hover:bg-slate-700 disabled:opacity-40 disabled:hover:bg-slate-800'
</script>

<template>
  <div class="flex h-full flex-col">
    <header class="border-b border-slate-800 bg-slate-900 px-4 py-4 sm:px-6">
      <h1 class="text-lg font-semibold tracking-tight">Settings</h1>
    </header>

    <main class="mx-auto w-full max-w-2xl flex-1 overflow-y-auto p-4 sm:p-6">
      <section class="mb-4 rounded-lg border border-slate-700 bg-slate-800/50 p-6">
        <h2 class="mb-4 text-sm font-medium uppercase tracking-wide text-slate-400">
          Terminal
        </h2>

        <div class="flex items-baseline justify-between">
          <label for="font-size" class="text-sm text-slate-200">Font size</label>
          <span class="font-mono text-sm text-slate-400">{{ ui.terminalFontSize }} px</span>
        </div>

        <div class="mt-3 flex items-center gap-3">
          <button
            type="button"
            :class="stepBtn"
            :disabled="ui.terminalFontSize <= minFontSize"
            aria-label="Smaller font"
            @click="stepFontSize(-1)"
          >
            −
          </button>
          <!-- Native appearance, tinted with accent-color: styling the track
               means restyling the thumb in three vendor pseudo-elements, and a
               half-styled range renders without one. -->
          <input
            id="font-size"
            type="range"
            class="min-w-0 flex-1 cursor-pointer accent-emerald-400"
            :min="minFontSize"
            :max="maxFontSize"
            step="1"
            :value="ui.terminalFontSize"
            @input="onFontSizeInput"
          />
          <button
            type="button"
            :class="stepBtn"
            :disabled="ui.terminalFontSize >= maxFontSize"
            aria-label="Larger font"
            @click="stepFontSize(1)"
          >
            +
          </button>
        </div>

        <!-- A sample in the terminal's own palette: the point of the setting is
             what the text looks like, and the Settings page is not where the
             terminal is. -->
        <div class="mt-4 overflow-x-auto rounded-md border border-slate-700 bg-slate-900 p-3">
          <pre
            class="font-mono leading-snug text-slate-200"
            :style="{ fontSize: ui.terminalFontSize + 'px' }"
          ><span class="text-emerald-400">~</span> echo "the quick brown fox 0123"</pre>
        </div>

        <div class="mt-3 flex items-center justify-between gap-3">
          <p class="text-xs text-slate-500">
            Applies to open terminals, and is remembered on this device.
          </p>
          <button
            type="button"
            class="shrink-0 text-xs text-slate-400 underline-offset-2 hover:text-slate-200 hover:underline disabled:opacity-40 disabled:no-underline"
            :disabled="ui.terminalFontSize === defaultFontSize"
            @click="ui.setTerminalFontSize(defaultFontSize)"
          >
            Reset
          </button>
        </div>
      </section>

      <section class="rounded-lg border border-slate-700 bg-slate-800/50 p-6">
        <h2 class="mb-4 text-sm font-medium uppercase tracking-wide text-slate-400">
          Server configuration
        </h2>
        <dl v-if="store.config" class="grid grid-cols-[7rem_1fr] gap-y-3 text-sm">
          <dt class="text-slate-400">Root</dt>
          <dd class="break-all font-mono text-slate-200">{{ store.config.root }}</dd>
          <dt class="text-slate-400">Shells</dt>
          <dd class="font-mono text-slate-200">{{ store.config.shells.join(', ') }}</dd>
          <dt class="text-slate-400">Version</dt>
          <!-- A release reports its tag; any other build reports the commit it
               was made from, which is long enough to need wrapping. -->
          <dd class="break-all font-mono text-slate-200">{{ store.config.version }}</dd>
        </dl>
        <!-- Without the error branch this said "Loading…" forever whenever the
             config request failed. -->
        <p v-else-if="store.error" class="text-sm text-rose-400">{{ store.error }}</p>
        <p v-else class="text-sm text-slate-500">Loading…</p>
      </section>

      <p class="mt-4 text-xs text-slate-500">
        Server configuration is read-only and set via server flags /
        environment variables.
      </p>
    </main>
  </div>
</template>
