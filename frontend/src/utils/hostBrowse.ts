// State machine behind the SSH start-directory chooser (§4.10): connect to a
// host's session-less directory lister, track whether it is usable, and tear
// the server-side connection down again.
//
// Framework-free on purpose: fetch/close are injected, so the ordering rules
// (a late response from a host the user has already left is ignored; the
// server connection is closed exactly once) are testable without a DOM.

import type { CreateSessionBody, DirectoriesResponse, HostKeyErrorDetails } from '@/api/types'

/**
 * sshCreateBody builds the SSH variant of the create-session body. The
 * directory is sent only when non-empty after trimming; absent means the
 * sshd default, exactly as before the chooser existed.
 */
export function sshCreateBody(
  common: { name: string; group: string },
  hostId: string,
  directory: string,
): Extract<CreateSessionBody, { target: 'ssh' }> {
  const dir = directory.trim()
  return { ...common, target: 'ssh', hostId, ...(dir ? { directory: dir } : {}) }
}

export type BrowseStatus = 'idle' | 'connecting' | 'ready' | 'error' | 'needsTrust'

export interface BrowseState {
  status: BrowseStatus
  hostId: string
  /** Absolute home directory the first load resolved to; set once ready. */
  path: string
  /** Message shown in the error state. */
  error: string
  /** Set in needsTrust: what the host-key dialog has to show. */
  hostKey: HostKeyErrorDetails | null
}

export interface BrowseDeps {
  fetchDirs: (hostId: string, path: string, signal: AbortSignal) => Promise<DirectoriesResponse>
  closeBrowse: (hostId: string) => Promise<void>
  isAbort: (e: unknown) => boolean
}

// Structural, so this module needs no import of ApiRequestError.
interface MaybeHostKeyError {
  hostKeyDetails?: () => HostKeyErrorDetails | null
}

export function hostKeyOf(e: unknown): HostKeyErrorDetails | null {
  if (typeof e !== 'object' || e === null) return null
  return (e as MaybeHostKeyError).hostKeyDetails?.() ?? null
}

/**
 * supportsStartDir reports whether a host can take a start directory. Windows
 * targets cannot: the remote `cd` wrapper and the SFTP check are POSIX-only.
 */
export function supportsStartDir(host: { targetOS: string; terminalType: string }): boolean {
  if (host.targetOS === 'windows') return false
  return host.terminalType !== 'cmd' && host.terminalType !== 'powershell'
}

export function createHostBrowse(deps: BrowseDeps, onChange: (s: BrowseState) => void = () => {}) {
  const state: BrowseState = { status: 'idle', hostId: '', path: '', error: '', hostKey: null }
  let seq = 0
  let ac: AbortController | null = null
  // True when a request may have opened a server-side connection that has not
  // been closed since.
  let dirty = false

  function set(patch: Partial<BrowseState>) {
    Object.assign(state, patch)
    onChange({ ...state })
  }

  function abortInFlight() {
    seq++
    ac?.abort()
    ac = null
  }

  function closeServerSide(hostId: string) {
    if (!dirty || !hostId) return
    dirty = false
    void deps.closeBrowse(hostId).catch(() => {})
  }

  function reportError(e: unknown) {
    const hk = hostKeyOf(e)
    if (hk) set({ status: 'needsTrust', hostKey: hk, error: '' })
    else set({ status: 'error', hostKey: null, error: e instanceof Error ? e.message : String(e) })
  }

  async function connect(hostId: string) {
    if (state.hostId && state.hostId !== hostId) closeServerSide(state.hostId)
    abortInFlight()
    const mine = seq
    const ctl = new AbortController()
    ac = ctl
    dirty = true
    set({ status: 'connecting', hostId, path: '', error: '', hostKey: null })
    try {
      // "" asks for the host's home directory.
      const res = await deps.fetchDirs(hostId, '', ctl.signal)
      if (mine !== seq) return
      set({ status: 'ready', path: res.path })
    } catch (e) {
      if (mine !== seq || deps.isAbort(e)) return
      reportError(e)
    }
  }

  return {
    get state(): Readonly<BrowseState> {
      return state
    },
    /** Starts (or restarts) for hostId, closing any other host's connection. */
    start: (hostId: string) => connect(hostId),
    /** Retry after an error, or after the host key was trusted. */
    retry: () => (state.hostId ? connect(state.hostId) : Promise.resolve()),
    /** A navigation inside the browser hit an unknown/changed host key. */
    reportHostKey: (e: unknown) => {
      if (state.status === 'idle') return
      reportError(e)
    },
    /** The trust dialog was dismissed without trusting. */
    dismissTrust: () => {
      if (state.status !== 'needsTrust') return
      set({ status: 'error', hostKey: null, error: 'Host key not trusted.' })
    },
    /** Loader for the directory browser; shares the lifecycle's abort signal. */
    load(path: string): Promise<DirectoriesResponse> {
      if (!ac || !state.hostId) return Promise.reject(new Error('Not connected.'))
      dirty = true
      return deps.fetchDirs(state.hostId, path, ac.signal)
    },
    /** Cancels in-flight work, closes the server-side connection (once), resets. */
    stop() {
      abortInFlight()
      closeServerSide(state.hostId)
      set({ status: 'idle', hostId: '', path: '', error: '', hostKey: null })
    },
  }
}

export type HostBrowse = ReturnType<typeof createHostBrowse>
