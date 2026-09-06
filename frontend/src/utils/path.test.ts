import { describe, expect, it } from 'vitest'
import { relativeTo } from './path'

describe('relativeTo', () => {
  it('strips the shared prefix for a file below', () => {
    expect(relativeTo('/home/me', '/home/me/notes/a.txt')).toBe('notes/a.txt')
  })

  it('names the directory itself as "."', () => {
    expect(relativeTo('/home/me', '/home/me')).toBe('.')
  })

  it('walks up for a sibling', () => {
    expect(relativeTo('/home/me/project', '/home/me/logs/a.txt')).toBe('../logs/a.txt')
  })

  it('walks up more than once', () => {
    expect(relativeTo('/a/b/c/d', '/a/x.txt')).toBe('../../../x.txt')
  })

  it('handles the root as a base', () => {
    expect(relativeTo('/', '/etc/hosts')).toBe('etc/hosts')
  })

  it('ignores trailing slashes and "." segments', () => {
    expect(relativeTo('/home/me/', '/home/./me/a.txt')).toBe('a.txt')
  })

  // Two paths on one machine always share "/", so this is only about staying
  // total rather than a case that occurs.
  it('falls back to the target when either path is not absolute', () => {
    expect(relativeTo('relative', '/home/me/a.txt')).toBe('/home/me/a.txt')
  })
})
