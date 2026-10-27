import { defineStore } from 'pinia'
import { ref } from 'vue'

export type ToastKind = 'info' | 'warning' | 'error'

export interface Toast {
  id: number
  message: string
  kind: ToastKind
}

// Transient, app-wide notices (e.g. the warnings a create/restart response
// carries). In memory only: a toast is never persisted, and a message must
// never carry a secret value — callers pass server-supplied text, which names
// entries and line numbers only.
export const useToastsStore = defineStore('toasts', () => {
  const toasts = ref<Toast[]>([])
  const timers = new Map<number, ReturnType<typeof setTimeout>>()
  let nextId = 1

  function dismiss(id: number) {
    const t = timers.get(id)
    if (t !== undefined) clearTimeout(t)
    timers.delete(id)
    toasts.value = toasts.value.filter((x) => x.id !== id)
  }

  // ttlMs <= 0 keeps the toast until it is dismissed by hand.
  function push(message: string, kind: ToastKind = 'info', ttlMs = 8000): number {
    const id = nextId++
    toasts.value = [...toasts.value, { id, message, kind }]
    if (ttlMs > 0) timers.set(id, setTimeout(() => dismiss(id), ttlMs))
    return id
  }

  return { toasts, push, dismiss }
})
