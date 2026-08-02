import { ref, shallowRef } from 'vue'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebLinksAddon } from '@xterm/addon-web-links'
import { encodeResize, parseControl, sessionWsURL } from '@/api/wsProtocol'
import {
  anyMod,
  applyModifiers,
  encodeSpecial,
  noMods,
  type Mods,
  type ModName,
  type SpecialKey,
} from '@/utils/keys'

export type ConnStatus = 'connecting' | 'connected' | 'exited' | 'disconnected'

// Dark theme matching the app palette (slate).
const theme = {
  background: '#0f172a',
  foreground: '#e2e8f0',
  cursor: '#34d399',
  selectionBackground: '#334155',
}

/**
 * useTerminal owns an xterm.js Terminal and its WebSocket connection for a
 * single session. It streams binary PTY bytes verbatim (no client-side
 * emulation), sends keystrokes as binary and resize as JSON control frames,
 * and resets the terminal on (re)attach so the ring-buffer replay renders
 * cleanly (§5, §7), and reconnects automatically with exponential backoff.
 */
export function useTerminal() {
  const status = ref<ConnStatus>('connecting')
  const term = shallowRef<Terminal | null>(null)

  let fit: FitAddon | null = null
  let ws: WebSocket | null = null
  let observer: ResizeObserver | null = null
  let hostEl: HTMLElement | null = null
  const encoder = new TextEncoder()
  let disposed = false

  // Touch scrolling (§ mobile): xterm appends .xterm-screen after
  // .xterm-viewport, so the screen layer sits on top and the viewport never
  // sees the touch — the backlog cannot scroll natively. We drive it ourselves
  // from a one-finger drag and preventDefault() every move, which is what keeps
  // browser gestures (pull-to-refresh, back-swipe, overscroll bounce) from
  // firing mid-scroll. That requires non-passive listeners; the matching
  // `touch-action: none` lives in style.css.
  let touching = false
  let touchLastY = 0
  let touchAccum = 0 // sub-line remainder in px, so drags track the finger 1:1
  let velocity = 0 // px/ms, smoothed — drives the flick momentum
  let lastMoveAt = 0
  let momentumFrame: number | null = null

  // Momentum tuning: decay per 60fps frame, and the speed below which a flick
  // is considered finished.
  const momentumDecay = 0.94
  const momentumMinVelocity = 0.02
  // A finger that rested before lifting is a drag, not a flick.
  const flickMaxIdleMs = 100

  // Armed modifiers for the on-screen key bar (issue #10). Sticky until the
  // next key is sent, then cleared — so "tap Ctrl, then C" yields Ctrl-C.
  const mods = ref<Mods>({ ...noMods })

  function toggleMod(name: ModName) {
    mods.value = { ...mods.value, [name]: !mods.value[name] }
  }

  function clearMods() {
    if (anyMod(mods.value)) mods.value = { ...noMods }
  }

  function send(data: string) {
    if (ws && ws.readyState === WebSocket.OPEN) ws.send(encoder.encode(data))
  }

  // pressSpecial sends a named special key with the currently armed modifiers
  // applied, then clears them.
  function pressSpecial(key: SpecialKey) {
    send(encodeSpecial(key, mods.value))
    clearMods()
    term.value?.focus()
  }

  // Reconnect state (§7): exponential backoff 1s → 2s → 4s → … → max 15s.
  let sessionId = ''
  let reconnectAttempts = 0
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null
  const backoffSteps = [1000, 2000, 4000, 8000, 15000]

  // WS close code the server sends for a missing/stopped session (§5). On this
  // we stop retrying — the shell is gone (e.g. after a backend restart).
  const closeSessionUnavailable = 4404

  function open(el: HTMLElement) {
    const t = new Terminal({
      scrollback: 5000,
      cursorBlink: true,
      fontFamily:
        'ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, monospace',
      fontSize: 13,
      theme,
    })
    fit = new FitAddon()
    t.loadAddon(fit)
    t.loadAddon(new WebLinksAddon())
    t.open(el)
    fit.fit()

    t.onData((d) => {
      send(applyModifiers(d, mods.value))
      clearMods()
    })

    observer = new ResizeObserver(() => doFit())
    observer.observe(el)

    hostEl = el
    // Non-passive: onTouchMove must be able to cancel the browser's own gesture.
    el.addEventListener('touchstart', onTouchStart, { passive: false })
    el.addEventListener('touchmove', onTouchMove, { passive: false })
    el.addEventListener('touchend', onTouchEnd, { passive: true })
    el.addEventListener('touchcancel', onTouchCancel, { passive: true })

    term.value = t
  }

  // rowHeight measures the rendered row pitch. .xterm-screen is sized to
  // exactly rows × rowHeight, unlike the host element, which keeps the few
  // leftover pixels FitAddon could not fill — using the host would make the
  // backlog lag behind the finger.
  function rowHeight(): number {
    const t = term.value
    if (!t) return 0
    const screen = t.element?.querySelector<HTMLElement>('.xterm-screen')
    const height = screen?.clientHeight || hostEl?.clientHeight || 0
    const rows = t.rows || 24
    return height > 0 ? height / rows : 0
  }

  // scrollPixels scrolls by a pixel delta (positive = content moves up, i.e.
  // toward newer output), carrying the sub-line remainder across calls. Returns
  // false when the backlog is already at the requested end, so momentum stops
  // instead of spinning against the top or bottom of the scrollback.
  function scrollPixels(dy: number): boolean {
    const t = term.value
    const pitch = rowHeight()
    if (!t || pitch <= 0) return false
    touchAccum += dy
    const lines = Math.trunc(touchAccum / pitch)
    if (lines === 0) return true
    touchAccum -= lines * pitch
    const before = t.buffer.active.viewportY
    t.scrollLines(lines)
    if (t.buffer.active.viewportY !== before) return true
    // At an edge: drop the remainder so reversing direction responds instantly.
    touchAccum = 0
    return false
  }

  function stopMomentum() {
    if (momentumFrame !== null) {
      cancelAnimationFrame(momentumFrame)
      momentumFrame = null
    }
    velocity = 0
  }

  // startMomentum continues a flick with exponential decay, so the full history
  // is reachable in a few gestures rather than dozens of short drags.
  function startMomentum() {
    if (Math.abs(velocity) < momentumMinVelocity) return
    let last = performance.now()
    const step = (now: number) => {
      momentumFrame = null
      // Clamp dt so a backgrounded tab does not resume with a huge jump.
      const dt = Math.min(now - last, 32)
      last = now
      if (!scrollPixels(velocity * dt)) {
        velocity = 0
        return
      }
      velocity *= Math.pow(momentumDecay, dt / 16.67)
      if (Math.abs(velocity) < momentumMinVelocity) {
        velocity = 0
        return
      }
      momentumFrame = requestAnimationFrame(step)
    }
    momentumFrame = requestAnimationFrame(step)
  }

  function onTouchStart(e: TouchEvent) {
    stopMomentum()
    touching = e.touches.length === 1
    if (!touching) return
    touchLastY = e.touches[0].clientY
    touchAccum = 0
    lastMoveAt = e.timeStamp
  }

  function onTouchMove(e: TouchEvent) {
    if (!touching || e.touches.length !== 1) return
    // Own the gesture for the whole drag, in either direction and at either end
    // of the scrollback — otherwise the leftover movement becomes a browser
    // pull-to-refresh or edge back-swipe.
    if (e.cancelable) e.preventDefault()
    const y = e.touches[0].clientY
    const dy = touchLastY - y
    touchLastY = y
    const dt = e.timeStamp - lastMoveAt
    lastMoveAt = e.timeStamp
    // Weighted toward the latest sample so the flick matches the finger's
    // speed at release, but smoothed enough to ignore jittery events.
    if (dt > 0) velocity = 0.7 * (dy / dt) + 0.3 * velocity
    scrollPixels(dy)
  }

  function onTouchEnd(e: TouchEvent) {
    // A multi-finger touch dropping back to one finger: re-anchor on the
    // survivor and keep scrolling rather than stranding the gesture.
    if (e.touches.length === 1) {
      touching = true
      touchLastY = e.touches[0].clientY
      touchAccum = 0
      velocity = 0
      lastMoveAt = e.timeStamp
      return
    }
    if (!touching) return
    touching = false
    if (e.timeStamp - lastMoveAt > flickMaxIdleMs) velocity = 0
    startMomentum()
  }

  function onTouchCancel() {
    touching = false
    stopMomentum()
  }

  function doFit() {
    if (!fit || !term.value) return
    try {
      fit.fit()
    } catch {
      return
    }
    sendResize()
  }

  function sendResize() {
    if (!term.value || !ws || ws.readyState !== WebSocket.OPEN) return
    ws.send(encodeResize(term.value.cols, term.value.rows))
  }

  function connect(id: string) {
    sessionId = id
    openSocket()
  }

  function openSocket() {
    if (disposed) return
    if (status.value !== 'disconnected') status.value = 'connecting'
    ws = new WebSocket(sessionWsURL(sessionId))
    ws.binaryType = 'arraybuffer'

    ws.onopen = () => {
      status.value = 'connected'
      reconnectAttempts = 0
      // Push our current geometry so the PTY matches the viewport.
      sendResize()
    }
    ws.onmessage = (ev) => {
      if (typeof ev.data === 'string') {
        handleControl(ev.data)
      } else {
        term.value?.write(new Uint8Array(ev.data as ArrayBuffer))
      }
    }
    ws.onclose = (ev) => scheduleReconnect(ev.code)
    ws.onerror = () => {
      // onclose fires after onerror; let scheduleReconnect there handle it.
      ws?.close()
    }
  }

  // scheduleReconnect retries with backoff unless the session ended, the server
  // reported it unavailable (4404), or the component was disposed.
  function scheduleReconnect(code?: number) {
    ws = null
    if (disposed || status.value === 'exited') return
    if (code === closeSessionUnavailable) {
      status.value = 'exited'
      return
    }
    status.value = 'disconnected'
    const delay = backoffSteps[Math.min(reconnectAttempts, backoffSteps.length - 1)]
    reconnectAttempts++
    reconnectTimer = setTimeout(openSocket, delay)
  }

  function handleControl(data: string) {
    const msg = parseControl(data)
    if (!msg) return
    switch (msg.type) {
      case 'attached':
        // Clear before the ring-buffer replay so it renders from a clean slate.
        term.value?.reset()
        break
      case 'exit':
        status.value = 'exited'
        break
      case 'error':
        term.value?.write(`\r\n\x1b[31m[sessile] ${msg.message}\x1b[0m\r\n`)
        break
    }
  }

  function dispose() {
    disposed = true
    if (reconnectTimer) {
      clearTimeout(reconnectTimer)
      reconnectTimer = null
    }
    stopMomentum()
    observer?.disconnect()
    observer = null
    if (hostEl) {
      hostEl.removeEventListener('touchstart', onTouchStart)
      hostEl.removeEventListener('touchmove', onTouchMove)
      hostEl.removeEventListener('touchend', onTouchEnd)
      hostEl.removeEventListener('touchcancel', onTouchCancel)
      hostEl = null
    }
    if (ws) {
      ws.onclose = null
      ws.onerror = null
      ws.onmessage = null
      ws.close()
      ws = null
    }
    term.value?.dispose()
    term.value = null
    fit = null
  }

  return {
    status,
    term,
    mods,
    open,
    connect,
    dispose,
    toggleMod,
    pressSpecial,
    fit: doFit,
  }
}
