// Typed fetch wrappers around the REST API (PROJECT_PLAN.md §6).
import type {
  AdminConfig,
  AgentSettings,
  AgentSettingsBody,
  ConnectionKind,
  GitImportResponse,
  Note,
  NoteContext,
  ModelsResponse,
  RestartOptions,
  Script,
  ScriptCheck,
  ScriptExample,
  ScriptList,
  ScriptRunResult,
  ScriptSettingsBody,
  Task,
  TaskSpec,
  TestResult,
  AppConfig,
  AuthStatus,
  Credentials,
  CreateSessionBody,
  DirectoriesResponse,
  ExchangeKeysResponse,
  Host,
  HostBody,
  HostKeyErrorDetails,
  HostFilesResponse,
  HostCwdResponse,
  HostKeyProbeResponse,
  HostopStatus,
  ProcessTreeResponse,
  Session,
  UpdateSessionBody,
  User,
} from './types'

/**
 * Error carrying the backend's structured {code,message} envelope. `details`
 * holds any extra fields the error object carried beyond code/message — in
 * practice just the host-key responses (§4.5.1), which add keyType/
 * fingerprint/previousFingerprint alongside the standard two.
 */
export class ApiRequestError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public details?: Record<string, unknown>,
  ) {
    super(message)
    this.name = 'ApiRequestError'
  }

  // Narrows details to the host-key shape when code says this is one of
  // those responses — the two call sites that create sessions/restart are
  // the only ones that need this, so it lives here rather than duplicating
  // the check at each of them.
  hostKeyDetails(): HostKeyErrorDetails | null {
    if (this.code !== 'host_key_unverified' && this.code !== 'host_key_changed') return null
    return this.details as unknown as HostKeyErrorDetails
  }
}

// isAlreadyRunning reports whether a failed restart failed only because the
// session is already running — another browser on the same session started it
// first (§6). The caller wanted a live session and there is one, so this is a
// cue to reconnect, not an error to show.
export function isAlreadyRunning(e: unknown): boolean {
  return e instanceof ApiRequestError && e.code === 'already_running'
}

// isUnsupportedPlatform reports whether a hostops request (§4.10) failed
// only because the target has no Platform support yet (e.g. a Windows SSH
// target before windowsPlatform is wired up for it) — worth a distinct,
// calmer message than a generic error.
export function isUnsupportedPlatform(e: unknown): boolean {
  return e instanceof ApiRequestError && e.code === 'unsupported_platform'
}

// unauthorizedHandler fires whenever a request comes back 401. The auth store
// wires itself up here (in main.ts) so a lapsed or missing session cookie
// sends the app back to /login from wherever request() was called — every
// call site would otherwise need to check for this itself. A plain callback
// rather than an import of the store, which would make this module depend on
// Pinia being installed before any request can even fail.
let unauthorizedHandler: (() => void) | null = null
export function setUnauthorizedHandler(fn: () => void) {
  unauthorizedHandler = fn
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    headers: { 'Content-Type': 'application/json' },
    ...init,
  })
  if (res.status === 204) return undefined as T
  const text = await res.text()
  const body = text ? JSON.parse(text) : undefined
  if (!res.ok) {
    const code = body?.error?.code ?? 'internal'
    const message = body?.error?.message ?? res.statusText
    const { code: _code, message: _message, ...details } = body?.error ?? {}
    if (res.status === 401) unauthorizedHandler?.()
    throw new ApiRequestError(res.status, code, message, Object.keys(details).length ? details : undefined)
  }
  return body as T
}

