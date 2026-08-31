<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useSessionsStore } from '@/stores/sessions'
import { useAuthStore } from '@/stores/auth'
import { useUiStore, minFontSize, maxFontSize, defaultFontSize } from '@/stores/ui'
import { isApplePlatform } from '@/utils/clipboard'
import { api } from '@/api/client'
import type { AdminConfig } from '@/api/types'

const store = useSessionsStore()
const auth = useAuthStore()
const ui = useUiStore()

const adminConfig = ref<AdminConfig | null>(null)
const adminLoadError = ref<string | null>(null)
const adminSaving = ref(false)
const adminSaveError = ref<string | null>(null)
const adminSaved = ref(false)

async function loadAdminConfig() {
  if (!auth.user?.isAdmin) return
  try {
    adminConfig.value = await api.getAdminConfig()
  } catch (e) {
    adminLoadError.value = e instanceof Error ? e.message : String(e)
  }
}

async function saveAdminConfig() {
  if (!adminConfig.value || adminSaving.value) return
  adminSaving.value = true
  adminSaveError.value = null
  adminSaved.value = false
  try {
    adminConfig.value = await api.updateAdminConfig(adminConfig.value)
    adminSaved.value = true
  } catch (e) {
    adminSaveError.value = e instanceof Error ? e.message : String(e)
  } finally {
    adminSaving.value = false
  }
}

// The help below names the keys this browser actually listens for, so it is
// worth knowing which platform we are on: the copy and paste chords are Cmd on
// Apple keyboards and Ctrl everywhere else (see utils/clipboard.ts).
const apple = isApplePlatform(navigator)

// Each row: what you want to do, the keys that do it, and — where a key needs
// one — the caveat a beginner would otherwise have to discover by being bitten.
const clipboardHelp = computed(() => [
  {
    what: 'Copy',
    keys: apple ? ['⌘ C'] : ['Ctrl + Shift + C', 'Ctrl + Insert'],
    note: ui.copyOnSelect
      ? 'Or just select the text with the mouse — copy on select is on.'
      : 'Right-click offers Copy too.',
  },
  {
    what: 'Paste',
    keys: apple ? ['⌘ V'] : ['Ctrl + V', 'Shift + Insert'],
    note: 'On a phone, use the keyboard’s own paste button.',
  },
  {
    what: 'Interrupt',
    // Ctrl even on Apple keyboards: this one is the terminal's, not the
    // system's, and ⌘C is the copy above.
    keys: ['Ctrl + C'],
    note: 'Always stops the running program. It never copies, selection or not.',
  },
])

const kbd =
  'rounded border border-slate-700 bg-slate-900 px-1.5 py-0.5 font-mono text-xs text-slate-200'

