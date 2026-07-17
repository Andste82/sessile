import { defineStore } from 'pinia'
import { ref } from 'vue'

// Small store for cross-component UI state that isn't tied to session data.
export const useUiStore = defineStore('ui', () => {
  // Whether the on-screen special-key bar is shown on the terminal (issue #10).
  const keyBarOpen = ref(false)

  function toggleKeyBar() {
    keyBarOpen.value = !keyBarOpen.value
  }

  return { keyBarOpen, toggleKeyBar }
})