export const api = {
  health: () => request<{ status: string }>('/api/health'),
  config: () => request<AppConfig>('/api/config'),
  directories: (path?: string) =>
    request<DirectoriesResponse>(
      path ? `/api/directories?path=${encodeURIComponent(path)}` : '/api/directories',
    ),
  listSessions: () => request<Session[]>('/api/sessions'),
  getSession: (id: string) => request<Session>(`/api/sessions/${id}`),
  createSession: (body: CreateSessionBody) =>
    request<Session>('/api/sessions', {
      method: 'POST',
      body: JSON.stringify(body),
    }),
  deleteSession: (id: string) =>
    request<void>(`/api/sessions/${id}`, { method: 'DELETE' }),
  // Only the fields present in `body` are changed; JSON.stringify drops an
  // undefined property, which is exactly the omitted-means-unchanged shape the
  // endpoint expects (§6). `group: ''` is a value, and clears the group.
  updateSession: (id: string, body: UpdateSessionBody) =>
    request<Session>(`/api/sessions/${id}`, {
      method: 'PATCH',
      body: JSON.stringify(body),
    }),
  // Options only mean something for a task session (§4.12.6); an ordinary
  // restart sends no body at all.
  restartSession: (id: string, opts?: RestartOptions) =>
    request<Session>(`/api/sessions/${id}/restart`, {
      method: 'POST',
      ...(opts && (opts.fresh || opts.rebuildContainer) ? { body: JSON.stringify(opts) } : {}),
    }),
  createTask: (spec: TaskSpec) =>
    request<Session>('/api/tasks', { method: 'POST', body: JSON.stringify(spec) }),
  getTask: (id: string) => request<Task>(`/api/tasks/${id}`),
  processTree: (id: string, scope?: 'session' | 'all') =>
    request<ProcessTreeResponse>(
      `/api/sessions/${id}/hostops/process-tree${scope ? `?scope=${scope}` : ''}`,
    ),
  listHostFiles: (id: string, path?: string) =>
    request<HostFilesResponse>(
      `/api/sessions/${id}/hostops/files${path ? `?path=${encodeURIComponent(path)}` : ''}`,
    ),
  sessionCwd: (id: string) => request<HostCwdResponse>(`/api/sessions/${id}/hostops/cwd`),
  moveHostFile: (id: string, src: string, dst: string) =>
    request<void>(`/api/sessions/${id}/hostops/move`, {
      method: 'POST',
      body: JSON.stringify({ src, dst }),
    }),
  copyHostFile: (id: string, src: string, dst: string) =>
    request<{ opId: string }>(`/api/sessions/${id}/hostops/copy`, {
      method: 'POST',
      body: JSON.stringify({ src, dst }),
    }),
  deleteHostFile: (id: string, path: string) =>
    request<{ opId: string }>(`/api/sessions/${id}/hostops/files?path=${encodeURIComponent(path)}`, {
      method: 'DELETE',
    }),
  hostopStatus: (id: string, opId: string) =>
    request<HostopStatus>(`/api/sessions/${id}/hostops/ops/${opId}`),

  authStatus: () => request<AuthStatus>('/api/auth/status'),
  bootstrap: (creds: Credentials) =>
    request<User>('/api/auth/bootstrap', { method: 'POST', body: JSON.stringify(creds) }),
  register: (creds: Credentials) =>
    request<User>('/api/auth/register', { method: 'POST', body: JSON.stringify(creds) }),
  login: (creds: Credentials) =>
    request<User>('/api/auth/login', { method: 'POST', body: JSON.stringify(creds) }),
  logout: () => request<void>('/api/auth/logout', { method: 'POST' }),
  me: () => request<User>('/api/auth/me'),
  getAdminConfig: () => request<AdminConfig>('/api/admin/config'),
  updateAdminConfig: (cfg: AdminConfig) =>
    request<AdminConfig>('/api/admin/config', { method: 'PUT', body: JSON.stringify(cfg) }),

  listHosts: () => request<Host[]>('/api/hosts'),
  getHost: (id: string) => request<Host>(`/api/hosts/${id}`),
  createHost: (body: HostBody) =>
    request<Host>('/api/hosts', { method: 'POST', body: JSON.stringify(body) }),
  updateHost: (id: string, body: HostBody) =>
    request<Host>(`/api/hosts/${id}`, { method: 'PUT', body: JSON.stringify(body) }),
  deleteHost: (id: string) => request<void>(`/api/hosts/${id}`, { method: 'DELETE' }),
  probeHostKey: (id: string) =>
    request<HostKeyProbeResponse>(`/api/hosts/${id}/host-key/probe`, { method: 'POST' }),
  trustHostKey: (id: string, body: { fingerprint: string; keyType: string }) =>
    request<void>(`/api/hosts/${id}/host-key/trust`, {
      method: 'POST',
      body: JSON.stringify(body),
    }),
  exchangeHostKeys: (id: string, body: { username: string; password: string }) =>
    request<ExchangeKeysResponse>(`/api/hosts/${id}/exchange-keys`, {
      method: 'POST',
      body: JSON.stringify(body),
    }),

  connectionKinds: () => request<ConnectionKind[]>('/api/agent/connection-kinds'),
  agentSettings: () => request<AgentSettings>('/api/agent/settings'),
  putAgentSettings: (body: AgentSettingsBody) =>
    request<AgentSettings>('/api/agent/settings', { method: 'PUT', body: JSON.stringify(body) }),
  testConnection: (body: { id?: string; kind: string; fields: Record<string, string> }) =>
    request<TestResult>('/api/agent/connections/test', { method: 'POST', body: JSON.stringify(body) }),
  connectionModels: (id: string, refresh = false) =>
    request<ModelsResponse>(`/api/agent/connections/${id}/models${refresh ? '?refresh=1' : ''}`),
  listNotes: () => request<Note[]>('/api/agent/notes'),
  getNote: (slug: string) => request<Note>(`/api/agent/notes/${encodeURIComponent(slug)}`),
  putNote: (slug: string, context: NoteContext, body: string) =>
    request<Note>(`/api/agent/notes/${encodeURIComponent(slug)}`, {
      method: 'PUT',
      body: JSON.stringify({ context, body }),
    }),
  deleteNote: (slug: string) =>
    request<void>(`/api/agent/notes/${encodeURIComponent(slug)}`, { method: 'DELETE' }),
  listScripts: () => request<ScriptList>('/api/agent/scripts'),
  listScriptExamples: () => request<ScriptExample[]>('/api/agent/script-examples'),
  installScriptExample: (name: string, opts: { as?: string; update?: boolean } = {}) => {
    const q = new URLSearchParams()
    if (opts.as) q.set('as', opts.as)
    if (opts.update) q.set('update', 'true')
    return request<Script>(`/api/agent/script-examples/${name}/install${q.size ? `?${q}` : ''}`, { method: 'POST' })
  },
  putScriptSettings: (name: string, body: ScriptSettingsBody) =>
    request<Script>(`/api/agent/scripts/${name}/settings`, { method: 'PUT', body: JSON.stringify(body) }),
  checkScript: (name: string, body?: ScriptSettingsBody) =>
    request<ScriptCheck>(`/api/agent/scripts/${name}/check`, {
      method: 'POST',
      ...(body ? { body: JSON.stringify(body) } : {}),
    }),
  runScript: (name: string, fn: string, input: unknown) =>
    request<ScriptRunResult>(`/api/agent/scripts/${name}/run`, {
      method: 'POST',
      body: JSON.stringify({ function: fn, input }),
    }),
  rebuildScript: (name: string) => request<Script>(`/api/agent/scripts/${name}/rebuild`, { method: 'POST' }),
  removeScript: (name: string) => request<void>(`/api/agent/scripts/${name}`, { method: 'DELETE' }),
  // A zip goes up as the raw body, not JSON (§4.15.1).
  uploadScript: (zip: Blob, opts: { as?: string; update?: boolean } = {}) => {
    const q = new URLSearchParams()
    if (opts.as) q.set('as', opts.as)
    if (opts.update) q.set('update', 'true')
    return request<Script>(`/api/agent/scripts${q.size ? `?${q}` : ''}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/zip' },
      body: zip,
    })
  },
  importGitIdentity: (hostId: string, gitHost: string) =>
    request<GitImportResponse>('/api/agent/git/import', {
      method: 'POST',
      body: JSON.stringify({ hostId, gitHost }),
    }),
  testGitAccount: (body: { id?: string; host: string; token?: string }) =>
    request<TestResult>('/api/agent/git/test', { method: 'POST', body: JSON.stringify(body) }),

  listUsers: () => request<User[]>('/api/admin/users'),
  deleteUser: (id: string) => request<void>(`/api/admin/users/${id}`, { method: 'DELETE' }),
  setUserAdmin: (id: string, isAdmin: boolean) =>
    request<User>(`/api/admin/users/${id}`, {
      method: 'PATCH',
      body: JSON.stringify({ isAdmin }),
    }),
}
