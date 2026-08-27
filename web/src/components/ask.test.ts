import { describe, expect, it } from 'vitest'

import { answerAsk, askConfirm, askText, currentAsk } from './ask'

const question = { title: 'Kill it?', confirm: 'Kill', cancel: 'Cancel' }

describe('asking before something cannot be taken back', () => {
  it('resolves true only when the answer is not a cancellation', async () => {
    const yes = askConfirm(question)
    answerAsk('')
    expect(await yes).toBe(true)

    const no = askConfirm(question)
    answerAsk(null)
    expect(await no).toBe(false)
  })

  it('gives a field question its text back, and null when it is dismissed', async () => {
    const named = askText({ ...question, field: { label: 'Name', value: 'This device' } })
    answerAsk('Work laptop')
    expect(await named).toBe('Work laptop')

    const dropped = askText({ ...question, field: { label: 'Name', value: 'This device' } })
    answerAsk(null)
    expect(await dropped).toBe(null)
  })

  it('does not read an emptied field as a cancellation', async () => {
    // The reason the answer is `string | null` rather than a string: somebody
    // clearing the name has answered, and a caller checking the string for
    // truth would treat it as a dismissal and quietly do nothing.
    const named = askText({ ...question, field: { label: 'Name', value: 'This device' } })
    answerAsk('')
    expect(await named).toBe('')
  })

  it('carries the question to whatever is drawing it', () => {
    const pending = askConfirm({ ...question, destructive: true })
    expect(currentAsk()?.request.title).toBe('Kill it?')
    expect(currentAsk()?.request.destructive).toBe(true)
    // Stable between reads, because useSyncExternalStore compares by identity.
    expect(currentAsk()).toBe(currentAsk())
    answerAsk(null)
    expect(currentAsk()).toBe(null)
    return pending
  })

  it('queues a second question rather than dropping it', async () => {
    // A dropped one is a promise that never settles, which is a click that
    // silently did nothing.
    const first = askConfirm({ ...question, title: 'first' })
    const second = askConfirm({ ...question, title: 'second' })
    expect(currentAsk()?.request.title).toBe('first')
    answerAsk(null)
    expect(currentAsk()?.request.title).toBe('second')
    answerAsk('')
    expect(await first).toBe(false)
    expect(await second).toBe(true)
  })

  it('ignores an answer to a question nobody asked', () => {
    expect(currentAsk()).toBe(null)
    expect(() => answerAsk('')).not.toThrow()
  })

  it('refuses a field on a plain confirmation', async () => {
    // askConfirm strips it. A yes/no question that grew a text box because a
    // caller passed one through is a dialog nobody designed.
    const yes = askConfirm({ ...question, field: { label: 'Name', value: 'x' } } as never)
    expect(currentAsk()?.request.field).toBe(undefined)
    answerAsk('')
    expect(await yes).toBe(true)
  })
})
