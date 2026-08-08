import { ref, shallowRef, watch } from 'vue'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebLinksAddon } from '@xterm/addon-web-links'
import { WebglAddon } from '@xterm/addon-webgl'
import { applyUnicodeVersion } from '@/utils/unicode'
import { scrollTargetFor, type MouseTracking } from '@/utils/gesture'
import { useUiStore } from '@/stores/ui'
import { loadSymbolFont, symbolFontFamilies } from '@/utils/fonts'
import { planReconnect, type ConnStatus } from '@/utils/reconnect'
import { encodeResize, parseControl, sessionWsURL } from '@/api/wsProtocol'
import { isCompositionArtifact, isImeKey, shouldFlushIme } from '@/utils/ime'
import type { ImeTraceEntry } from '@/utils/imeTrace'
import { createImeTrace, imeTraceEnabled, stateFlags } from '@/utils/imeTrace'
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

// Re-exported so the components that render a connection state keep importing
// it from the composable that owns one.
export type { ConnStatus }

// Monospace stack first — xterm measures the cell from it, and every font here
// has the digits it measures with, so the emoji fallbacks appended at the end
// cannot change the cell size. Naming them matters anyway: without an emoji
// font in the stack a browser is free to fall back to a proportional face
// whose glyph overhangs the two cells we now reserve (issue #27).
// The symbol faces sit between the two groups: after the monospace ones, so
// they can never be asked for a character those already have, and before the
// emoji ones, so ☠ ⚛ ☢ ⌘ ■ get the monochrome glyph that fits their single cell
// instead of a two-cell emoji (issue #46). They ship with the app — see
// utils/fonts.ts for why leaving that to the machine did not work.
// The size the stack is rendered at is the user's, and lives in the ui store.
const fontFamily = [
  'ui-monospace',
  'SFMono-Regular',
  '"SF Mono"',
  'Menlo',
  'Consolas',
  'monospace',
  ...symbolFontFamilies,
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
  const ui = useUiStore()

  let fit: FitAddon | null = null
  let ws: WebSocket | null = null
  let observer: ResizeObserver | null = null
  let hostEl: HTMLElement | null = null
  const encoder = new TextEncoder()
  let disposed = false

  // Touch scrolling (§ mobile): we drive the backlog ourselves from a one-finger
  // drag and preventDefault() every move, which is what keeps browser gestures
  // (pull-to-refresh, back-swipe, overscroll bounce) from firing mid-scroll, and
  // what buys the flick momentum below. That requires non-passive listeners; the
  // matching `touch-action: none` lives in style.css. xterm scrolls on touch
  // too, and the two must not both run — see the listeners in open().
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
  // True only while resetXtermComposition is dispatching its own compositionend,
  // so the handler below can recognise that event by identity. It used to
  // recognise it by isTrusted, which is not the same question — see there.
  let imeResetting = false
  // Experiment for issue #82. A keyboard decides whether a glided word needs a
  // leading space by asking what precedes the cursor, and the helper textarea
  // is emptied after every word, so it always answers "start of field". With
  // this on, the delivered text stays in front of the cursor and only what is
  // new is sent. Off by default until the device says it is the answer.
  const imeKeepContext = ref(true)
  // What is currently parked in the textarea as context, already delivered.
  let imeContext = ''
  // Enough for a keyboard to see the preceding word; there is no use for more.
  const imeContextMax = 64
  let imeRestoreTimer: ReturnType<typeof setTimeout> | null = null
  // Quiet period that ends a sequence. Gboard's end-then-restart churn lands
  // within a task or two; anything longer is a new word, not a correction.
  const imeSettleMs = 40

  // Recorder for issue #82, off unless the page was opened with ?debug=ime.
  // Swipe typing drops the trailing space a mobile keyboard commits after a
  // glided word, and the sequence that would say why only happens on a phone.
  // See utils/imeTrace.ts; retire this with the fault, as #69 did for #64.
  // Read only: main.ts captures the launch URL's flag before anything renders,
  // because the router drops the query on the way here from the dashboard.
  const imeTracing = imeTraceEnabled(
    typeof sessionStorage === 'undefined' ? null : sessionStorage,
  )
  const imeTrace = createImeTrace()

  // trace records one step. Reading the textarea is the whole point — it is the
  // buffer the delivered text is taken from — so it is captured every time.
  function trace(kind: string, extra: Partial<ImeTraceEntry> = {}) {
    if (!imeTracing) return
    imeTrace.record({
      kind,
      ta: term.value?.textarea?.value ?? '',
      state: stateFlags(imeActive, imeComposing, imeDelivered),
      ...extra,
    })
  }

  // Clipboard state (issue #21) — see handleKeyEvent and onPaste.
  const applePlatform = isApplePlatform(navigator)
  // How long to wait for a chord's native paste before reading the clipboard
  // ourselves: long enough for the event to be dispatched, short enough that
  // the keystroke still feels like it did something.
  const pasteFallbackMs = 150
  let pasteFallbackTimer: ReturnType<typeof setTimeout> | null = null
  let pasteCount = 0
  let beforeInputHandledPaste = false

  // Copy-on-select: true between a left-button press inside the terminal and
  // the release that ends it. The flag is what tells our selection apart from
  // one made elsewhere in the app — the release is listened for on the
  // document, so a drag that runs off the edge of the terminal still copies,
  // and without it a selection dragged in the sidebar would end by putting the
  // terminal's leftover selection on the clipboard.
  let selecting = false

  let touching = false
  let pointerId: number | null = null // the finger the gesture follows
  let touchLastY = 0
  let touchLastX = 0
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

  // loadWebgl swaps the DOM renderer for the GPU one, which is what VS Code's
  // terminal runs on. It has to happen after open(): the addon needs the
  // element the terminal was opened into.
  //
  // Every failure path ends in the DOM renderer, which is xterm's default and
  // stays correct — it is the slow one, not the wrong one. loadAddon throws
  // where there is no WebGL2 context to be had (old devices, a blocklisted
  // driver, a browser with acceleration switched off), and a context can also
  // be taken away later: mobile browsers reclaim GPU memory from backgrounded
  // tabs, and a phone left on the terminal overnight comes back to a dead
  // context. onContextLoss is the only warning of that, and an addon disposed
  // there hands rendering back rather than leaving a terminal that has stopped
  // painting.
  function loadWebgl(t: Terminal) {
    try {
      const addon = new WebglAddon()
      // Disposing here hands rendering back to the DOM renderer, which picks
      // the buffer up as it stands.
      addon.onContextLoss(() => addon.dispose())
      t.loadAddon(addon)
    } catch {
      // No WebGL2 context to be had. The DOM renderer is already in place.
    }
  }

  // Awaits the symbol font before building the terminal. xterm measures each
  // character once and caches the width it works letter-spacing out from, so a
  // symbol drawn before the font lands keeps the fallback's width until
  // something clears that cache. The wait is bounded and, after the first
  // visit, served from cache — see utils/fonts.ts.
  async function open(el: HTMLElement) {
    await loadSymbolFont(ui.terminalFontSize)
    if (disposed) return // unmounted while the font was in flight

    const t = new Terminal({
      scrollback: 5000,
      cursorBlink: true,
      fontFamily,
      // Read after the await, so a size changed while the font was in flight is
      // the one the terminal opens at — the watcher below has nothing to apply
      // it to until then.
      fontSize: ui.terminalFontSize,
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
    loadWebgl(t)
    fit.fit()

    t.onData((d) => {
      send(applyModifiers(d, mods.value))
      clearMods()
    })

    t.attachCustomKeyEventHandler(handleKeyEvent)

    observer = new ResizeObserver(() => doFit())
    observer.observe(el)

    hostEl = el
    // Capture, and stopped there (issue #64): xterm binds touch scrolling of its
    // own to the .xterm element below us, and its cancel() does not stop
    // propagation, so both scrollers ran on every move. They settle on different
    // clocks — ours moves ydisp synchronously, xterm's moves the viewport's
    // scrollTop and applies a rounded line delta from the scroll event the
    // browser dispatches later, while xterm's rAF sync writes scrollTop back
    // from ydisp in between. Whichever landed first decided whether a swipe
    // scrolled once, twice, or (when the late delta cancelled ours) not at all.
    // Taking the gesture in the capture phase leaves exactly one scroller.
    //
    // Every touchstart and touchmove is withheld, not only the ones we act on:
    // xterm tracks the finger from one event to the next, so letting a single
    // move through after swallowing its predecessors would hand it a stale
    // position and a jump the size of the whole drag. Only touchmove is
    // cancelled, so a tap still reaches xterm as the click that focuses the
    // terminal and drives selection.
    //
    // Non-passive: onTouchMove must be able to cancel the browser's own gesture.
    const ownGesture = { passive: false, capture: true }
    el.addEventListener('touchstart', onTouchStart, ownGesture)
    el.addEventListener('touchmove', onTouchMove, ownGesture)

    // The gesture itself — see onPointerDown for why it cannot be the touch
    // events above.
    el.addEventListener('pointerdown', onPointerDown)
    el.addEventListener('pointermove', onPointerMove)
    el.addEventListener('pointerup', onPointerUp)
    el.addEventListener('pointercancel', onPointerCancel)

    // The other half of "one scroller at a time" (issue #64). Blocking xterm's
    // touch handling is not enough on its own: xterm also writes
    // .xterm-viewport's scrollTop from ydisp on an animation frame, and reads
    // it back on the scroll event the browser dispatches afterwards, turning it
    // into a line delta of its own. It may skip exactly one such event, so the
    // round trip only balances while writes and events alternate. A busy frame
    // or a write landing mid-gesture breaks that, and the delta then computed
    // from a scrollTop we have since scrolled past drags the buffer back where
    // it came from — the leftover "one line per swipe, then fine again".
    // While the finger or its momentum owns the terminal we are the only thing
    // that may move the buffer, so the event never reaches xterm. Capture
    // reaches it despite scroll not bubbling: the capture phase walks the
    // ancestors of the target either way.
    el.addEventListener('scroll', onViewportScroll, true)

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

    // Copy-on-select. Never cancelled: xterm owns the mouse selection itself,
    // and all we do is read the result of it. Capture phase, though, because a
    // selection inside a program that asked for mouse tracking — vim, htop —
    // is made by holding the force-selection modifier, and xterm answers that
    // one by stopping the mousedown from propagating. In the bubble phase we
    // would never see it, and exactly those drags would fail to copy.
    el.addEventListener('mousedown', onSelectionMouseDown, true)
    document.addEventListener('mouseup', onSelectionMouseUp)

    term.value = t
  }

  // A size picked in Settings applies to the terminal that is already open —
  // nobody wants to reopen a session to see whether the size they chose is the
  // readable one. xterm re-measures the cell
  // when the option changes, so the fit afterwards is what turns the new cell
  // into a column count the PTY is told about.
  watch(
    () => ui.terminalFontSize,
    (size) => {
      if (!term.value) return
      term.value.options.fontSize = size
      doFit()
    },
  )

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
        trace('ime key', { key: e.key, keyCode: e.keyCode })
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
  // stays SIGINT — so copying is the selection itself (when copy-on-select is
  // on), Ctrl+Shift+C, Ctrl+Insert, or right-click.
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

  // onSelectionMouseDown opens a selection drag. Left button only: the right
  // button opens the context menu (a copy path of its own, and one that must
  // not be pre-empted by a copy of the selection it is being opened on), and
  // the middle button is X11's paste.
  function onSelectionMouseDown(e: MouseEvent) {
    selecting = e.button === 0
  }

  // onSelectionMouseUp copies what the drag selected, if the preference is on.
  //
  // Reading the selection here is safe despite xterm having a document-level
  // mouseup listener of its own, registered on mousedown and therefore after
  // ours: the selection model is written during mousemove, and for the clicks
  // that select without dragging — double-click for a word, triple for a line,
  // shift-click to extend — during mousedown. By the time any mouseup runs,
  // what getSelection() returns is final.
  //
  // Nothing is copied when the selection is empty, so the click that dismisses
  // a selection leaves the clipboard alone rather than clearing it.
  function onSelectionMouseUp() {
    if (!selecting) return
    selecting = false
    if (ui.copyOnSelect) copySelection()
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
    // Start from a known buffer so the value read at the end is this word and
    // nothing else. Normally that is empty — every sequence ends by clearing
    // it — but xterm's blur and right-click paths write there too, and with the
    // context experiment on it is the tail of what was already delivered.
    const ta = term.value?.textarea
    if (ta && ta.value !== imeContext) {
      ta.value = imeContext
      ta.setSelectionRange(imeContext.length, imeContext.length)
    }
  }

  function onCompositionStart(e: Event) {
    if (imeResetting) return // ours, from resetXtermComposition
    trace('compositionstart', { data: (e as CompositionEvent).data ?? '' })
    beginImeSequence()
    imeComposing = true
    clearImeSettle() // an open composition ends on its own; nothing to wait for
  }

  // onCompositionEnd is withheld from xterm: its _finalizeComposition would
  // send a slice of the textarea, and on a predictive keyboard this event
  // often marks an intermediate state ("hel" before the tapped "hello"), not
  // the finished word. What ends the word is quiet, so we wait for it.
  // The guard is our own dispatch flag, not e.isTrusted. The question here is
  // "did we send this", and isTrusted answers a different one: Chrome ends a
  // composition with an untrusted compositionend of its own in the path a
  // keyboard commits a glided word through. Treating that as ours left
  // imeComposing stuck on, so no settle was ever armed and the sequence never
  // finished — the word escaped through xterm's own path, and everything the
  // keyboard committed after it, the trailing space included, stayed in the
  // helper textarea for good (issue #82).
  function onCompositionEnd(e: Event) {
    if (imeResetting) return
    trace('compositionend', { data: (e as CompositionEvent).data ?? '' })
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
    const gated = isCompositionArtifact(imeActive, inputType)
    trace(gated ? 'input gated' : 'input passed to xterm', {
      inputType,
      data: (e as InputEvent).data ?? '',
    })
    if (!gated) return
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
    // Only what the keyboard added since the context was parked is new; the
    // context itself has already been sent once.
    const fresh = text.startsWith(imeContext) ? text.slice(imeContext.length) : text
    imeContext = imeKeepContext.value
      ? (imeContext + fresh).slice(-imeContextMax)
      : ''
    // Always leave it empty, whatever the context is. xterm reads this buffer
    // from a timer of its own the moment resetXtermComposition fires, and an
    // empty read is what makes it send nothing — see restoreImeContext for the
    // other half.
    ta.value = ''
    ta.setSelectionRange(0, 0)
    return fresh
  }

  // restoreImeContext puts the delivered tail back in front of the cursor, so
  // the keyboard can see what precedes the next word and prepend a space to it
  // itself — the whole of issue #82. Measured on a device: with "Hallo" parked
  // there, the next glided word arrives as " wir" rather than "wir".
  //
  // On a timer, and queued after resetXtermComposition, because xterm reads the
  // same buffer from a timer it queues there. Ours therefore runs second and
  // xterm still sees the empty buffer it needs to stay quiet. Restoring
  // synchronously instead put the context in front of that read, and xterm
  // delivered it as terminal input: three glided words reached the pty as
  // "hellohello wolfhello wolf rennthello wolf rennt".
  function restoreImeContext() {
    clearImeRestore()
    imeRestoreTimer = setTimeout(() => {
      imeRestoreTimer = null
      if (!imeContext || imeActive) return // a new word owns the buffer already
      const ta = term.value?.textarea
      if (!ta || ta.value !== '') return // something else is using it
      ta.value = imeContext
      ta.setSelectionRange(imeContext.length, imeContext.length)
      trace('context restored')
    }, 0)
  }

  function clearImeRestore() {
    if (imeRestoreTimer) {
      clearTimeout(imeRestoreTimer)
      imeRestoreTimer = null
    }
  }

  // setImeKeepContext flips the experiment and leaves the buffer consistent
  // with it, so turning it off cannot strand text nothing will deliver.
  function setImeKeepContext(on: boolean) {
    imeKeepContext.value = on
    clearImeRestore()
    const ta = term.value?.textarea
    imeContext = ''
    if (ta) {
      ta.value = ''
      ta.setSelectionRange(0, 0)
    }
    trace(`context experiment ${on ? 'on' : 'off'}`)
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
    trace('flush (a real key ended the word)')
    // A real key ends the thought as well as the word — Enter runs the command,
    // an arrow moves away from it. Whatever the keyboard writes next is not a
    // continuation, so it gets no context to prepend a space to.
    imeContext = ''
    clearImeRestore()
    clearImeSettle()
    imeDelivered = true
    armImeSettle()
    deliverIme(takeImeText())
  }

  // settleIme ends a sequence that has gone quiet, delivering the word unless
  // a real key already flushed it.
  function settleIme() {
    imeSettleTimer = null
    if (!imeActive) {
      trace('settle (no sequence open)')
      return
    }
    trace('settle')
    const text = takeImeText()
    const delivered = imeDelivered
    imeActive = false
    imeComposing = false
    imeDelivered = false
    resetXtermComposition()
    if (delivered) trace('tail dropped (already flushed)', { data: text })
    if (!delivered) deliverIme(text)
    restoreImeContext()
  }

  // deliverIme sends committed text the way typed text is sent, so the key bar's
  // armed modifiers still apply to a one-character commit.
  function deliverIme(text: string) {
    // While the context experiment runs, nothing is sent. Keeping text in the
    // helper textarea restarts xterm's own diffing, which then delivers the
    // whole buffer on top of our delta — measured: three glided words reached
    // the pty as "hellohello wolfhello wolf rennthello wolf rennt". The
    // experiment only needs the trace to show whether the keyboard starts
    // prepending a space once it can see what precedes the cursor, so the shell
    // is left out of it rather than filled with nonsense.
    trace(text ? 'SENT' : 'nothing to send', { data: text })
    if (!text) return
    send(applyModifiers(text, mods.value))
    clearMods()
  }

  // resetXtermComposition hands xterm a finished composition over the textarea
  // we just emptied. It clears isComposing, releases the key path its helper
  // holds open, and hides the composition overlay — while its own finalize
  // reads an empty string and so cannot deliver the word again.
  function resetXtermComposition() {
    imeResetting = true
    try {
      term.value?.textarea?.dispatchEvent(
        new CompositionEvent('compositionend', { data: '' })
      )
    } finally {
      // dispatchEvent is synchronous, so the flag covers exactly this event and
      // nothing else — which is the whole reason it can replace isTrusted.
      imeResetting = false
    }
  }

  // onViewportScroll withholds a viewport scroll event from xterm for as long
  // as the gesture owns the terminal — see the listener registration in open().
  // Outside a gesture the event is xterm's own business: it is what turns a
  // dragged scrollbar into a buffer scroll on a desktop.
  function onViewportScroll(e: Event) {
    if (touching || momentumFrame !== null) e.stopPropagation()
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

  // scrollBy sends a drag wherever the terminal's current state says it belongs
  // — the same fork a desktop makes on a mouse wheel, which is the behaviour to
  // match: a shell scrolls its backlog, and a program drawing its own screen
  // gets told to scroll instead. Without the second half, a TUI on a phone
  // simply does not react to being scrolled, because the alternate screen has
  // no scrollback for us to move.
  //
  // Returns false when the scroll went nowhere, which is what stops momentum
  // spinning against the end of a backlog.
  function scrollBy(dy: number): boolean {
    const t = term.value
    if (!t) return false
    const target = scrollTargetFor(
      t.buffer.active.type,
      t.modes.mouseTrackingMode as MouseTracking
    )
    if (target === 'backlog') return scrollPixels(dy)
    sendWheel(dy)
    // A program is free to ignore the scroll, and there is no way to ask
    // whether it did, so a flick coasts its full decay rather than stopping on
    // a signal we cannot read.
    return true
  }

  // sendWheel hands the drag to xterm as a wheel event, which is the road the
  // desktop already takes. xterm turns it into a mouse report for a program
  // that asked for mouse tracking, or into cursor keys for one on the alternate
  // screen — respecting application-cursor mode, the active mouse protocol, and
  // its own sub-line accumulator. Encoding it here instead would mean a second
  // copy of all three, free to drift from the one the wheel uses.
  //
  // Pixel delta and the finger's position: xterm reads the coordinates to fill
  // in the row and column a mouse report carries.
  function sendWheel(dy: number) {
    term.value?.element?.dispatchEvent(
      new WheelEvent('wheel', {
        deltaY: dy,
        deltaMode: 0, // pixels; xterm keeps the sub-line remainder itself
        clientX: touchLastX,
        clientY: touchLastY,
        bubbles: true,
        cancelable: true,
      })
    )
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
      if (!scrollBy(velocity * dt)) {
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

  // The gesture runs on pointer events rather than touch events, because of the
  // way xterm renders (issue #64). A touch event is delivered to whatever was
  // under the finger when it landed, for the whole gesture — and xterm's DOM
  // renderer rebuilds a row's children every time it draws it:
  //
  //   rowElement.replaceChildren(...rowFactory.createRow(...))
  //
  // Scrolling draws rows. So a finger that came down on a character is holding
  // a <span> that the first scrolled line deletes, and every event after that
  // is delivered to it while it hangs detached from the document, where neither
  // our listener nor xterm's can see it. The view follows the finger for a few
  // pixels and then stops dead. A finger that came down on empty space — the
  // row itself, or past the end of the text — holds an element that survives,
  // and that swipe tracks the finger the whole way. Which of the two you get is
  // decided by where the finger landed, which is why one session gave both.
  //
  // setPointerCapture takes that decision away from the DOM: every later event
  // for this pointer is delivered to the host element, whatever becomes of what
  // was underneath it.
  function onPointerDown(e: PointerEvent) {
    if (e.pointerType === 'mouse') return // desktop selection stays xterm's
    stopMomentum()
    // A second finger is not a new gesture: a palm or the base of a thumb
    // landing mid-swipe must not take it over, or end it.
    if (pointerId !== null) return
    pointerId = e.pointerId
    try {
      hostEl?.setPointerCapture(e.pointerId)
    } catch {
      // Refused: the gesture still works for as long as what is under the
      // finger lives, which is all it ever did before.
    }
    touchLastY = e.clientY
    touchLastX = e.clientX
    touchAccum = 0
    velocity = 0
    lastMoveAt = e.timeStamp
    touching = true
  }

  function onPointerMove(e: PointerEvent) {
    if (e.pointerType === 'mouse' || e.pointerId !== pointerId) return
    if (!touching) return
    const dy = touchLastY - e.clientY
    touchLastY = e.clientY
    touchLastX = e.clientX
    const dt = e.timeStamp - lastMoveAt
    lastMoveAt = e.timeStamp
    // Weighted toward the latest sample so the flick matches the finger's
    // speed at release, but smoothed enough to ignore jittery events.
    if (dt > 0) velocity = 0.7 * (dy / dt) + 0.3 * velocity
    scrollBy(dy)
  }

  function onPointerUp(e: PointerEvent) {
    if (e.pointerType === 'mouse') return
    if (e.pointerId !== pointerId) return
    pointerId = null
    if (!touching) return
    touching = false
    if (e.timeStamp - lastMoveAt > flickMaxIdleMs) velocity = 0
    startMomentum()
  }

  function onPointerCancel(e: PointerEvent) {
    if (e.pointerType === 'mouse') return
    if (e.pointerId !== pointerId) return
    pointerId = null
    touching = false
    stopMomentum()
  }

  // The touch listeners drive nothing now. They are what keeps xterm's own
  // touch scrolling out of the gesture (see open()) and what stops the browser
  // turning leftover travel into a pull-to-refresh at the ends of the backlog.
  // They see only the events whose target is still in the document — but the
  // ones that die detached are invisible to xterm as well, so nothing gets
  // through unblocked.
  function onTouchStart(e: TouchEvent) {
    e.stopPropagation()
  }

  function onTouchMove(e: TouchEvent) {
    e.stopPropagation()
    if (e.cancelable) e.preventDefault()
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
    // A reconnect does not announce itself: "connecting" would replace the
    // overlay the user is already reading.
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

  // scheduleReconnect applies the policy in utils/reconnect.ts: backoff for a
  // connection that dropped under a live session, and nothing at all for one
  // the server says is gone — that one is reconnected by the attached frame a
  // restart pushes, or by TerminalPage when the polled list says the session is
  // running again.
  function scheduleReconnect(code?: number) {
    ws = null
    if (disposed) return

    const plan = planReconnect(status.value, code, reconnectAttempts)
    status.value = plan.status
    if (plan.delayMs === null) return
    reconnectAttempts++
    reconnectTimer = setTimeout(openSocket, plan.delayMs)
  }

  function handleControl(data: string) {
    const msg = parseControl(data)
    if (!msg) return
    switch (msg.type) {
      case 'attached':
        // Clear before the ring-buffer replay so it renders from a clean slate.
        term.value?.reset()
        // A second attach on a live connection is a restart: the session ended,
        // someone — not necessarily this browser — started it again, and the
        // server moved us to the new shell. Saying so here is what takes the
        // "session ended" banner down everywhere, rather than only in the tab
        // whose button was clicked.
        status.value = 'connected'
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
      // Capture flag included: it is part of what identifies the listener.
      hostEl.removeEventListener('touchstart', onTouchStart, true)
      hostEl.removeEventListener('touchmove', onTouchMove, true)
      hostEl.removeEventListener('pointerdown', onPointerDown)
      hostEl.removeEventListener('pointermove', onPointerMove)
      hostEl.removeEventListener('pointerup', onPointerUp)
      hostEl.removeEventListener('pointercancel', onPointerCancel)
      hostEl.removeEventListener('scroll', onViewportScroll, true)
      hostEl.removeEventListener('compositionstart', onCompositionStart, true)
      hostEl.removeEventListener('compositionend', onCompositionEnd, true)
      hostEl.removeEventListener('input', gateCompositionInput, true)
      hostEl.removeEventListener('paste', onPaste, true)
      hostEl.removeEventListener('beforeinput', onBeforeInput, true)
      hostEl.removeEventListener('input', onPasteInput, true)
      hostEl.removeEventListener('mousedown', onSelectionMouseDown, true)
      hostEl = null
    }
    // Not on the host: this one is the document's, and outlives the element.
    document.removeEventListener('mouseup', onSelectionMouseUp)
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
    imeTracing,
    imeTrace,
    imeKeepContext,
    setImeKeepContext,
    open,
    connect,
    dispose,
    toggleMod,
    pressSpecial,
    focus,
    fit: doFit,
  }
}
