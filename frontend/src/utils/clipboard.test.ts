import { describe, expect, it } from 'vitest'
import {
  isApplePlatform,
  isCopyShortcut,
  isPasteInput,
  isPasteShortcut,
  pastedText,
  type KeyChord,
} from './clipboard'

function chord(over: Partial<KeyChord> = {}): KeyChord {
  return {
    type: 'keydown',
    key: 'v',
    ctrlKey: false,
    metaKey: false,
    shiftKey: false,
    altKey: false,
    ...over,
  }
}

describe('isPasteShortcut', () => {
  const cases: { name: string; e: KeyChord; apple: boolean; want: boolean }[] = [
    {
      name: 'Ctrl+V pastes off Apple platforms',
      e: chord({ ctrlKey: true }),
      apple: false,
      want: true,
    },
    {
      name: 'Ctrl+Shift+V pastes too',
      e: chord({ ctrlKey: true, shiftKey: true, key: 'V' }),
      apple: false,
      want: true,
    },
    {
      name: 'Shift+Insert pastes',
      e: chord({ key: 'Insert', shiftKey: true }),
      apple: false,
      want: true,
    },
    {
      name: 'Cmd+V pastes on Apple platforms',
      e: chord({ metaKey: true }),
      apple: true,
      want: true,
    },
    // Everything the terminal still owns.
    {
      name: 'plain v is typing',
      e: chord(),
      apple: false,
      want: false,
    },
    {
      name: 'Ctrl+V stays quoted-insert on Apple platforms',
      e: chord({ ctrlKey: true }),
      apple: true,
      want: false,
    },
    {
      name: 'Cmd+V is not a chord off Apple platforms',
      e: chord({ metaKey: true }),
      apple: false,
      want: false,
    },
    {
      name: 'AltGr+V is a character, not a chord',
      e: chord({ ctrlKey: true, altKey: true }),
      apple: false,
      want: false,
    },
    {
      name: 'Ctrl+C is not a paste',
      e: chord({ key: 'c', ctrlKey: true }),
      apple: false,
      want: false,
    },
    {
      name: 'unshifted Insert stays a terminal key',
      e: chord({ key: 'Insert' }),
      apple: false,
      want: false,
    },
    {
      name: 'keyup carries no default action to preserve',
      e: chord({ type: 'keyup', ctrlKey: true }),
      apple: false,
      want: false,
    },
  ]

  for (const c of cases) {
    it(c.name, () => {
      expect(isPasteShortcut(c.e, c.apple)).toBe(c.want)
    })
  }
})

describe('isCopyShortcut', () => {
  const cases: { name: string; e: KeyChord; apple: boolean; want: boolean }[] = [
    {
      name: 'Ctrl+Shift+C copies',
      e: chord({ key: 'C', ctrlKey: true, shiftKey: true }),
      apple: false,
      want: true,
    },
    {
      name: 'Ctrl+Insert copies',
      e: chord({ key: 'Insert', ctrlKey: true }),
      apple: false,
      want: true,
    },
    // Ctrl+C is SIGINT and nothing else, selection or not.
    {
      name: 'Ctrl+C is never a copy',
      e: chord({ key: 'c', ctrlKey: true }),
      apple: false,
      want: false,
    },
    {
      name: 'Cmd+C is left to the browser on Apple platforms',
      e: chord({ key: 'c', metaKey: true }),
      apple: true,
      want: false,
    },
    {
      name: 'Ctrl+Shift+C is left to Cmd+C on Apple platforms',
      e: chord({ key: 'C', ctrlKey: true, shiftKey: true }),
      apple: true,
      want: false,
    },
    {
      name: 'Shift+Insert is a paste, not a copy',
      e: chord({ key: 'Insert', shiftKey: true }),
      apple: false,
      want: false,
    },
    {
      name: 'keyup carries no default action to preserve',
      e: chord({ type: 'keyup', key: 'C', ctrlKey: true, shiftKey: true }),
      apple: false,
      want: false,
    },
  ]

  for (const c of cases) {
    it(c.name, () => {
      expect(isCopyShortcut(c.e, c.apple)).toBe(c.want)
    })
  }

  it('never collides with a paste chord', () => {
    const chords = [
      chord({ ctrlKey: true }),
      chord({ key: 'V', ctrlKey: true, shiftKey: true }),
      chord({ key: 'Insert', shiftKey: true }),
      chord({ key: 'Insert', ctrlKey: true }),
      chord({ key: 'C', ctrlKey: true, shiftKey: true }),
    ]
    for (const apple of [false, true]) {
      for (const e of chords) {
        expect(isPasteShortcut(e, apple) && isCopyShortcut(e, apple)).toBe(false)
      }
    }
  })
})

describe('isPasteInput', () => {
  it('accepts clipboard insertions', () => {
    expect(isPasteInput('insertFromPaste')).toBe(true)
    expect(isPasteInput('insertFromPasteAsQuotation')).toBe(true)
  })

  it('rejects typing, composition and empty inputTypes', () => {
    expect(isPasteInput('insertText')).toBe(false)
    expect(isPasteInput('insertCompositionText')).toBe(false)
    expect(isPasteInput('')).toBe(false)
  })
})

describe('pastedText', () => {
  it('prefers data', () => {
    expect(pastedText({ data: 'ls -la' })).toBe('ls -la')
  })

  it('falls back to the data transfer', () => {
    expect(
      pastedText({ data: null, dataTransfer: { getData: () => 'echo hi' } })
    ).toBe('echo hi')
  })

  it('reports nothing when the event carries no text', () => {
    expect(pastedText({})).toBe('')
    expect(pastedText({ data: '', dataTransfer: { getData: () => '' } })).toBe('')
  })
})

describe('isApplePlatform', () => {
  it('detects macOS and iOS', () => {
    expect(isApplePlatform({ platform: 'MacIntel' })).toBe(true)
    expect(isApplePlatform({ platform: 'iPhone' })).toBe(true)
  })

  it('reads the user agent when platform is unavailable', () => {
    expect(isApplePlatform({ userAgent: 'Mozilla/5.0 (Macintosh)' })).toBe(true)
    expect(isApplePlatform({ userAgent: 'Mozilla/5.0 (X11; Linux x86_64)' })).toBe(
      false
    )
  })

  it('defaults to non-Apple', () => {
    expect(isApplePlatform({})).toBe(false)
  })
})
