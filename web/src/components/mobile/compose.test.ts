import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

import { isNewSubmission } from './ComposeInput'

/**
 * One commit, one message.
 *
 * 「语音输入法好像有的时候会粘贴两遍，尤其是那种可能带转写、自动总结或者转发
 * 的」. `send` reads the text out of React state and clears it with
 * `setText('')`, which does not take effect until the next render — so two
 * commits arriving before that both read the same string and both send it. A
 * keyboard that transcribes and then commits a summary produces exactly that:
 * two submissions of the same text with nothing typed in between.
 *
 * By content rather than by a timer, because the two arrive some hundreds of
 * milliseconds apart and any window short enough to be safe is too short to
 * catch them.
 */
describe('a repeated commit of the same text', () => {
  it('sends the first one', () => {
    expect(isNewSubmission('deploy the thing', '')).toBe(true)
  })

  it('drops the second, which is the reported bug', () => {
    expect(isNewSubmission('deploy the thing', 'deploy the thing')).toBe(false)
  })

  it('sends a different one straight after', () => {
    // A transcription followed by a *summary* is two different strings and
    // both are wanted; only an identical repeat is the fault.
    expect(isNewSubmission('summary: deploy', 'deploy the thing')).toBe(true)
  })

  it('sends the same words again once the box has been edited', () => {
    // The component clears `last` on every change, so retyping the same
    // command on purpose still works. Modelled here as an empty `last`.
    expect(isNewSubmission('y', '')).toBe(true)
  })

  it('never sends an empty box', () => {
    expect(isNewSubmission('', '')).toBe(false)
    expect(isNewSubmission('', 'anything')).toBe(false)
  })
})

describe('and the component actually uses it', () => {
  /*
   * The predicate and the wiring are two things, and the tests above only reach
   * one of them: `send` can be changed back to `if (!text) return` with every
   * assertion still green -- the rule exists, is correct, and is not consulted.
   *
   * This is the fourth time in this suite. The pattern is always the same shape:
   * a decision extracted into a pure function so it can be tested, and then a
   * call site that nothing tests at all. The extraction is what makes it
   * testable and also what makes it skippable.
   */
  const code = readFileSync(new URL('./ComposeInput.tsx', import.meta.url), 'utf8')
    .replace(/\/\*[\s\S]*?\*\//g, ' ')
    .replace(/^\s*\/\/.*$/gm, ' ')

  it('guards send with the predicate', () => {
    expect(code).toMatch(/if \(!isNewSubmission\(text, lastSent\.current\)\) return/)
    expect(code).toMatch(/lastSent\.current = text/)
  })

  it('clears the record whenever the box changes', () => {
    // Without this, sending the same command twice on purpose is impossible:
    // the second one looks exactly like the double the guard exists to drop.
    expect(code).toMatch(/lastSent\.current = ''/)
  })

  it('treats an IME keycode as composition, like the rest of the panel', () => {
    // 229 is "the IME is handling this", and the keyboards that report it
    // without setting isComposing are the dictation ones -- the same family as
    // the double-send. ConfirmDialog and DirectoryPicker both check it.
    expect(code).toMatch(/keyCode === 229/)
  })
})
