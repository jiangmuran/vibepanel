import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Terminal as Xterm } from '@xterm/xterm'

import { focusTerminal, keyboardIsSpokenFor, takesTyping } from './focus'
import { liveTerminals } from './terminals'

/**
 * The rule about when the keyboard is allowed to move, without a browser.
 *
 * The half of this feature that can go wrong is the half that says *no*, and it
 * is invisible: a panel that steals focus out of a half-typed note looks like a
 * keyboard problem, not like a tab you clicked four seconds earlier. So the
 * decision is a function over what is holding the keyboard, and the DOM lookup
 * that feeds it is one line at the bottom of focus.ts.
 */
function fakeTerm(disableStdin: boolean, onFocus: () => void): Xterm {
  return { options: { disableStdin }, focus: onFocus } as unknown as Xterm
}

describe('what counts as somebody typing', () => {
  it('is every field a person puts words in', () => {
    expect(takesTyping({ tagName: 'TEXTAREA' })).toBe(true)
    expect(takesTyping({ tagName: 'INPUT' })).toBe(true)
    expect(takesTyping({ tagName: 'INPUT', type: 'text' })).toBe(true)
    expect(takesTyping({ tagName: 'INPUT', type: 'password' })).toBe(true)
    expect(takesTyping({ tagName: 'INPUT', type: 'search' })).toBe(true)
    expect(takesTyping({ tagName: 'SELECT' })).toBe(true)
    expect(takesTyping({ tagName: 'DIV', isContentEditable: true })).toBe(true)
  })

  it('is not a control that happens to be an input', () => {
    // The mobile key bar, the todo checkboxes and the file picker are all
    // inputs. Pressing a key in one of them puts a character nowhere.
    for (const type of ['checkbox', 'radio', 'button', 'submit', 'file', 'range', 'color']) {
      expect(takesTyping({ tagName: 'INPUT', type }), type).toBe(false)
    }
    expect(takesTyping({ tagName: 'BUTTON' })).toBe(false)
    expect(takesTyping({ tagName: 'DIV' })).toBe(false)
    expect(takesTyping(null)).toBe(false)
  })

  it("is not xterm's own hidden textarea", () => {
    // Without this exception the feature cannot work at all: the main terminal
    // holds the keyboard through a textarea, so clicking a tab in the strip
    // underneath would always be told that somebody is typing.
    expect(
      takesTyping({
        tagName: 'TEXTAREA',
        classList: { contains: (n: string) => n === 'xterm-helper-textarea' },
      }),
    ).toBe(false)
  })
})

describe('when the keyboard is spoken for', () => {
  it('leaves a text field alone', () => {
    expect(keyboardIsSpokenFor({ active: { tagName: 'TEXTAREA' } })).toBe(true)
  })

  it('leaves an open modal alone', () => {
    // The picker's list and the confirmation's cancel button are not text
    // fields, so the rule above would happily pull the focus off them.
    expect(keyboardIsSpokenFor({ active: { tagName: 'BUTTON' }, modalOpen: true })).toBe(true)
  })

  it('says nothing is, when nothing is', () => {
    expect(keyboardIsSpokenFor({})).toBe(false)
    expect(keyboardIsSpokenFor({ active: { tagName: 'BODY' } })).toBe(false)
  })
})

describe('aiming the keyboard at a terminal', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    liveTerminals.clear()
  })
  afterEach(() => {
    vi.useRealTimers()
    liveTerminals.clear()
  })

  it('focuses the terminal that was asked for', () => {
    let focused = 0
    liveTerminals.set('a', fakeTerm(false, () => focused++))
    liveTerminals.set('b', fakeTerm(false, () => (focused += 100)))
    focusTerminal('a', () => ({}))
    vi.advanceTimersByTime(1)
    expect(focused).toBe(1)
  })

  it('does not interrupt somebody typing', () => {
    let focused = 0
    liveTerminals.set('a', fakeTerm(false, () => focused++))
    focusTerminal('a', () => ({ active: { tagName: 'TEXTAREA' } }))
    vi.advanceTimersByTime(1000)
    expect(focused).toBe(0)
  })

  it('waits for a terminal that has not mounted yet', () => {
    // The whole reason this is deferred: choosing another session unmounts one
    // xterm and mounts a new one, and the click handler runs before either.
    let focused = 0
    focusTerminal('late', () => ({}))
    vi.advanceTimersByTime(100)
    expect(focused).toBe(0)
    liveTerminals.set('late', fakeTerm(false, () => focused++))
    vi.advanceTimersByTime(100)
    expect(focused).toBe(1)
  })

  it('asks again at the moment it acts, not at the moment it was asked', () => {
    // Somebody clicked a session and then, while its terminal was mounting,
    // clicked into the compose box. The answer at the click was "nobody is
    // typing"; the answer that matters is the one at the end.
    let focused = 0
    let typing = false
    focusTerminal('late', () => (typing ? { active: { tagName: 'TEXTAREA' } } : {}))
    vi.advanceTimersByTime(100)
    typing = true
    liveTerminals.set('late', fakeTerm(false, () => focused++))
    vi.advanceTimersByTime(100)
    expect(focused).toBe(0)
  })

  it('gives up rather than waiting forever', () => {
    let focused = 0
    focusTerminal('never', () => ({}))
    vi.advanceTimersByTime(5000)
    liveTerminals.set('never', fakeTerm(false, () => focused++))
    vi.advanceTimersByTime(5000)
    expect(focused).toBe(0)
  })

  it('never focuses a read-only terminal', () => {
    // That is the phone. Focusing xterm there raises the software keyboard over
    // the output somebody is reading, which is the reason readOnly exists.
    let focused = 0
    liveTerminals.set('phone', fakeTerm(true, () => focused++))
    focusTerminal('phone', () => ({}))
    vi.advanceTimersByTime(1000)
    expect(focused).toBe(0)
  })
})
