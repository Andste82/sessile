import { describe, expect, it } from 'vitest'
import { uuidv4 } from './uuid'

describe('uuidv4', () => {
  it('matches the shape the backend accepts as a client-chosen id', () => {
    for (let i = 0; i < 50; i++) {
      expect(uuidv4()).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/)
    }
  })
})
