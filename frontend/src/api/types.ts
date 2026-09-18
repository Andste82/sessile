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
  group: string // free-text label the user files sessions under, "" for none (§4.11)
  taskId: string | null // the task (§4.12) this session runs, null for an ordinary session
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
  | { name: string; target: 'local'; group?: string; directory: string; shell: string }
  | { name: string; target: 'ssh'; group?: string; hostId: string }

// Both fields optional, and both meaningful when present: an omitted one is
// left unchanged, `group: ''` clears the group (§6).
export interface UpdateSessionBody {
  name?: string
  group?: string
}

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
  tasksDir: string // where task folders go on this host (§4.12), default ".sessile/tasks"
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
  tasksDir?: string // "" or omitted: the default
}

// A session's process tree (PROJECT_PLAN.md §4.10, §6).
export interface Process {
  pid: number
  ppid: number
  command: string
  children: Process[]
}

// rootPid is null when processes is a forest rather than one rooted tree
// (scope=all, or an unresolved scope=session) — there is no portable
// single "whole host" root pid to name (Linux's "1"/init convention has no
// Windows equivalent), so the backend reports every process with no
// visible parent as its own root instead of guessing one.
//
// scoped is true when processes is actually narrowed to this session's own
// processes — always true for a local session's default view, but for SSH
// it depends on HostSession.SessionRootPID finding a match (§4.10): an exec
// preamble records the session's own PID for itself, which resolves
// reliably on a POSIX SSH target; a socket-matching fallback covers the
// rest, less reliably. false means processes is the whole target instead,
// honestly labeled rather than presented as if it were narrowed.
export interface ProcessTreeResponse {
  rootPid: number | null
  scoped: boolean
  processes: Process[]
}

// One entry from a session's file browser (§4.10, §6). For a local session,
// name/path are relative to the shared local-host workspace root, same
// convention as DirectoriesResponse. For an SSH session there is no
// workspace root — path is whatever the target's own filesystem uses.
export interface HostDirEntry {
  name: string
  isDir: boolean
  // False for a special file (a device, a FIFO) — size has no relation to
  // what reading it actually produces for one of those, unlike a regular
  // file's.
  isRegular: boolean
  size: number
  modTime: string // RFC 3339 UTC
}

export interface HostFilesResponse {
  path: string
  // The same directory as `path`, but as the target itself names it. Equal to
  // `path` for SSH; the real path on the server for a local session, whose
  // `path` is relative to the workspace root.
  absolutePath: string
  entries: HostDirEntry[]
}

/** Where the session's shell currently is, absolute on the target. */
export interface HostCwdResponse {
  path: string
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

// ---- Agent settings (PROJECT_PLAN.md §4.13, §4.16) ----

export type AgentName = 'claude' | 'codex' | 'gemini'
export type ConnectionFieldType = 'string' | 'url' | 'secret'

export interface ConnectionField {
  name: string
  label: string
  type: ConnectionFieldType
  required: boolean
  help?: string
  env?: string
}

// One fixed way of authenticating one agent; the table lives in code
// (internal/agents/kinds.go).
export interface ConnectionKind {
  id: string
  agent: AgentName
  label: string
  enterprise: boolean
  steps: string[]
  fields: ConnectionField[]
  fixedEnv?: Record<string, string>
  testable: boolean
  listsModels: boolean
  aliases?: string[]
  modelField?: string
  defaultExpiryDays?: number
}

export interface Connection {
  id: string
  name: string
  kind: string
  agent: AgentName
  fields: Record<string, string> // non-secret fields only
  secretsSet: Record<string, boolean> // secret field -> is it set
  expires: string | null // RFC 3339 UTC
  expired: boolean
  expiresSoon: boolean
}

export interface Profile {
  id: string
  name: string
  agent: AgentName
  connectionId: string
  model: string // "" = the resolved default
}

export interface TaskDefaults {
  hostId: string // "" none, "local" for the server itself
  profileId: string
}

export interface GitAccount {
  id: string
  host: string
  name: string
  email: string
  username: string
  hasToken: boolean
}

export interface AgentSettings {
  connections: Connection[]
  profiles: Profile[]
  taskDefaults: TaskDefaults
  git: GitAccount[]
}

// PUT body: a secret that is omitted keeps the saved value of the item with
// the same id; ids may be client-chosen UUIDs so a profile can reference a
// connection that is new in the same request.
export interface AgentSettingsBody {
  connections: {
    id: string
    name: string
    kind: string
    fields: Record<string, string>
    expires?: string // YYYY-MM-DD or RFC 3339; omitted/"" = no expiry
  }[]
  profiles: { id: string; name: string; connectionId: string; model: string }[]
  taskDefaults: TaskDefaults
  git: { id: string; host: string; name: string; email: string; username: string; token?: string }[]
}

export interface TestResult {
  ok: boolean
  detail?: string
  error?: string
}

export interface ModelInfo {
  id: string
  name: string
}

export interface ModelsResponse {
  models: ModelInfo[]
  listed: boolean // false: the kind's built-in aliases
  error?: string
  defaultModel: string
  defaultSource: 'connection' | 'cli'
}
