import { onBeforeUnmount, ref } from 'vue'

// Whether there is room for two terminals side by side (§4.12, the task
// page's split). Tailwind's `md` is 768px; below it the panes become tabs.
const splitQuery = '(min-width: 768px)'

export function useWideScreen() {
  const wide = ref(window.matchMedia?.(splitQuery).matches ?? true)
  const mq = window.matchMedia?.(splitQuery)
  const onChange = (e: MediaQueryListEvent) => {
    wide.value = e.matches
  }
  mq?.addEventListener?.('change', onChange)
  onBeforeUnmount(() => mq?.removeEventListener?.('change', onChange))
  return wide
}
