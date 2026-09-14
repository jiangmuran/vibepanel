import { describe, expect, it } from 'vitest'

import { mergeErrors, pickLine, readFrameMessage, shouldReload, terminalSafe } from './pick'

const frame = {} as Window
const other = {} as Window

describe('a message from a preview frame', () => {
  it('is accepted from the pane’s own frame and nothing else', () => {
    const msg = { type: 'vp.pick', on: false }
    expect(readFrameMessage(frame, msg, [frame])).toEqual(msg)
    // Another frame, or no source at all. The origin of every sandboxed frame
    // is "null", so the window is the only thing that says which one it was.
    expect(readFrameMessage(other, msg, [frame])).toBeNull()
    expect(readFrameMessage(null, msg, [frame])).toBeNull()
    expect(readFrameMessage(frame, msg, [null, undefined])).toBeNull()
  })

  it('is rebuilt rather than passed through', () => {
    const got = readFrameMessage(
      frame,
      {
        type: 'vp.picked',
        selector: 'div'.repeat(500),
        text: 'x',
        rect: { x: 1.4, y: 'no', w: Infinity, h: 3 },
        viewport: null,
        extra: 'dropped',
      },
      [frame],
    )
    expect(got).toEqual({
      type: 'vp.picked',
      selector: 'div'.repeat(500).slice(0, 200),
      text: 'x',
      rect: { x: 1, y: 0, w: 0, h: 3 },
      viewport: { w: 0, h: 0 },
    })
    expect(readFrameMessage(frame, { type: 'vp.shell', cmd: 'rm' }, [frame])).toBeNull()
    expect(readFrameMessage(frame, 'vp.pick', [frame])).toBeNull()
    const many = readFrameMessage(
      frame,
      { type: 'vp.errors', errors: Array.from({ length: 500 }, () => ({ message: 'm'.repeat(9000) })) },
      [frame],
    )
    if (many?.type !== 'vp.errors') throw new Error('not errors')
    expect(many.errors).toHaveLength(50)
    expect(many.errors[0].message).toHaveLength(500)
  })
})

describe('what a pick pastes into a terminal', () => {
  it('is one line, whatever the page put in it', () => {
    // A line break in a PTY is Enter. In a shell, Enter runs the line.
    const hostile = 'title\r\nrm -rf ~\n\u001b[201~\u0007\u202eevil\u2066'
    const safe = terminalSafe(hostile, 200)
    // eslint-disable-next-line no-control-regex -- the control characters are what is being tested for
    expect(safe).not.toMatch(/[\r\n\u001b\u202e\u2066]/)
    expect(safe).toBe('title rm -rf ~ [201~ evil')
  })

  it('never ends in a newline, and leaves room to type', () => {
    const line = pickLine({
      type: 'vp.picked',
      selector: 'section.tiles > div.tile:nth-of-type(2)',
      text: '今日 token\n\n  41.3M',
      rect: { x: 40, y: 120, w: 460, h: 300 },
      viewport: { w: 1920, h: 1080 },
    })
    expect(line).toBe(
      '[index.html · 1920×1080] section.tiles > div.tile:nth-of-type(2) “今日 token 41.3M” (40,120 460×300) ',
    )
    expect(line.endsWith('\n')).toBe(false)
    expect(line.endsWith(' ')).toBe(true)
  })

  it('cuts by character, not by code unit', () => {
    expect(terminalSafe('非常长的会话标题', 5)).toBe('非常长的…')
    expect([...terminalSafe('👩‍💻'.repeat(40), 10)].length).toBeLessThanOrEqual(10)
  })
})

describe('when the frame reloads', () => {
  it('waits for the directory to settle', () => {
    expect(shouldReload('a', 'a', 'a')).toBe(false)
    // The first poll that sees a change does not reload…
    expect(shouldReload('a', 'a', 'b')).toBe(false)
    // …the second one that agrees does.
    expect(shouldReload('a', 'b', 'b')).toBe(true)
    // A file still being written keeps changing, and keeps not reloading.
    expect(shouldReload('a', 'b', 'c')).toBe(false)
    expect(shouldReload('a', '', '')).toBe(false)
  })
})

describe('errors kept for the agent', () => {
  it('keeps the newest and says each once', () => {
    const e = (message: string) => ({ kind: 'error', message, source: 'app.js', line: 1 })
    expect(mergeErrors([e('old'), e('same')], [e('same'), e('new')]).map((x) => x.message)).toEqual([
      'same',
      'new',
      'old',
    ])
  })
})
