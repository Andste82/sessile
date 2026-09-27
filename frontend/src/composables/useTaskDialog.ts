import { ref } from 'vue'

// The task form (§4.12.1) is opened from several places — the sidebar's
// "New task", the dashboard, the new-session dialog — and mounted once in
// AppShell. This is the one flag they share.
const open = ref(false)

export function useTaskDialog() {
  return {
    open,
    show: () => {
      open.value = true
    },
    hide: () => {
      open.value = false
    },
  }
}
