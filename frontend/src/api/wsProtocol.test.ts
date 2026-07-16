import { describe, it, expect } from 'vitest'
import { parseControl, encodeResize } from './wsProtocol'

describe('parseControl', () => {
  it('parses an attached frame', () => {
    expect(
      parseControl('{"type":"attached","sessionId":"abc","replayBytes":42}'),
    ).toEqual({ type: 'attached', sessionId: 'abc', replayBytes: 42 })
  })

  it('parses an exit frame', () => {
    expect(parseControl('{"type":"exit"}')).toEqual({ type: 'exit' })
  })

  it('parses an error frame', () => {
    expect(parseControl('{"type":"error","message":"boom"}')).toEqual({
      type: 'error',
      message: 'boom',
    })
  })

  it('rejects malformed JSON', () => {
    expect(parseControl('not json')).toBeNull()
  })

  it('rejects unknown types', () => {
    expect(parseControl('{"type":"nope"}')).toBeNull()
  })

  it('rejects attached missing fields', () => {
    expect(parseControl('{"type":"attached","sessionId":"abc"}')).toBeNull()
  })
})

describe('encodeResize', () => {
  it('encodes cols and rows', () => {
    expect(encodeResize(120, 32)).toBe('{"type":"resize","cols":120,"rows":32}')
  })
})
