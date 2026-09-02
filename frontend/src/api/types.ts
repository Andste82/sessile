// TypeScript types mirroring the JSON shapes in PROJECT_PLAN.md §6.
// Keep these in exact sync with the backend responses.

export type Status = 'running' | 'stopped'
export type TargetType = 'local' | 'ssh'

export interface Session {
  id: string
  name: string
  targetType: TargetType
  directory: string // local only
  shell: string // local only
  hostId: string // ssh only
  hostDisplayName: string // ssh only — snapshotted at creation, survives a host rename/delete
  status: Status
  pid: number
  created: string // RFC 3339 UTC
  lastActivity: string // RFC 3339 UTC
  rows: number
  cols: number
  clientCount: number
  command: string // foreground program, or "bash › ping" for a script and what it started; "" if unknown or ssh
  cwd: string // working directory relative to root, "" if unknown or ssh
  title: string // window title the running program set for itself (OSC 0/2), "" if none
}

// Discriminated on target: "local" needs directory+shell, "ssh" needs hostId.
export type CreateSessionBody =
  | { name: string; target: 'local'; directory: string; shell: string }
  | { name: string; target: 'ssh'; hostId: string }

export interface AppConfig {
  shells: string[]
  version: string
  allowLocalHost: boolean
}

export interface DirectoriesResponse {
  path: string // cleaned relative path being listed ("." = root)
  parent: string | null // parent relative path, or null at the root
  directories: string[] // immediate subdirectory names, sorted
}

export interface ApiError {
  error: { code: string; message: string }
}

// The 409 shape session creation/restart and the host-key endpoints share
// when a host's key is unrecognized or has changed (§4.5.1).
export interface HostKeyErrorDetails {
  keyType: string
  fingerprint: string
  previousFingerprint?: string
}

export interface HostKeyProbeResponse {
  keyType: string
  fingerprint: string
  status: 'new' | 'unchanged' | 'changed'
  previousFingerprint?: string
}

// Response from POST /api/hosts/:id/exchange-keys (§4.5.2) — describes the
// newly generated key, not the host's own host key.
export interface ExchangeKeysResponse {
  success: boolean
  keyType: string
  fingerprint: string
}

export interface User {
  id: string
  username: string
  isAdmin: boolean
}

export interface AuthStatus {
  needsSetup: boolean
  allowRegistration: boolean
  displayName: string
  version: string
}

export interface Credentials {
  username: string
  password: string
}

export interface AdminConfig {
  displayName: string
  allowRegistration: boolean
  allowLocalHost: boolean
}

export type AuthMethod = 'password' | 'privateKey'
export type TargetOS = 'linux' | 'darwin' | 'windows' | 'other'

export interface Host {
  id: string
  name: string
  group: string
  address: string
  username: string
  authMethod: AuthMethod
  hasPassword: boolean
  hasPrivateKey: boolean
  targetOS: TargetOS | ''
  terminalType: string
  customCommand: string
  trustedHostKeyType: string
  trustedHostKeyFingerprint: string // empty means "not yet trusted" (§4.5.1)
  created: string // RFC 3339 UTC
}

// Used for both create and update — an omitted secret field on update means
// "leave unchanged" (mirrors the backend's *string "was this key present"
// distinction: JSON.stringify simply drops an undefined property).
export interface HostBody {
  name: string
  group: string
  address: string
  username: string
  authMethod: AuthMethod
  password?: string
  privateKey?: string
  privateKeyPassphrase?: string
  targetOS: TargetOS | ''
  terminalType: string
  customCommand: string
}

// A session's process tree (PROJECT_PLAN.md §4.10, §6). For a local
// session, processes are the real descendants of the session's own shell.
// For an SSH session there is no reliable way to learn the remote shell's
// own PID over the protocol (§4.10's design note), so rootPid is always 1
// there and processes is the whole target's tree, not just this session's.
export interface Process {
  pid: number
  ppid: number
  command: string
  children: Process[]
}

// scoped is true when processes is actually narrowed to this session's own
// processes — always true for a local session's default view, but for SSH
// it depends on HostSession.SessionRootPID finding a match (§4.10), which
// on a stock OpenSSH target usually can't (its per-connection process is
// commonly non-dumpable, hiding its pid from everyone but root). false
// means processes is the whole target instead, honestly labeled rather
// than presented as if it were narrowed.
export interface ProcessTreeResponse {
  rootPid: number
  scoped: boolean
  processes: Process[]
}

// One entry from a session's file browser (§4.10, §6). For a local session,
// name/path are relative to the shared local-host workspace root, same
// convention as DirectoriesResponse. For an SSH session there is no
// sandbox root — path is whatever the target's own filesystem uses.
export interface HostDirEntry {
  name: string
  isDir: boolean
  size: number
  modTime: string // RFC 3339 UTC
}

export interface HostFilesResponse {
  path: string
  entries: HostDirEntry[]
}

// Poll fallback for a Delete/Copy's progress (§5.2) — the same shape the WS
// hostop* events carry, collapsed into one snapshot.
export interface HostopStatus {
  opId: string
  kind: 'delete' | 'copy'
  done: number
  total: number
  status: 'running' | 'ok' | 'error'
  message?: string
}
