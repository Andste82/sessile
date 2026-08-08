// Diagnostics for issue #82, shown only when the page was opened with
// ?debug=ime — the same instrument-then-retire approach the scroll overlay took
// for #64 (retired in #69).
//
// Swipe typing loses the trailing space that a mobile keyboard commits after a
// glided word, and the fault lives on a phone: there is no console to read, no
// way to reproduce it on a desktop, and the keyboard's actual event sequence is
// the one thing static reading of the code cannot supply. Two mechanisms fit
// what is reported and they need opposite fixes, so this records the sequence
// instead of guessing between them.
//
// What to look for in a trace of one glided word:
//   - Is there an `input` with inputType "insertText" and data " " at all? If
//     not, the keyboard never commits a separate space and the fault is
//     elsewhere entirely.
//   - Does it arrive before or after `settle`? Before means the sequence should
//     have carried it and the loss is downstream; after means the settle window
//     closed too early and the space was left outside the sequence.
//   - Does `ta` hold the space when `settle` reads it? That is the value the
//     terminal receives, so a space missing there is a space the keyboard never
//     put in the buffer.
//   - Compare `sent` against what appeared in the shell.

/** One recorded step. Strings are stored raw and escaped only when formatted. */
export interface ImeTraceEntry {
  /** Milliseconds since the first entry. */
  at: number
  /** What happened: an event name, or one of our own decisions. */
  kind: string
  /** InputEvent.inputType, where there was one. */
  inputType?: string
  /** Event data, or the text we delivered. */
  data?: string
  /** The helper textarea's value at that moment — the load-bearing datum. */
  ta?: string
  /** Our own sequence state: active / composing / delivered. */
  state?: string
  /** Key identity, for keydowns. */
  key?: string
  keyCode?: number
}

/** Where the flag is remembered for the rest of the tab. */
export const imeTraceKey = 'sessile.debug.ime'

/** The part of Storage this needs, so a test can pass a fake. */
export type FlagStore = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>

/**
 * imeTraceEnabled reports whether this tab asked for the recorder.
 *
 * The flag is sticky for the tab, because the router does not carry a query
 * across navigation: the dashboard links to `/sessions/:id` and nothing else,
 * so opening the app with ?debug=ime and tapping a session would arrive at the
 * terminal with the flag gone — and the one place it is needed is the terminal.
 * Whoever set it would swipe, see no panel, and conclude the instrument was
 * broken rather than absent.
 *
 * `?debug=` with anything else — `?debug=off` will do — clears it again.
 * sessionStorage rather than localStorage, deliberately: a diagnostic that
 * outlives the tab it was switched on in is a trap for whoever opens the app
 * next, and the app's own preferences live in localStorage under the same
 * prefix.
 */
export function imeTraceEnabled(search: string, store?: FlagStore | null): boolean {
  let asked: string | null = null
  try {
    asked = new URLSearchParams(search).get('debug')
  } catch {
    asked = null
  }
  const fromQuery = asked === 'ime'
  if (!store) return fromQuery

  try {
    if (fromQuery) {
      store.setItem(imeTraceKey, '1')
      return true
    }
    // An explicit `debug` that is not ours turns it off again.
    if (asked !== null) {
      store.removeItem(imeTraceKey)
      return false
    }
    return store.getItem(imeTraceKey) === '1'
  } catch {
    // Private-mode browsers can throw on storage access; the query still works.
    return fromQuery
  }
}

export interface ImeTrace {
  record(entry: Omit<ImeTraceEntry, 'at'>): void
  entries(): ImeTraceEntry[]
  format(): string
  clear(): void
}

/**
 * createImeTrace builds a recorder.
 *
 * The buffer is capped because a keyboard emits several events per keystroke
 * and the page may stay open for a while before the bug shows: the oldest
 * entries go, since the interesting ones are always the last few words typed.
 */
export function createImeTrace(limit = 300, now: () => number = Date.now): ImeTrace {
  let list: ImeTraceEntry[] = []
  let origin: number | null = null

  return {
    record(entry) {
      const t = now()
      if (origin === null) origin = t
      list.push({ ...entry, at: t - origin })
      if (list.length > limit) list = list.slice(list.length - limit)
    },
    entries: () => list.slice(),
    format: () => formatImeTrace(list),
    clear() {
      list = []
      origin = null
    },
  }
}

/**
 * formatImeTrace renders a trace as text to paste into the issue.
 *
 * Every captured string is JSON-quoted rather than printed bare. The whole
 * question is whether a single space survives, and a bare trailing space in a
 * log line is invisible — which is the one thing this must not be.
 */
export function formatImeTrace(entries: ImeTraceEntry[]): string {
  if (entries.length === 0) return '(nothing recorded yet — type a swiped word)'
  return entries.map(formatEntry).join('\n')
}

function formatEntry(e: ImeTraceEntry): string {
  const parts = [`+${String(e.at).padStart(5)}ms`, e.kind.padEnd(22)]
  if (e.inputType) parts.push(e.inputType)
  if (e.key !== undefined) parts.push(`key=${JSON.stringify(e.key)}`)
  if (e.keyCode !== undefined) parts.push(`code=${e.keyCode}`)
  if (e.data !== undefined) parts.push(`data=${JSON.stringify(e.data)}`)
  if (e.ta !== undefined) parts.push(`ta=${JSON.stringify(e.ta)}`)
  if (e.state) parts.push(`[${e.state}]`)
  return parts.join(' ')
}

/** stateFlags renders our sequence state compactly: A active, C composing, D delivered. */
export function stateFlags(active: boolean, composing: boolean, delivered: boolean): string {
  return `A${active ? '+' : '-'} C${composing ? '+' : '-'} D${delivered ? '+' : '-'}`
}
