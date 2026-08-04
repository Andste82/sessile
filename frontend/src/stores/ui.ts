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

  return { keyBarOpen, toggleKeyBar, terminalFontSize, setTerminalFontSize }
})
