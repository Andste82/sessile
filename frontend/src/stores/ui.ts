import { defineStore } from 'pinia'
import { ref, watch } from 'vue'

// Terminal font size (px). The range is what a slider can offer without either
// end being useless: below 8 the glyphs stop being readable, above 32 a phone
// fits barely 20 columns and the shell wraps everything.
export const minFontSize = 8
export const maxFontSize = 32
export const defaultFontSize = 13

// Kept in localStorage rather than on the server: the readable size depends on
// the screen in front of the user, so the same session opened on a phone and on
// a desktop wants two different answers.
const fontSizeKey = 'sessile.terminalFontSize'

// Copy-on-select: a mouse selection lands on the clipboard as soon as the
// button comes up. On by default, because it is what a terminal does — xterm,
// PuTTY and tmux all copy the selection — and because the chord it replaces,
// Ctrl+Shift+C, is the browser's devtools shortcut and cannot be taken from it.
// The cost is that every drag overwrites whatever was on the clipboard, which
// is why it can be switched off.
export const defaultCopyOnSelect = true
const copyOnSelectKey = 'sessile.copyOnSelect'

/**
 * parseCopyOnSelect reads a stored (or cross-tab) value as a flag. Anything
 * that is not one of the two strings we write — a corrupted entry, a cleared
 * one, a value from a future version — falls back to the default rather than
 * counting as "off", which is what the falsiness of '' or null would give.
 */
export function parseCopyOnSelect(value: unknown): boolean {
  if (value === 'true') return true
  if (value === 'false') return false
  return defaultCopyOnSelect
}

/**
 * clampFontSize maps anything — a slider value, a stored string, a corrupted
 * entry — onto a size the terminal can actually be given. Non-numeric input
 * falls back to the default instead of clamping, so an empty or garbled stored
 * value does not silently become the smallest font we allow.
 */
export function clampFontSize(value: unknown): number {
  // Deliberately not Number(value) alone: it reads null and '' as 0, which
  // would clamp to the smallest font rather than fall back.
  const n =
    typeof value === 'number'
      ? value
      : typeof value === 'string' && value.trim() !== ''
        ? Number(value)
        : NaN
  if (!Number.isFinite(n)) return defaultFontSize
  return Math.min(maxFontSize, Math.max(minFontSize, Math.round(n)))
}

// Storage is not guaranteed: a browser with cookies blocked throws on access
// rather than returning null, and losing the preference is not a reason to fail
// to build the store.
function readFontSize(): number {
  try {
    const raw = localStorage.getItem(fontSizeKey)
    return raw === null ? defaultFontSize : clampFontSize(raw)
  } catch {
    return defaultFontSize
  }
}

function readCopyOnSelect(): boolean {
  try {
    return parseCopyOnSelect(localStorage.getItem(copyOnSelectKey))
  } catch {
    return defaultCopyOnSelect
  }
}

// Small store for cross-component UI state that isn't tied to session data.
export const useUiStore = defineStore('ui', () => {
  // Whether the on-screen special-key bar is shown on the terminal (issue #10).
  const keyBarOpen = ref(false)

  function toggleKeyBar() {
    keyBarOpen.value = !keyBarOpen.value
  }

  const terminalFontSize = ref(readFontSize())

  function setTerminalFontSize(size: unknown) {
    terminalFontSize.value = clampFontSize(size)
  }

  watch(terminalFontSize, (size) => {
    try {
      localStorage.setItem(fontSizeKey, String(size))
    } catch {
      // Unwritable storage: the size still applies for this page's lifetime.
    }
  })

  const copyOnSelect = ref(readCopyOnSelect())

  function setCopyOnSelect(on: boolean) {
    copyOnSelect.value = on
  }

  watch(copyOnSelect, (on) => {
    try {
      localStorage.setItem(copyOnSelectKey, String(on))
    } catch {
      // Unwritable storage: the choice still applies for this page's lifetime.
    }
  })

  // Two tabs on the same session is a normal way to use this app — they mirror
  // each other (§5) — so a size set in one of them and not the other reads as
  // the setting not having taken. `storage` fires in every tab *except* the one
  // that wrote, which is exactly the set that needs telling.
  //
  // Applying it here writes the same string back through the watcher above.
  // That is a no-op: setItem with the value already stored neither changes the
  // entry nor notifies anyone, so this cannot bounce between tabs.
  function onStorage(e: StorageEvent) {
    // key === null is a clear(), which takes every preference with it. A
    // removeItem reports its own key with a null newValue, which both parsers
    // already read as "no usable value" and answer with their default.
    if (e.key === null) {
      terminalFontSize.value = defaultFontSize
      copyOnSelect.value = defaultCopyOnSelect
      return
    }
    if (e.key === fontSizeKey) terminalFontSize.value = clampFontSize(e.newValue)
    else if (e.key === copyOnSelectKey) copyOnSelect.value = parseCopyOnSelect(e.newValue)
  }

  // Never removed: the store lives exactly as long as the page it listens for.
  // Guarded because the store is also built in tests, where there is no window.
  if (typeof window !== 'undefined') window.addEventListener('storage', onStorage)

  return {
    keyBarOpen,
    toggleKeyBar,
    terminalFontSize,
    setTerminalFontSize,
    copyOnSelect,
    setCopyOnSelect,
  }
})
