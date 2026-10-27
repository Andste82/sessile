import { describe, it, expect } from 'vitest'
import {
  activeRows,
  createBodyParts,
  emptyRow,
  envBodyParts,
  MAX_ENTRIES,
  overrideRow,
  patchEntries,
  renameRow,
  rowsFromDefaults,
  rowsFromSession,
  seedWithDefaults,
  setRowValue,
  toggleSecret,
  validateRows,
  type EnvRow,
} from './env'

const SENTINEL = 'sentinel-secret-value'

function row(over: Partial<EnvRow> = {}): EnvRow {
  return { ...emptyRow(), ...over }
}

describe('rows', () => {
  it('seeds inherited rows from defaults; a secret has no value', () => {
    const rows = rowsFromDefaults([
      { name: 'A', value: '1', secret: false, isSet: true },
      { name: 'S', secret: true, isSet: true },
    ])
    expect(rows.map((r) => [r.name, r.value, r.secret, r.origin, r.isSet])).toEqual([
      ['A', '1', false, 'inherited', true],
      ['S', '', true, 'inherited', true],
    ])
    expect(new Set(rows.map((r) => r.key)).size).toBe(2)
  })

  it('can seed defaults as own rows for the settings editor', () => {
    expect(rowsFromDefaults([{ name: 'A', value: '1', secret: false, isSet: true }], true)[0].origin).toBe('own')
  })

  it('maps the inherited flag of a session', () => {
    const rows = rowsFromSession([
      { name: 'A', value: '1', secret: false, isSet: true, inherited: true },
      { name: 'B', value: '2', secret: false, isSet: true, inherited: false },
    ])
    expect(rows.map((r) => r.origin)).toEqual(['inherited', 'own'])
  })
})

describe('createBodyParts', () => {
  it('sends nothing for untouched inherited rows', () => {
    const rows = rowsFromDefaults([
      { name: 'A', value: '1', secret: false, isSet: true },
      { name: 'S', secret: true, isSet: true },
    ])
    expect(createBodyParts(rows)).toEqual({})
  })

  it('lists removed defaults and own entries separately', () => {
    const rows = [
      ...rowsFromDefaults([{ name: 'A', value: '1', secret: false, isSet: true }]).map((r) => ({
        ...r,
        removed: true,
      })),
      row({ name: ' B ', value: 'x' }),
      row({ name: 'S', value: SENTINEL, secret: true }),
      row(), // placeholder
    ]
    expect(createBodyParts(rows)).toEqual({
      env: [
        { name: 'B', value: 'x', secret: false },
        { name: 'S', value: SENTINEL, secret: true },
      ],
      removeDefaults: ['A'],
    })
  })

  it('an overridden default becomes an own entry with the new value', () => {
    const [inh] = rowsFromDefaults([{ name: 'A', value: '1', secret: false, isSet: true }])
    const over = setRowValue(overrideRow(inh), '2')
    expect(createBodyParts([over])).toEqual({ env: [{ name: 'A', value: '2', secret: false }] })
  })
})

describe('envBodyParts gating', () => {
  const rows = [row({ name: 'A', value: '1' })]
  it('sends everything for a supported host', () => {
    expect(envBodyParts(rows, true, true)).toEqual({
      env: [{ name: 'A', value: '1', secret: false }],
      loadDotenv: true,
    })
  })
  it('omits loadDotenv when off', () => {
    expect('loadDotenv' in envBodyParts(rows, false, true)).toBe(false)
  })
  it('sends nothing for an unsupported host', () => {
    expect(envBodyParts(rows, true, false)).toEqual({})
  })
})

describe('patchEntries', () => {
  it('omits the value of an untouched set secret (keep)', () => {
    const rows = rowsFromSession([
      { name: 'S', secret: true, isSet: true },
      { name: 'P', value: 'v', secret: false, isSet: true, inherited: true },
    ])
    expect(patchEntries(rows)).toEqual([
      { name: 'S', secret: true },
      { name: 'P', value: 'v', secret: false },
    ])
  })

  it('sends a retyped secret and drops removed and placeholder rows', () => {
    const [s] = rowsFromSession([{ name: 'S', secret: true, isSet: true }])
    const typed = setRowValue(s, SENTINEL)
    const gone = { ...row({ name: 'G', value: '1' }), removed: true }
    expect(patchEntries([typed, gone, row()])).toEqual([{ name: 'S', value: SENTINEL, secret: true }])
  })
})