onMounted(() => {
  // Deliberately not awaited: the page renders while the config loads.
  if (!store.config) void store.fetchConfig()
  void loadAdminConfig()
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

      <section class="mb-4 rounded-lg border border-slate-700 bg-slate-800/50 p-6">
        <h2 class="mb-4 text-sm font-medium uppercase tracking-wide text-slate-400">
          Copy &amp; paste
        </h2>

        <label class="flex cursor-pointer items-start gap-3">
          <input
            type="checkbox"
            class="mt-0.5 h-4 w-4 shrink-0 cursor-pointer accent-emerald-400"
            :checked="ui.copyOnSelect"
            @change="ui.setCopyOnSelect(($event.target as HTMLInputElement).checked)"
          />
          <span>
            <span class="text-sm text-slate-200">Copy on select</span>
            <span class="mt-1 block text-xs text-slate-500">
              Text you select with the mouse goes to the clipboard straight
              away, so copying needs no key at all. It replaces whatever was on
              the clipboard before.
            </span>
          </span>
        </label>

        <!-- The short version of the terminal clipboard rules. Two of them
             surprise everybody the first time: Ctrl+C does not copy, and
             Ctrl+Shift+C is the browser's own shortcut. -->
        <dl class="mt-5 grid grid-cols-[5.5rem_1fr] items-baseline gap-x-4 gap-y-3 text-sm">
          <template v-for="row in clipboardHelp" :key="row.what">
            <dt class="text-slate-400">{{ row.what }}</dt>
            <dd>
              <span class="flex flex-wrap items-center gap-x-2 gap-y-1">
                <template v-for="(key, i) in row.keys" :key="key">
                  <span v-if="i > 0" class="text-xs text-slate-500">or</span>
                  <kbd :class="kbd">{{ key }}</kbd>
                </template>
              </span>
              <span class="mt-1 block text-xs text-slate-500">{{ row.note }}</span>
            </dd>
          </template>
        </dl>

        <p v-if="!apple" class="mt-4 text-xs text-slate-500">
          Heads-up: <kbd :class="kbd">Ctrl + Shift + C</kbd> also opens the
          browser’s developer tools, and no page can stop it. The text is
          copied anyway — but copy on select and
          <kbd :class="kbd">Ctrl + Insert</kbd> keep the devtools out of it.
        </p>
      </section>

      <section class="rounded-lg border border-slate-700 bg-slate-800/50 p-6">
        <h2 class="mb-4 text-sm font-medium uppercase tracking-wide text-slate-400">
          Server configuration
        </h2>
        <dl v-if="store.config" class="grid grid-cols-[7rem_1fr] gap-y-3 text-sm">
          <dt class="text-slate-400">Local host</dt>
          <dd class="font-mono text-slate-200">
            {{ store.config.allowLocalHost ? 'Enabled' : 'Disabled' }}
          </dd>
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
        The section above is read-only and set via server flags / environment
        variables{{ auth.user?.isAdmin ? ' — the settings below are the exception.' : '.' }}
      </p>

      <section
        v-if="auth.user?.isAdmin"
        class="mt-4 rounded-lg border border-slate-700 bg-slate-800/50 p-6"
      >
        <h2 class="mb-4 text-sm font-medium uppercase tracking-wide text-slate-400">
          Admin
        </h2>

        <form v-if="adminConfig" class="flex flex-col gap-4" @submit.prevent="saveAdminConfig">
          <label class="flex flex-col gap-1 text-sm">
            <span class="text-slate-400">Display name</span>
            <input
              v-model="adminConfig.displayName"
              type="text"
              maxlength="64"
              placeholder="sessile"
              class="rounded-md border border-slate-600 bg-slate-900 px-3 py-2 text-slate-100 outline-none focus:border-emerald-500"
            />
            <span class="text-xs text-slate-500">Shown on the login page. Empty uses the generic title.</span>
          </label>

          <label class="flex cursor-pointer items-start gap-3">
            <input
              v-model="adminConfig.allowRegistration"
              type="checkbox"
              class="mt-0.5 h-4 w-4 shrink-0 cursor-pointer accent-emerald-400"
            />
            <span>
              <span class="text-sm text-slate-200">Allow self-service registration</span>
              <span class="mt-1 block text-xs text-slate-500">
                Lets anyone create their own account from the login page. Off by default.
              </span>
            </span>
          </label>

          <label class="flex cursor-pointer items-start gap-3">
            <input
              v-model="adminConfig.allowLocalHost"
              type="checkbox"
              class="mt-0.5 h-4 w-4 shrink-0 cursor-pointer accent-emerald-400"
            />
            <span>
              <span class="text-sm text-slate-200">Allow sessions on this host</span>
              <span class="mt-1 block text-xs text-slate-500">
                Lets any logged-in user open a local shell on the server itself, in addition to
                their own SSH hosts. Off by default.
              </span>
            </span>
          </label>

          <p v-if="adminSaveError" class="text-sm text-rose-400">{{ adminSaveError }}</p>

          <div class="mt-1 flex items-center gap-3">
            <button
              type="submit"
              :disabled="adminSaving"
              class="rounded-md bg-emerald-600 px-4 py-2 text-sm font-medium text-white hover:bg-emerald-500 disabled:cursor-not-allowed disabled:opacity-50"
            >
              {{ adminSaving ? 'Saving…' : 'Save' }}
            </button>
            <span v-if="adminSaved" class="text-sm text-slate-500">Saved.</span>
          </div>
        </form>
        <p v-else-if="adminLoadError" class="text-sm text-rose-400">{{ adminLoadError }}</p>
        <p v-else class="text-sm text-slate-500">Loading…</p>
      </section>
    </main>
  </div>
</template>
