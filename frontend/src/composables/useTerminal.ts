import { ref, shallowRef } from 'vue'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebLinksAddon } from '@xterm/addon-web-links'
import { encodeResize, parseControl, sessionWsURL } from '@/api/wsProtocol'

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
 * cleanly (§5, §7). Auto-reconnect is layered on in M4.
 */
export function useTerminal() {
  const status = ref<ConnStatus>('connecting')
  const term = shallowRef<Terminal | null>(null)

  let fit: FitAddon | null = null
  let ws: WebSocket | null = null
  let observer: ResizeObserver | null = null
  const encoder = new TextEncoder()
  let disposed = false

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
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(encoder.encode(d))
      }
    })

    observer = new ResizeObserver(() => doFit())
    observer.observe(el)
    term.value = t
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
    if (disposed) return
    status.value = 'connecting'
    ws = new WebSocket(sessionWsURL(id))
    ws.binaryType = 'arraybuffer'

    ws.onopen = () => {
      status.value = 'connected'
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
    ws.onclose = () => {
      if (status.value !== 'exited') status.value = 'disconnected'
    }
    ws.onerror = () => {
      if (status.value !== 'exited') status.value = 'disconnected'
    }
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
    observer?.disconnect()
    observer = null
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

  return { status, term, open, connect, dispose, fit: doFit }
}
