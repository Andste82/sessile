import { describe, expect, it } from 'vitest'
import { joinRemote, parentRemote, relativeTo, remoteCrumbs } from './path'

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

describe('remote path helpers', () => {
  it.each([
    ['/', []],
    ['/home/me', [{ name: 'home', path: '/home' }, { name: 'me', path: '/home/me' }]],
    ['/a//b/', [{ name: 'a', path: '/a' }, { name: 'b', path: '/a/b' }]],
  ])('remoteCrumbs(%s)', (input, want) => {
    expect(remoteCrumbs(input)).toEqual(want)
  })

  it.each([
    ['/', 'etc', '/etc'],
    ['/home', 'me', '/home/me'],
    ['/home/', 'me', '/home/me'],
  ])('joinRemote(%s, %s)', (base, name, want) => {
    expect(joinRemote(base, name)).toBe(want)
  })

  it.each([
    ['/', null],
    ['/home', '/'],
    ['/home/me', '/home'],
  ])('parentRemote(%s)', (input, want) => {
    expect(parentRemote(input)).toBe(want)
  })
})
