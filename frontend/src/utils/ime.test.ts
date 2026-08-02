import { describe, expect, it } from 'vitest'
import { isCompositionArtifact } from './ime'

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
