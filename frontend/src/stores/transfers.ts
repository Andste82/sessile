import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api } from '@/api/client'
import { onHostopEvent } from '@/composables/useHostopEvents'
import { uploadHostFile, UploadAbortedError, type UploadHandle } from '@/api/upload'

// Transfers and long-running host operations outlive the component that shows
// them, so their state cannot live in it.
//
// FileExplorerPanel is unmounted twice over in normal use: switching to the
// Processes tab (`v-if` in FileBrowserPanel) and closing the panel (`v-if` in
// TerminalPage) both destroy it. An upload keeps running regardless — the XHR
// is never aborted — and a Delete/Copy runs as a server-side goroutine that
// doesn't know the client exists. Keeping their state in component refs meant
// a running upload silently lost its progress bar, and a finished Delete
// delivered its hostopDone to a listener that no longer existed, so the
// listing stayed stale.
//
// Keyed by session id: two sessions can each have work in flight, and each
// panel only ever shows its own.

export interface UploadState {
  name: string
  loaded: number
  total: number
  status: 'running' | 'error' | 'cancelled'
}

export interface OpState {
  opId: string
  kind: 'delete' | 'copy'
  entryName: string
  done: number
  total: number
  status: 'running' | 'ok' | 'error'
  message: string
}

// How long a settled upload or operation stays on screen before clearing
// itself. Long enough to read "done" or "failed", short enough not to sit
// there.
const settledLingerMs = 1200

export const useTransfersStore = defineStore('transfers', () => {
  const uploads = ref<Record<string, UploadState | undefined>>({})
  const ops = ref<Record<string, OpState | undefined>>({})

  // Abort handles and generation counters are deliberately outside the
  // reactive state: they are neither rendered nor comparable, and a function
  // in a ref only invites accidental deep-reactivity work.
  const handles = new Map<string, UploadHandle>()
  const generations = new Map<string, number>()

  function generation(sessionId: string): number {
    return generations.get(sessionId) ?? 0
  }

  function bumpGeneration(sessionId: string): number {
    const next = generation(sessionId) + 1
    generations.set(sessionId, next)
    return next
  }

  // One module-level subscription, not one per component. The fan-out in
  // useHostopEvents is fed by the shared /ws/events connection and has no
  // lifecycle of its own, so this stays live while every panel comes and goes.
  onHostopEvent((e) => {
    const op = ops.value[e.sessionId]
    if (!op) return
    if (e.type === 'hostopStarted') {
      // Adopt the id from the event rather than waiting for the HTTP response:
      // a fast local operation can publish started/progress/done before the
      // response resolves, and an op that never learns its id can never match
      // the events that follow.
      if (!op.opId) op.opId = e.opId
      return
    }
    if (e.opId !== op.opId) return
    if (e.type === 'hostopProgress') {
      op.done = e.done
      op.total = e.total
    } else if (e.type === 'hostopDone') {
      settleOp(e.sessionId, e.status === 'error' ? 'error' : 'ok', e.message)
    }
  })

  function settleOp(sessionId: string, status: 'ok' | 'error', message: string) {
    const op = ops.value[sessionId]
    if (!op || op.status !== 'running') return
    op.status = status
    op.message = message
    const gen = generation(sessionId)
    setTimeout(() => {
      // Only clear if nothing newer has started meanwhile — an op that never
      // received a hostopStarted still has an empty opId, and so would a
      // freshly started one, so the id is not usable as the identity here.
      if (gen === generation(sessionId) && ops.value[sessionId]?.status !== 'running') {
        ops.value[sessionId] = undefined
      }
    }, settledLingerMs)
  }

  /**
   * startOp registers a Delete/Copy before its request is sent, so the events
   * it publishes have somewhere to land even if they arrive first. `run` is
   * the API call; it reports the opId the server assigned.
   */
  async function startOp(
    sessionId: string,
    kind: 'delete' | 'copy',
    entryName: string,
    run: () => Promise<{ opId: string }>,
  ): Promise<void> {
    const gen = bumpGeneration(sessionId)
    ops.value[sessionId] = { opId: '', kind, entryName, done: 0, total: 0, status: 'running', message: '' }
    try {
      const { opId } = await run()
      if (gen !== generation(sessionId)) return
      const op = ops.value[sessionId]
      if (op && !op.opId) op.opId = opId
      void pollOpFallback(sessionId, gen, opId)
    } catch (err) {
      if (gen === generation(sessionId)) ops.value[sessionId] = undefined
      throw err
    }
  }

  // Poll fallback for the case the socket is down, or was closed and reopened
  // in the window between hostopStarted and hostopDone. One check after a
  // grace period, not a loop: the WS path is primary.
  async function pollOpFallback(sessionId: string, gen: number, opId: string) {
    await new Promise((resolve) => setTimeout(resolve, 1500))
    if (gen !== generation(sessionId)) return
    const op = ops.value[sessionId]
    if (!op || op.opId !== opId || op.status !== 'running') return
    try {
      const status = await api.hostopStatus(sessionId, opId)
      if (status.status === 'running') return
      settleOp(sessionId, status.status === 'error' ? 'error' : 'ok', status.message ?? '')
    } catch {
      // The op may already be retired server-side (§5.2's retention window),
      // or the poll itself failed transiently. The WS path remains the source
      // of truth either way.
    }
  }

  /**
   * startUpload begins a transfer and tracks it. Resolves true when the file
   * landed, false when it was cancelled; rejects on a real failure.
   */
  async function startUpload(sessionId: string, path: string, file: File): Promise<boolean> {
    const name = file.name
    uploads.value[sessionId] = { name, loaded: 0, total: file.size, status: 'running' }

    const handle = uploadHostFile(sessionId, path, file, (loaded, total) => {
      const state = uploads.value[sessionId]
      if (state?.name === name && state.status === 'running') {
        state.loaded = loaded
        state.total = total
      }
    })
    handles.set(sessionId, handle)

    try {
      await handle.promise
      if (uploads.value[sessionId]?.name === name) uploads.value[sessionId] = undefined
      return true
    } catch (err) {
      const cancelled = err instanceof UploadAbortedError
      const state = uploads.value[sessionId]
      if (state?.name === name) {
        state.status = cancelled ? 'cancelled' : 'error'
        // Clear on a timer so the toolbar's Upload button doesn't stay
        // disabled. The panel's own error line, set separately, keeps the
        // failure readable after the banner is gone.
        setTimeout(() => {
          const later = uploads.value[sessionId]
          if (later?.name === name && later.status !== 'running') uploads.value[sessionId] = undefined
        }, settledLingerMs)
      }
      if (cancelled) return false
      throw err
    } finally {
      handles.delete(sessionId)
    }
  }

  /** cancelUpload aborts the in-flight transfer for a session, if there is one. */
  function cancelUpload(sessionId: string) {
    handles.get(sessionId)?.abort()
  }

  function uploadFor(sessionId: string): UploadState | undefined {
    return uploads.value[sessionId]
  }

  function opFor(sessionId: string): OpState | undefined {
    return ops.value[sessionId]
  }

  return { uploads, ops, startOp, startUpload, cancelUpload, uploadFor, opFor }
})
