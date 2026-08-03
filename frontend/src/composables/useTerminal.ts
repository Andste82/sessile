import { ref, shallowRef } from 'vue'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebLinksAddon } from '@xterm/addon-web-links'
import { applyUnicodeVersion } from '@/utils/unicode'
import { encodeResize, parseControl, sessionWsURL } from '@/api/wsProtocol'
import { isCompositionArtifact, isImeKey, shouldFlushIme } from '@/utils/ime'
import {
  isApplePlatform,
  isCopyShortcut,
  isPasteInput,
  isPasteShortcut,
  pastedText,
} from '@/utils/clipboard'
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

// Monospace stack first — xterm measures the cell from it, and every font here
// has the digits it measures with, so the emoji fallbacks appended at the end
// cannot change the cell size. Naming them matters anyway: without an emoji
// font in the stack a browser is free to fall back to a proportional face
// whose glyph overhangs the two cells we now reserve (issue #27).
const fontFamily = [
  'ui-monospace',
  'SFMono-Regular',
  '"SF Mono"',
  'Menlo',
  'Consolas',
  'monospace',
  '"Apple Color Emoji"',
  '"Segoe UI Emoji"',
  '"Noto Color Emoji"',
].join(', ')

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
  // IME state (§ mobile, issue #22): xterm has three ways of turning keyboard
  // composition into terminal input and all three are wrong for predictive
  // keyboards. `_inputEvent` forwards any `insertText` without asking whether a
  // composition is in flight; `CompositionHelper.keydown` answers every Android
  // composing keystroke (keyCode 229) by diffing the helper textarea on a timer
  // and sending the difference; `_finalizeComposition` sends a slice of that
  // same textarea at every compositionend. Gboard swapping a half-typed word
  // for a tapped suggestion ends one composition and starts another, so the
  // last two fire mid-word and leak "hel" on the way to "hello" — and neither
  // reads the event, so gating `input` alone (as we first tried) cannot stop
  // them.
  //
  // So we own the sequence instead: withhold every composition event and every
  // composition `input` from xterm, let the browser keep editing the textarea,
  // and deliver its value once — when the keyboard has gone quiet, or when a
  // real key says the word is finished. The textarea is the browser's own
  // account of what the user meant, deletions and re-writes included.
  let imeActive = false // a sequence is in flight, settle window included
  let imeComposing = false // between compositionstart and compositionend
  let imeDelivered = false // already flushed by a real key; ignore the tail
  let imeSettleTimer: ReturnType<typeof setTimeout> | null = null
  // Quiet period that ends a sequence. Gboard's end-then-restart churn lands
  // within a task or two; anything longer is a new word, not a correction.
  const imeSettleMs = 40

  // Clipboard state (issue #21) — see handleKeyEvent and onPaste.
  const applePlatform = isApplePlatform(navigator)
  // How long to wait for a chord's native paste before reading the clipboard
  // ourselves: long enough for the event to be dispatched, short enough that
  // the keystroke still feels like it did something.
  const pasteFallbackMs = 150
  let pasteFallbackTimer: ReturnType<typeof setTimeout> | null = null
  let pasteCount = 0
  let beforeInputHandledPaste = false

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
    // Empty writes are never meaningful, and xterm's composition helper emits
    // them once we have taken its textarea away (see resetXtermComposition).
    if (!data) return
    if (ws && ws.readyState === WebSocket.OPEN) ws.send(encoder.encode(data))
  }

  // focus puts the caret in xterm's helper textarea, so keystrokes go to the
  // PTY without the user having to click the terminal first (issue #25).
  function focus() {
    term.value?.focus()
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
      fontFamily,
      fontSize: 13,
      theme,
      // Required for `unicode.activeVersion` below — xterm gates the whole
      // unicode handle behind this flag.
      allowProposedApi: true,
    })
    fit = new FitAddon()
    t.loadAddon(fit)
    t.loadAddon(new WebLinksAddon())
    // Wide-character widths (issue #27) — see utils/unicode.ts for why the
    // built-in table is not the one we want.
    applyUnicodeVersion(t)
    t.open(el)
    fit.fit()

    t.onData((d) => {
      send(applyModifiers(d, mods.value))
      clearMods()
    })

    t.attachCustomKeyEventHandler(handleKeyEvent)

    observer = new ResizeObserver(() => doFit())
    observer.observe(el)

    hostEl = el
    // Non-passive: onTouchMove must be able to cancel the browser's own gesture.
    el.addEventListener('touchstart', onTouchStart, { passive: false })
    el.addEventListener('touchmove', onTouchMove, { passive: false })
    el.addEventListener('touchend', onTouchEnd, { passive: true })
    el.addEventListener('touchcancel', onTouchCancel, { passive: true })

    // Capture phase on the host: xterm binds its own composition and input
    // handlers directly to the helper textarea, and a capture listener on an
    // ancestor runs before any listener on the target, which is the only way to
    // withhold an event from it.
    el.addEventListener('compositionstart', onCompositionStart, true)
    el.addEventListener('compositionend', onCompositionEnd, true)
    el.addEventListener('input', gateCompositionInput, true)

    // Clipboard, same capture-phase reasoning: xterm binds `paste` to both the
    // textarea and its own container, and we must take the event before it.
    el.addEventListener('paste', onPaste, true)
    el.addEventListener('beforeinput', onBeforeInput, true)
    el.addEventListener('input', onPasteInput, true)

    term.value = t
  }

  // handleKeyEvent runs before xterm's own key handling; returning false means
  // "not mine, and do not cancel it". That is the whole fix for Ctrl+V: xterm
  // would otherwise send ^V and preventDefault the keydown, and a cancelled
  // keydown never becomes a paste. Left uncancelled, the browser pastes into
  // the helper textarea exactly as it does for the context menu, and onPaste
  // picks it up.
  function handleKeyEvent(e: KeyboardEvent): boolean {
    // An IME keystroke is the keyboard talking to itself. Swallow it without
    // cancelling — the browser still needs it to drive the composition, but
    // xterm must not see it, or CompositionHelper.keydown starts diffing the
    // textarea behind our back.
    if (isImeKey(e)) {
      // Opening the sequence here also covers keyboards that report 229 but
      // never open a composition: the quiet period delivers their textarea,
      // which is the job xterm's diffing was doing badly.
      if (e.type === 'keydown') {
        beginImeSequence()
        if (!imeComposing) armImeSettle()
      }
      return false
    }
    if (shouldFlushIme(e, imeActive)) flushIme()
    if (isCopyShortcut(e, applePlatform)) {
      // Swallowed whether or not anything was copied: Ctrl+Shift+C must never
      // reach the shell as ^C. preventDefault only when we did copy, so an
      // empty selection leaves the clipboard alone instead of clearing it.
      if (e.type === 'keydown' && copySelection()) e.preventDefault()
      return false
    }
    if (!isPasteShortcut(e, applePlatform)) return true
    if (e.type === 'keydown') armClipboardFallback()
    return false
  }

  // copySelection puts the terminal's selection on the clipboard, reporting
  // whether there was anything to copy. Plain Ctrl+C never lands here — it
  // stays SIGINT — so copying is Ctrl+Shift+C, Ctrl+Insert, or right-click.
  function copySelection(): boolean {
    const text = term.value?.getSelection() ?? ''
    if (!text) return false
    // The async API needs a secure context, which a self-hosted deployment
    // reached over plain http on a LAN is not. Decide synchronously: an async
    // fallback would land outside the user gesture, where the legacy path is
    // itself refused.
    const clip = window.isSecureContext ? navigator.clipboard : null
    if (clip?.writeText) {
      clip.writeText(text).catch(() => {
        // Permission denied: the selection stays selected, right-click works.
      })
    } else {
      legacyCopy(text)
    }
    return true
  }

  // legacyCopy is the pre-Clipboard-API path, and the same trick xterm uses
  // for right-click copy: stage the text in the helper textarea, which already
  // holds focus, so the copy command has something to act on and the terminal
  // never visibly loses focus.
  function legacyCopy(text: string) {
    const ta = term.value?.textarea
    if (!ta) return
    ta.value = text
    ta.select()
    try {
      document.execCommand('copy')
    } catch {
      // Nothing else to fall back to.
    }
    // Leave it as xterm expects to find it: empty, cursor collapsed.
    ta.value = ''
    ta.setSelectionRange(0, 0)
  }

  // armClipboardFallback covers browsers that do not turn the chord into a
  // paste event for a focused-but-hidden textarea. If nothing was delivered
  // shortly after the keystroke, read the clipboard through the async API
  // instead — still inside the user gesture, so the permission holds.
  function armClipboardFallback() {
    if (pasteFallbackTimer) clearTimeout(pasteFallbackTimer)
    const seen = pasteCount
    pasteFallbackTimer = setTimeout(() => {
      pasteFallbackTimer = null
      if (pasteCount !== seen) return // the native paste arrived
      navigator.clipboard
        ?.readText()
        .then(deliverPaste)
        .catch(() => {
          // No clipboard permission (or no clipboard): nothing more to try.
        })
    }, pasteFallbackMs)
  }

  // onPaste takes over from xterm's own paste handler. Cancelling the event
  // keeps the text out of the helper textarea, which both spares us xterm's
  // trailing `input` event and stops the pasted text lingering there where the
  // next composition would read it back.
  function onPaste(e: ClipboardEvent) {
    e.preventDefault()
    e.stopPropagation()
    deliverPaste(e.clipboardData?.getData('text/plain') ?? '')
  }

  // onBeforeInput handles the mobile path: a paste offered by the keyboard's
  // clipboard menu can arrive as a plain editing command with no clipboard
  // event, and xterm forwards only `insertText`, so the text would be dropped.
  function onBeforeInput(e: InputEvent) {
    if (!isPasteInput(e.inputType)) return
    const text = pastedText(e)
    // Some browsers withhold the text until the edit is applied; recover it
    // from the textarea in onPasteInput instead.
    beforeInputHandledPaste = text !== '' && e.cancelable
    if (!beforeInputHandledPaste) return
    e.preventDefault()
    e.stopPropagation()
    deliverPaste(text)
  }

  // onPasteInput is that recovery: the edit has landed, so the pasted text is
  // sitting in the helper textarea — which xterm otherwise keeps empty.
  function onPasteInput(e: Event) {
    if (!isPasteInput((e as InputEvent).inputType ?? '')) return
    const handled = beforeInputHandledPaste
    beforeInputHandledPaste = false
    if (handled) return
    e.stopPropagation()
    const ta = e.target as HTMLTextAreaElement | null
    const text = pastedText(e as InputEvent) || ta?.value || ''
    if (ta) ta.value = ''
    deliverPaste(text)
  }

  // deliverPaste feeds clipboard text to the terminal through xterm's own
  // paste path, which normalises newlines to CR and applies bracketed-paste
  // framing when the foreground app asked for it.
  function deliverPaste(text: string) {
    if (!text) return
    pasteCount++
    // Leave the textarea empty, as xterm's own paste path does: it is the
    // buffer CompositionHelper reads a committed word out of, and Firefox's
    // right-click handler parks the current selection there.
    const ta = term.value?.textarea
    if (ta) ta.value = ''
    term.value?.paste(text)
  }

  // beginImeSequence opens a sequence, or starts a fresh one when the last
  // word has already been delivered — a composition that begins after a flush
  // is the next word, not a correction of the last.
  function beginImeSequence() {
    if (imeActive && !imeDelivered) return
    imeActive = true
    imeDelivered = false
    // Start from a known-empty buffer so the value read at the end is this word
    // and nothing else. It is normally empty already — every sequence ends by
    // clearing it — but xterm's blur and right-click paths write there too.
    const ta = term.value?.textarea
    if (ta && ta.value !== '') ta.value = ''
  }

  function onCompositionStart(e: Event) {
    if (!e.isTrusted) return // ours, from resetXtermComposition
    beginImeSequence()
    imeComposing = true
    clearImeSettle() // an open composition ends on its own; nothing to wait for
  }

  // onCompositionEnd is withheld from xterm: its _finalizeComposition would
  // send a slice of the textarea, and on a predictive keyboard this event
  // often marks an intermediate state ("hel" before the tapped "hello"), not
  // the finished word. What ends the word is quiet, so we wait for it.
  function onCompositionEnd(e: Event) {
    if (!e.isTrusted) return
    e.stopPropagation()
    imeComposing = false
    armImeSettle()
  }

  // gateCompositionInput withholds composition input from xterm without
  // preventDefault: the textarea must still apply every edit, because its value
  // is what we deliver once the sequence settles. While a composition is open
  // there is nothing to wait for — the keyboard tells us when it ends — but
  // once it has ended, further edits are the keyboard revising its own commit,
  // and each one restarts the quiet period.
  function gateCompositionInput(e: Event) {
    const inputType = (e as InputEvent).inputType ?? ''
    if (!isCompositionArtifact(imeActive, inputType)) return
    e.stopPropagation()
    imeActive = true
    if (!imeComposing) armImeSettle()
  }

  function clearImeSettle() {
    if (imeSettleTimer) {
      clearTimeout(imeSettleTimer)
      imeSettleTimer = null
    }
  }

  function armImeSettle() {
    clearImeSettle()
    imeSettleTimer = setTimeout(settleIme, imeSettleMs)
  }

  // takeImeText empties the helper textarea and returns what was in it.
  function takeImeText(): string {
    const ta = term.value?.textarea
    if (!ta) return ''
    const text = ta.value
    ta.value = ''
    ta.setSelectionRange(0, 0)
    return text
  }

  // flushIme delivers the staged word because a real key arrived. The sequence
  // stays open: the browser still has the composition's own commit events to
  // emit, and those must be swallowed rather than delivered a second time.
  //
  // Emptying the textarea here is also what makes the key safe to hand on to
  // xterm: its helper finalizes any composition it still believes is open by
  // sending a slice of that textarea, which is now empty — and send() drops
  // empty writes. So Enter delivers the word, then the carriage return.
  function flushIme() {
    clearImeSettle()
    imeDelivered = true
    armImeSettle()
    deliverIme(takeImeText())
  }

  // settleIme ends a sequence that has gone quiet, delivering the word unless
  // a real key already flushed it.
  function settleIme() {
    imeSettleTimer = null
    if (!imeActive) return
    const text = takeImeText()
    const delivered = imeDelivered
    imeActive = false
    imeComposing = false
    imeDelivered = false
    resetXtermComposition()
    if (!delivered) deliverIme(text)
  }

  // deliverIme sends committed text the way typed text is sent, so the key bar's
  // armed modifiers still apply to a one-character commit.
  function deliverIme(text: string) {
    if (!text) return
    send(applyModifiers(text, mods.value))
    clearMods()
  }

  // resetXtermComposition hands xterm a finished composition over the textarea
  // we just emptied. It clears isComposing, releases the key path its helper
  // holds open, and hides the composition overlay — while its own finalize
  // reads an empty string and so cannot deliver the word again.
  function resetXtermComposition() {
    term.value?.textarea?.dispatchEvent(
      new CompositionEvent('compositionend', { data: '' })
    )
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
    clearImeSettle()
    if (pasteFallbackTimer) {
      clearTimeout(pasteFallbackTimer)
      pasteFallbackTimer = null
    }
    observer?.disconnect()
    observer = null
    if (hostEl) {
      hostEl.removeEventListener('touchstart', onTouchStart)
      hostEl.removeEventListener('touchmove', onTouchMove)
      hostEl.removeEventListener('touchend', onTouchEnd)
      hostEl.removeEventListener('touchcancel', onTouchCancel)
      hostEl.removeEventListener('compositionstart', onCompositionStart, true)
      hostEl.removeEventListener('compositionend', onCompositionEnd, true)
      hostEl.removeEventListener('input', gateCompositionInput, true)
      hostEl.removeEventListener('paste', onPaste, true)
      hostEl.removeEventListener('beforeinput', onBeforeInput, true)
      hostEl.removeEventListener('input', onPasteInput, true)
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
    focus,
    fit: doFit,
  }
}
