import { describe, expect, it } from 'vitest'

import { codeDigits } from './code'

describe('codeDigits', () => {
  it('reads a code the way it was typed', () => {
    expect(codeDigits('123456')).toBe('123456')
    expect(codeDigits('１２３ ４５６')).toBe('123456')
    expect(codeDigits(' 123-456 ')).toBe('123456')
    expect(codeDigits('abc')).toBe('')
  })
})
