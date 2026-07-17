// Typed fetch wrappers around the REST API (PROJECT_PLAN.md §6).
import type {
  AppConfig,
  CreateSessionBody,
  DirectoriesResponse,
  Session,
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
}
