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
  command: string // foreground program, or "bash › ping" for a script and what it started; "" if unknown
  cwd: string // working directory relative to root, "" if unknown
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
  path: string // cleaned relative path being listed ("." = root)
  parent: string | null // parent relative path, or null at the root
  directories: string[] // immediate subdirectory names, sorted
}

export interface ApiError {
  error: { code: string; message: string }
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