describe('row transitions', () => {
  const [s] = rowsFromSession([{ name: 'S', secret: true, isSet: true }])

  it('renaming a set secret requires a retype', () => {
    const r = renameRow(s, 'T')
    expect(r).toMatchObject({ name: 'T', isSet: false, retype: true, value: '' })
    expect(validateRows([r])).toMatch(/again/)
    expect(validateRows([setRowValue(r, 'x')])).toBeNull()
  })

  it('turning the secret flag off on a set secret requires a retype', () => {
    const r = toggleSecret(s)
    expect(r.secret).toBe(false)
    expect(validateRows([r])).toMatch(/again/)
    expect(patchEntries([setRowValue(r, 'x')])).toEqual([{ name: 'S', value: 'x', secret: false }])
  })

  it('turning the flag on keeps the value', () => {
    const r = toggleSecret(row({ name: 'A', value: 'v' }))
    expect(r).toMatchObject({ secret: true, value: 'v' })
  })

  it('overriding an inherited secret requires typing it', () => {
    const [inh] = rowsFromDefaults([{ name: 'S', secret: true, isSet: true }])
    const r = overrideRow(inh)
    expect(r.origin).toBe('own')
    expect(validateRows([r])).toMatch(/again/)
  })

  it('plain rename keeps everything else', () => {
    expect(renameRow(row({ name: 'A', value: '1' }), 'B')).toMatchObject({ name: 'B', value: '1' })
  })
})

describe('validateRows', () => {
  const dupA = [row({ name: 'A', value: '1' }), row({ name: 'A', value: '2' })]
  it.each([
    ['empty list', [], null],
    ['valid', [row({ name: 'A_1', value: 'x' })], null],
    ['blank placeholder ignored', [row()], null],
    ['name missing', [row({ value: 'x' })], 'needs a name'],
    ['digit first', [row({ name: '1A', value: 'x' })], 'not a valid name'],
    ['dash', [row({ name: 'A-B', value: 'x' })], 'not a valid name'],
    ['case-sensitive, no dup', [row({ name: 'a', value: 'v' }), row({ name: 'A', value: 'v' })], null],
    ['duplicate', dupA, 'more than once'],
    ['NUL', [row({ name: 'A', value: 'a\0b' })], 'NUL'],
    ['removed duplicate ignored', [dupA[0], { ...dupA[1], removed: true }], null],
  ])('%s', (_n, rows, want) => {
    const got = validateRows(rows as EnvRow[])
    if (want === null) expect(got).toBeNull()
    else expect(got).toContain(want)
  })

  it('caps the entry count', () => {
    const rows = Array.from({ length: MAX_ENTRIES + 1 }, (_, i) => row({ name: `V${i}`, value: 'x' }))
    expect(validateRows(rows)).toContain(String(MAX_ENTRIES))
    expect(validateRows(rows.slice(0, MAX_ENTRIES))).toBeNull()
  })

  it('never puts a value into a message', () => {
    const msgs = [
      validateRows([row({ name: 'A-B', value: SENTINEL })]),
      validateRows([row({ name: 'A', value: SENTINEL }), row({ name: 'A', value: SENTINEL })]),
      validateRows([row({ name: 'A', value: SENTINEL + '\0' })]),
    ]
    for (const m of msgs) expect(m).not.toContain(SENTINEL)
  })
})

describe('activeRows', () => {
  it('drops removed rows and placeholders', () => {
    const keep = row({ name: 'A', value: '1' })
    expect(activeRows([keep, row(), { ...row({ name: 'B', value: '2' }), removed: true }])).toEqual([keep])
  })
})

describe('seedWithDefaults', () => {
  it('keeps rows typed before the defaults arrived and prefers them on a name clash', () => {
    const typed = { ...emptyRow(), name: 'EDITOR', value: 'nano' }
    const typed2 = { ...emptyRow(), name: 'NEW', value: 'x' }
    const out = seedWithDefaults(
      [typed, typed2],
      [
        { name: 'EDITOR', value: 'vim', secret: false, isSet: true },
        { name: 'PAGER', value: 'less', secret: false, isSet: true },
      ],
    )
    expect(out.map((r) => r.name)).toEqual(['PAGER', 'EDITOR', 'NEW'])
    expect(out[0].origin).toBe('inherited')
    expect(out[1].value).toBe('nano')
  })

  it('is just the defaults when nothing was typed', () => {
    const out = seedWithDefaults([], [{ name: 'A', value: '1', secret: false, isSet: true }])
    expect(out).toHaveLength(1)
    expect(out[0].origin).toBe('inherited')
  })
})
