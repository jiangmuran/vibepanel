import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

const source = readFileSync(new URL('./components/Terminal.tsx', import.meta.url), 'utf8')

describe('terminal contrast guard', () => {
  it('keeps the dim-text ratio high enough for ANSI backgrounds', () => {
    expect(source).toMatch(/const MIN_TERMINAL_CONTRAST_RATIO = 9\b/)
    expect(source).toMatch(/minimumContrastRatio:\s*MIN_TERMINAL_CONTRAST_RATIO\b/)
  })
})
