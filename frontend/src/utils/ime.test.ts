import { describe, expect, it } from 'vitest'
import { isCompositionArtifact, isImeKey, shouldFlushIme, type ImeKey } from './ime'

describe('isCompositionArtifact', () => {
  const cases: {
    name: string
    composing: boolean
    inputType: string
    want: boolean
  }[] = [
    // Committed input outside a composition must always reach the terminal.
    {
      name: 'plain typing passes through',
      composing: false,
      inputType: 'insertText',
      want: false,
    },
    {
      name: 'backspace outside a composition passes through',
      composing: false,
      inputType: 'deleteContentBackward',
      want: false,
    },
    {
      name: 'paste passes through',
      composing: false,
      inputType: 'insertFromPaste',
      want: false,
    },
    // Everything emitted while a composition is in flight is a preview.
    {
      name: 'composition preview is withheld',
      composing: true,
      inputType: 'insertCompositionText',
      want: true,
    },
    {
      name: 'Gboard insertText mid-composition is withheld',
      composing: true,
      inputType: 'insertText',
      want: true,
    },
    {
      name: 'Gboard rubbing out a half-typed word is withheld',
      composing: true,
      inputType: 'deleteContentBackward',
      want: true,
    },
    // Some keyboards emit composition input with no compositionstart/end pair.
    {
      name: 'unpaired composition insert is withheld',
      composing: false,
      inputType: 'insertCompositionText',
      want: true,
    },
    {
      name: 'unpaired composition delete is withheld',
      composing: false,
      inputType: 'deleteByComposition',
      want: true,
    },
    // An empty inputType (older WebKit) is only suspect while composing.
    {
      name: 'missing inputType outside a composition passes through',
      composing: false,
      inputType: '',
      want: false,
    },
    {
      name: 'missing inputType while composing is withheld',
      composing: true,
      inputType: '',
      want: true,
    },
  ]

  for (const c of cases) {
    it(c.name, () => {
      expect(isCompositionArtifact(c.composing, c.inputType)).toBe(c.want)
    })
  }
})

function key(over: Partial<ImeKey> = {}): ImeKey {
  return { type: 'keydown', key: 'a', keyCode: 65, isComposing: false, ...over }
}

describe('isImeKey', () => {
  const cases: { name: string; e: ImeKey; want: boolean }[] = [
    {
      name: 'the Android composing keystroke belongs to the keyboard',
      e: key({ key: 'Unidentified', keyCode: 229 }),
      want: true,
    },
    {
      name: 'a key flagged as composing belongs to the keyboard',
      e: key({ key: 'Enter', keyCode: 13, isComposing: true }),
      want: true,
    },
    {
      name: 'the Process key belongs to the keyboard',
      e: key({ key: 'Process', keyCode: 0 }),
      want: true,
    },
    {
      name: 'ordinary typing does not',
      e: key(),
      want: false,
    },
    {
      name: 'Enter outside a composition does not',
      e: key({ key: 'Enter', keyCode: 13 }),
      want: false,
    },
    {
      name: 'keyup is left to xterm, which refocuses on it',
      e: key({ type: 'keyup', key: 'Unidentified', keyCode: 229 }),
      want: false,
    },
  ]

  for (const c of cases) {
    it(c.name, () => {
      expect(isImeKey(c.e)).toBe(c.want)
    })
  }
})

describe('shouldFlushIme', () => {
  const cases: { name: string; e: ImeKey; active: boolean; want: boolean }[] = [
    {
      name: 'Enter finishes the staged word',
      e: key({ key: 'Enter', keyCode: 13 }),
      active: true,
      want: true,
    },
    {
      name: 'Ctrl-C finishes it too',
      e: key({ key: 'c', keyCode: 67 }),
      active: true,
      want: true,
    },
    {
      name: 'nothing to flush without a sequence',
      e: key({ key: 'Enter', keyCode: 13 }),
      active: false,
      want: false,
    },
    {
      name: 'the keyboard composing further is not the user finishing',
      e: key({ key: 'Unidentified', keyCode: 229 }),
      active: true,
      want: false,
    },
    {
      name: 'arming Shift is not finishing',
      e: key({ key: 'Shift', keyCode: 16 }),
      active: true,
      want: false,
    },
    {
      name: 'keyup is not finishing',
      e: key({ type: 'keyup', key: 'Enter', keyCode: 13 }),
      active: true,
      want: false,
    },
  ]

  for (const c of cases) {
    it(c.name, () => {
      expect(shouldFlushIme(c.e, c.active)).toBe(c.want)
    })
  }
})
