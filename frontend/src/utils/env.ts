// Pure logic behind the environment table (issue #123): rows from server data,
// the request bodies built from rows, and the structural client-side checks.
//
// Framework-free so it is testable without a DOM. The client checks are
// deliberately structural only (name shape, duplicates, NUL, entry count): the
// server stays authoritative for reserved names and size limits, and its
// message is shown inline. Nothing here may log or persist a value.

import type { EnvEntry, EnvVarInfo } from '@/api/types'

export type EnvRowOrigin = 'own' | 'inherited'

export interface EnvRow {
  key: string
  name: string
  value: string
  secret: boolean
  origin: EnvRowOrigin
  /** The server holds a value for this entry (secrets never send it back). */
  isSet: boolean
  /** Inherited rows only: dropped for this session (shown struck through). */
  removed: boolean
  /**
   * The user must type the value again: a stored secret was renamed, had its
   * secret flag turned off, or was overridden, so its old value no longer
   * applies and the server cannot hand it back. Optional extension of the
   * shared row shape; absent means false.
   */
  retype?: boolean
}

// Mirrors the server's envfile limits; the server is authoritative.
export const NAME_RE = /^[A-Za-z_][A-Za-z0-9_]*$/
export const MAX_ENTRIES = 100

let keySeq = 0
export function newRowKey(): string {
  keySeq += 1
  return `env-row-${keySeq}`
}

export function emptyRow(): EnvRow {
  return {
    key: newRowKey(),
    name: '',
    value: '',
    secret: false,
    origin: 'own',
    isSet: false,
    removed: false,
  }
}

function fromInfo(e: EnvVarInfo, origin: EnvRowOrigin): EnvRow {
  return {
    key: newRowKey(),
    name: e.name,
    value: e.secret ? '' : (e.value ?? ''),
    secret: e.secret,
    origin,
    isSet: e.isSet,
    removed: false,
  }
}

/**
 * rowsFromDefaults seeds the create dialog: every user default arrives as an
 * inherited row. The defaults editor on the Settings page passes asOwn, since
 * there the defaults are the rows being edited, not something inherited.
 */
export function rowsFromDefaults(env: EnvVarInfo[], asOwn = false): EnvRow[] {
  return env.map((e) => fromInfo(e, asOwn ? 'own' : 'inherited'))
}

/**
 * seedWithDefaults merges loaded defaults into rows the user may already have
 * edited (a slow response must not replace them): inherited rows go first, and
 * a default whose name the user already uses is dropped, the user's row wins.
 */
export function seedWithDefaults(current: EnvRow[], env: EnvVarInfo[]): EnvRow[] {
  const used = new Set(current.map((r) => r.name))
  return [...rowsFromDefaults(env).filter((r) => !used.has(r.name)), ...current]
}

/** rowsFromSession builds the edit dialog's rows; `inherited` marks defaults. */
export function rowsFromSession(env: EnvVarInfo[]): EnvRow[] {
  return env.map((e) => fromInfo(e, e.inherited ? 'inherited' : 'own'))
}

// A row the user added and never filled in is a placeholder, not an entry.
function isBlank(r: EnvRow): boolean {
  return r.origin === 'own' && r.name.trim() === '' && r.value === '' && !r.isSet
}

/** Rows that take part in a request: not removed, not an empty placeholder. */
export function activeRows(rows: EnvRow[]): EnvRow[] {
  return rows.filter((r) => !r.removed && !isBlank(r))
}

/**
 * createBodyParts turns the create dialog's rows into the deviations from the
 * user's defaults. An unchanged inherited row sends nothing (the server merges
 * the defaults itself); a removed one is listed in removeDefaults; own rows
 * (including an overridden default, which the server lets win by name) go in
 * `env` with their value. Keys are omitted entirely when empty.
 */
export function createBodyParts(rows: EnvRow[]): { env?: EnvEntry[]; removeDefaults?: string[] } {
  const env: EnvEntry[] = []
  const removeDefaults: string[] = []
  for (const r of rows) {
    if (r.origin === 'inherited') {
      if (r.removed) removeDefaults.push(r.name)
      continue
    }
    if (r.removed || isBlank(r)) continue
    env.push({ name: r.name.trim(), value: r.value, secret: r.secret })
  }
  return {
    ...(env.length ? { env } : {}),
    ...(removeDefaults.length ? { removeDefaults } : {}),
  }
}

/**
 * envBodyParts is what the create dialog spreads into the body. When the host
 * cannot take an environment nothing at all is sent (the server would answer
 * 400), whatever the rows hold.
 */
export function envBodyParts(
  rows: EnvRow[],
  loadDotenv: boolean,
  supported: boolean,
): { env?: EnvEntry[]; removeDefaults?: string[]; loadDotenv?: boolean } {
  if (!supported) return {}
  return { ...createBodyParts(rows), ...(loadDotenv ? { loadDotenv: true } : {}) }
}

/**
 * patchEntries builds the FULL replacement list for PATCH /api/sessions/:id
 * and PUT /api/env-defaults. A set secret that was left blank omits its value,
 * which tells the server to keep the stored one; everything else carries its
 * value. Removed and placeholder rows are left out, which deletes them.
 */
export function patchEntries(rows: EnvRow[]): EnvEntry[] {
  return activeRows(rows).map((r) => {
    const name = r.name.trim()
    if (r.secret && r.isSet && r.value === '' && !r.retype) {
      return { name, secret: true }
    }
    return { name, value: r.value, secret: r.secret }
  })
}

/**
 * validateRows runs the structural checks and returns the first problem as a
 * message (names and counts only, never a value), or null when fine.
 */
export function validateRows(rows: EnvRow[]): string | null {
  const active = activeRows(rows)
  if (active.length > MAX_ENTRIES) return `At most ${MAX_ENTRIES} variables are allowed.`
  const seen = new Set<string>()
  for (const r of active) {
    const name = r.name.trim()
    if (name === '') return 'Every variable needs a name.'
    if (!NAME_RE.test(name)) {
      return `"${name}" is not a valid name (letters, digits and underscore; not starting with a digit).`
    }
    if (seen.has(name)) return `"${name}" appears more than once.`
    seen.add(name)
    if (r.value.includes('\0')) return `The value of ${name} must not contain a NUL character.`
    if (r.retype && r.value === '') return `Type the value of ${name} again.`
  }
  return null
}

// --- row transitions used by EnvTable ---------------------------------------

/** Renaming a stored secret orphans its value: the new name needs a fresh one. */
export function renameRow(row: EnvRow, name: string): EnvRow {
  if (row.secret && row.isSet) return { ...row, name, isSet: false, retype: true, value: '' }
  return { ...row, name }
}

/** Flips the secret flag; turning it off on a stored secret needs a retype. */
export function toggleSecret(row: EnvRow): EnvRow {
  if (row.secret) {
    if (row.isSet) return { ...row, secret: false, isSet: false, retype: true, value: '' }
    return { ...row, secret: false }
  }
  return { ...row, secret: true }
}

/**
 * Override turns an inherited row into an own entry of the same name. A
 * secret's value is not known to the browser, so it has to be typed.
 */
export function overrideRow(row: EnvRow): EnvRow {
  if (row.secret) {
    return { ...row, origin: 'own', isSet: false, retype: true, value: '', removed: false }
  }
  return { ...row, origin: 'own', removed: false }
}

/** Typing a value satisfies a pending retype. */
export function setRowValue(row: EnvRow, value: string): EnvRow {
  return { ...row, value, ...(value !== '' ? { retype: false } : {}) }
}
