// TypeScript types mirroring the JSON shapes in PROJECT_PLAN.md §6.
// Keep these in exact sync with the backend responses.

export type Status = 'running' | 'stopped'

export interface Session {
  id: string
  name: string
  directory: string
  shell: string
  status: Status
  pid: number
  created: string // RFC 3339 UTC
  lastActivity: string // RFC 3339 UTC
  rows: number
  cols: number
  clientCount: number
}

export interface CreateSessionBody {
  name: string
  directory: string
  shell: string
}

export interface AppConfig {
  root: string
  shells: string[]
  version: string
}

export interface DirectoriesResponse {
  directories: string[]
}

export interface ApiError {
  error: { code: string; message: string }
}
