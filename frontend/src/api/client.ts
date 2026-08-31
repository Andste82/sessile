// Typed fetch wrappers around the REST API (PROJECT_PLAN.md §6).
import type {
  AdminConfig,
  AppConfig,
  AuthStatus,
  Credentials,
  CreateSessionBody,
  DirectoriesResponse,
  Session,
  User,
} from './types'

/** Error carrying the backend's structured {code,message} envelope. */
export class ApiRequestError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
  ) {
    super(message)
    this.name = 'ApiRequestError'
  }
}

// isAlreadyRunning reports whether a failed restart failed only because the
// session is already running — another browser on the same session started it
// first (§6). The caller wanted a live session and there is one, so this is a
// cue to reconnect, not an error to show.
export function isAlreadyRunning(e: unknown): boolean {
  return e instanceof ApiRequestError && e.code === 'already_running'
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
    if (res.status === 401) unauthorizedHandler?.()
    throw new ApiRequestError(res.status, code, message)
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
  renameSession: (id: string, name: string) =>
    request<Session>(`/api/sessions/${id}`, {
      method: 'PATCH',
      body: JSON.stringify({ name }),
    }),
  restartSession: (id: string) =>
    request<Session>(`/api/sessions/${id}/restart`, { method: 'POST' }),

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
}
