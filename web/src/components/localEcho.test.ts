import { describe, expect, it } from 'vitest'

import { EchoPredictor, PREDICTION_TIMEOUT_MS, echoAgent, isClaudeCode, predictWidth, type EchoCell, type EchoScreen } from './localEcho'

/**
 * A screen that behaves like Claude Code's input line: "❯ " then the input,
 * with the cursor after it. `typed` is what the server has echoed so far.
 */
function claude(typed = '', opts: { cols?: number; row?: number; hint?: string; hidden?: boolean } = {}) {
  const cols = opts.cols ?? 40
  const row = opts.row ?? 10
  const cells: { chars: string; dim: boolean }[] = []
  const put = (s: string, dim = false) => {
    for (const ch of s) {
      const w = predictWidth(ch.codePointAt(0) ?? 0) ?? 1
      cells.push({ chars: ch, dim })
      if (w === 2) cells.push({ chars: '', dim })
    }
  }
  put('❯ ')
  put(typed)
  const cursorX = cells.length
  if (!typed && opts.hint) put(opts.hint, true)
  const screen: EchoScreen = {
    cols,
    cursorX,
    cursorY: row,
    cursorHidden: opts.hidden,
    cell: (x, y) => (y === row ? (cells[x] ?? { chars: '', dim: false }) : { chars: '', dim: false }),
  }
  return screen
}

/** The line as the layer would draw it: the server's cells, overlaid. */
function shown(p: EchoPredictor, s: EchoScreen): string {
  const v = p.view()
  const out: string[] = []
  for (let x = 0; x < s.cols; x++) {
    const c = s.cell(x, s.cursorY)
    out.push(c?.dim ? ' ' : c?.chars || ' ')
  }
  if (v) {
    const from = v.chars[0]?.x ?? v.cursorX
    for (let x = from; x < s.cols; x++) out[x] = ' '
    for (const c of v.chars) {
      out[c.x] = c.ch
      if (c.w === 2) out[c.x + 1] = ''
    }
  }
  return out.join('').slice(2).trimEnd()
}

describe('EchoPredictor', () => {
  it('draws a keystroke at once, before the server has echoed it', () => {
    const p = new EchoPredictor()
    const s = claude()
    p.input('h', s, 0)
    p.input('i', s, 10)
    expect(shown(p, s)).toBe('hi')
    expect(p.view()?.cursorX).toBe(4)
  })

  it('covers the dim "Try …" hint rather than typing over it', () => {
    const p = new EchoPredictor()
    const s = claude('', { hint: 'Try "how do I log an error?"' })
    p.input('f', s, 0)
    expect(shown(p, s)).toBe('f')
  })

  it('draws wide characters two cells wide', () => {
    const p = new EchoPredictor()
    const s = claude()
    p.input('修复！', s, 0)
    const v = p.view()
    expect(v?.chars.map((c) => [c.ch, c.x, c.w])).toEqual([
      ['修', 2, 2],
      ['复', 4, 2],
      ['！', 6, 2],
    ])
    expect(v?.cursorX).toBe(8)
  })

  // The bug the whole-line model exists for. The server echoes "c" and only
  // a round trip later its deletion; a prediction that tracked keystrokes let
  // the "c" come back on screen in between.
  it('never shows a state the typist has left while the server catches up', () => {
    const p = new EchoPredictor()
    let now = 0
    p.input('a', claude(), now)
    p.output((now += 300)) // measures a 300 ms round trip
    for (const k of ['b', 'c', '\x7f', '\x7f', 'x', 'y']) p.input(k, claude(), (now += 150))
    // The server's screen at each point on the way to the final line.
    for (const server of ['a', 'ab', 'abc', 'ab', 'a', 'ax', 'axy']) {
      p.settle(claude(server), (now += 20))
      expect(shown(p, claude(server))).toBe('axy')
    }
  })

  // The second half of the same bug: the server passes through the very
  // state the typist ended in, with keystrokes still on the wire. Believed at
  // once, the prediction was dropped and the "c" reappeared.
  it('does not believe a match until a round trip after the last keystroke', () => {
    const p = new EchoPredictor()
    p.input('a', claude(), 0)
    p.output(300)
    p.input('b', claude(), 300)
    p.input('c', claude(), 450)
    p.input('\x7f', claude(), 460)
    // At 480 the server shows "ab" on its way to "abc": looks finished, is not.
    expect(p.settle(claude('ab'), 480)).toBe(false)
    expect(p.active).toBe(true)
    expect(p.confirmDelay(480)).toBeGreaterThan(0)
    // A round trip after the last key, the server has "ab" for real.
    expect(p.settle(claude('ab'), 460 + 420)).toBe(true)
    expect(p.active).toBe(false)
  })

  it('checks against the frame once it is whole, not the chunk that moved the cursor', () => {
    const p = new EchoPredictor()
    p.input('a', claude(), 0)
    // Mid-frame the cursor is on another row; settle is only called once the
    // output is quiet, and by then it is back. A settle on the moved row is a
    // real move and drops the prediction.
    expect(p.settle({ ...claude('a'), cursorY: 9 }, 400)).toBe(true)
    expect(p.active).toBe(false)
    expect(p.lastReset).toContain('row 10->9')
  })

  it('erases a character the server already has', () => {
    const p = new EchoPredictor()
    const s = claude('ab')
    p.input('\x7f', s, 0)
    expect(shown(p, s)).toBe('a')
    p.input('\x7f', s, 10)
    expect(shown(p, s)).toBe('')
  })

  it('erases a wide character as one', () => {
    const p = new EchoPredictor()
    const s = claude('好')
    p.input('\x7f', s, 0)
    expect(shown(p, s)).toBe('')
    expect(p.view()?.cursorX).toBe(2)
  })

  // A held backspace through "hello world": key repeat is faster than the
  // round trip, so the server is still showing most of the line when the
  // local one is empty. The space in the middle once read as the start of
  // the input and dropped the prediction, and the line grew back to "hello
  // world" before deleting itself a second time.
  it('never lets the line grow back under a held backspace', () => {
    for (const [agent, screen] of [
      ['claude', claude],
      ['codex', codex],
      ['opencode', opencode],
    ] as const) {
      const p = new EchoPredictor(agent)
      p.output(300)
      let now = 1000
      let last = 'hello world'.length
      const serverStates = ['hello world', 'hello world', 'hello worl', 'hello wor', 'hello wo', 'hello w']
      for (let i = 0; i < 16; i++) {
        // The server is five keystrokes behind, and then stops at the start.
        const server = screen(serverStates[Math.min(i, serverStates.length - 1)])
        p.input('\x7f', server, (now += 30))
        p.settle(server, now + 20)
        const raw = shown(p, server)
        // opencode's row starts with its box; the input is what follows "┃".
        const line = (raw.includes('┃') ? raw.slice(raw.indexOf('┃') + 1) : raw).trim()
        expect(line.length, `${agent} after ${i + 1} backspaces: "${line}"`).toBeLessThanOrEqual(last)
        last = line.length
      }
      expect(last, agent).toBe(0)
      expect(p.active, agent).toBe(true)
    }
  })

  // The half-drawn frame that ended a held backspace early: the cursor is
  // already back at the start while the row still shows the last character.
  it('holds an emptied line through a frame whose cursor is ahead of its text', () => {
    const p = new EchoPredictor()
    p.output(300)
    let now = 1000
    for (let i = 0; i < 3; i++) p.input('\x7f', claude('abc'), (now += 60))
    expect(shown(p, claude('abc'))).toBe('')
    const midFrame = { ...claude('h'), cursorX: 2 }
    expect(p.input('\x7f', midFrame, (now += 60))).toBe(true)
    expect(p.active).toBe(true)
    expect(shown(p, midFrame)).toBe('')
    // Once the server really is empty and a round trip has passed, it ends.
    expect(p.settle(claude(''), now + 1000)).toBe(true)
    expect(p.active).toBe(false)
  })

  it('confirms an emptied opencode input showing its grey hint again', () => {
    const p = new EchoPredictor('opencode')
    p.output(100)
    p.input('\x7f', opencode('a'), 1000)
    expect(p.active).toBe(true)
    expect(p.settle(opencode(''), 2000)).toBe(true)
    expect(p.active).toBe(false)
  })

  it('does not erase the prompt', () => {
    const p = new EchoPredictor()
    expect(p.input('\x7f', claude(), 0)).toBe(false)
    expect(p.active).toBe(false)
  })

  it('leaves the mode keys to Claude Code on an empty input', () => {
    for (const k of ['!', '#', '?']) {
      const p = new EchoPredictor()
      expect(p.input(k, claude(), 0)).toBe(false)
    }
    // Anywhere else they are just characters.
    const p = new EchoPredictor()
    expect(p.input('?', claude('why'), 0)).toBe(true)
  })

  it('stops at Enter, and waits for the server before predicting again', () => {
    const p = new EchoPredictor()
    p.input('a', claude(), 0)
    p.output(100)
    expect(p.input('\r', claude('a'), 200)).toBe(false)
    expect(p.active).toBe(false)
    // Typed straight after: the server has not cleared the input yet.
    expect(p.input('b', claude('a'), 220)).toBe(false)
    // After a pause long enough for it to have caught up, prediction resumes.
    expect(p.input('b', claude(), 220 + 1000)).toBe(true)
  })

  it('ignores focus reports, which every click into the terminal sends', () => {
    const p = new EchoPredictor()
    p.input('\x1b[I', claude(), 0)
    expect(p.input('a', claude(), 5)).toBe(true)
  })

  it('drops the line on a mouse report but keeps predicting', () => {
    const p = new EchoPredictor()
    p.input('a', claude(), 0)
    p.input('\x1b[<0;10;5M\x1b[<0;10;5m', claude(), 10)
    expect(p.active).toBe(false)
    expect(p.input('b', claude(), 20)).toBe(true)
  })

  it('does not predict pastes, emoji, or past the edge', () => {
    expect(new EchoPredictor().input('x'.repeat(40), claude(), 0)).toBe(false)
    expect(new EchoPredictor().input('\u{1f600}', claude(), 0)).toBe(false)
    const narrow = new EchoPredictor()
    expect(narrow.input('abcdef', claude('', { cols: 8 }), 0)).toBe(false)
  })

  it('does not predict over text after the cursor', () => {
    const s = claude('abc')
    const moved = { ...s, cursorX: 3 }
    expect(new EchoPredictor().input('x', moved, 0)).toBe(false)
  })

  it('does not start while the program has the cursor hidden', () => {
    expect(new EchoPredictor().input('a', claude('', { hidden: true }), 0)).toBe(false)
  })

  it('gives up if the server never agrees', () => {
    const p = new EchoPredictor()
    p.input('a', claude(), 0)
    expect(p.expire(PREDICTION_TIMEOUT_MS - 1)).toBe(false)
    expect(p.expire(PREDICTION_TIMEOUT_MS + 1)).toBe(true)
    expect(p.active).toBe(false)
  })

  it('keeps showing what is typed for as long as typing goes on', () => {
    const p = new EchoPredictor()
    let now = 0
    for (const k of 'a slow link and a fast typist') p.input(k, claude(), (now += 150))
    // Well past the timeout since the first key, but not since the last.
    expect(p.expire(now + 10)).toBe(false)
    expect(shown(p, claude())).toBe('a slow link and a fast typist')
  })
})

/**
 * Codex's input line: "› " and the input, the hint dim, the whole row.
 */
function codex(typed = '', hint = 'Ask Codex to do anything') {
  const s = claude(typed, { hint })
  const row = s.cursorY
  return {
    ...s,
    cell: (x: number, y: number) => {
      const c = s.cell(x, y)
      return y === row && x === 0 ? { ...c!, chars: '\u203a' } : c
    },
  } satisfies EchoScreen
}

/**
 * opencode's input: a box from column 5 to 30 on its own background, "┃  "
 * then the input, and the hint in a grey rather than dim -- the case that
 * made the marker the test for an empty input instead of the style.
 */
function opencode(typed = '', hint = 'Ask anything…') {
  const row = 7
  const cells: EchoCell[] = []
  for (let x = 0; x < 40; x++) cells.push({ chars: '', dim: false, bg: x >= 5 && x < 30 ? 'box' : 'page' })
  cells[5] = { chars: '\u2503', dim: false, bg: 'box' }
  let x = 8
  for (const ch of typed) {
    const w = predictWidth(ch.codePointAt(0) ?? 0) ?? 1
    cells[x] = { chars: ch, dim: false, bg: 'box' }
    if (w === 2) cells[x + 1] = { chars: '', dim: false, bg: 'box' }
    x += w
  }
  const cursorX = x
  if (!typed) {
    for (const ch of hint) {
      cells[x] = { chars: ch, dim: false, bg: 'box' }
      x++
    }
  }
  return {
    cols: 40,
    cursorX,
    cursorY: row,
    cell: (cx: number, y: number) => (y === row ? (cells[cx] ?? null) : { chars: '', dim: false, bg: 'page' }),
  } satisfies EchoScreen
}

describe('EchoPredictor with Codex', () => {
  it('predicts on "›" and covers the dim hint', () => {
    const p = new EchoPredictor('codex')
    expect(p.input('hi', codex(), 0)).toBe(true)
    expect(p.view()?.chars.map((c) => c.x)).toEqual([2, 3])
  })

  it('leaves "!" (shell) and "?" (shortcuts) to Codex, and types "#"', () => {
    expect(new EchoPredictor('codex').input('!', codex(), 0)).toBe(false)
    expect(new EchoPredictor('codex').input('?', codex(), 0)).toBe(false)
    expect(new EchoPredictor('codex').input('#', codex(), 0)).toBe(true)
  })
})

describe('EchoPredictor with opencode', () => {
  it('treats the grey hint as a hint because the input is empty, not because it is dim', () => {
    const p = new EchoPredictor('opencode')
    expect(p.input('a', opencode(), 0)).toBe(true)
    expect(p.view()?.chars[0]?.x).toBe(8)
  })

  it('keeps the input inside the box: it ends where the box background does', () => {
    const p = new EchoPredictor('opencode')
    p.input('a', opencode(), 0)
    expect(p.view()?.end).toBe(30)
    // 30 minus the padding opencode keeps inside the box.
    const long = new EchoPredictor('opencode')
    expect(long.input('x'.repeat(16), opencode(), 0)).toBe(true)
    expect(long.input('x'.repeat(4), opencode(), 10)).toBe(false)
  })

  it('confirms against the box, not the page beside it', () => {
    const p = new EchoPredictor('opencode')
    p.input('a', opencode(), 0)
    p.output(100)
    expect(p.settle(opencode('a'), 1000)).toBe(true)
    expect(p.active).toBe(false)
  })

  it('leaves "!" (shell) to opencode and types "?"', () => {
    expect(new EchoPredictor('opencode').input('!', opencode(), 0)).toBe(false)
    expect(new EchoPredictor('opencode').input('?', opencode(), 0)).toBe(true)
  })

  it('does not predict over typed text after the cursor inside the box', () => {
    const s = opencode('abc')
    expect(new EchoPredictor('opencode').input('x', { ...s, cursorX: 9 }, 0)).toBe(false)
  })
})

describe('echoAgent', () => {
  const s = (over: Partial<{ launchProfileId: string; launchCommand: string[]; command: string }>) => ({
    launchProfileId: '',
    launchCommand: [],
    command: 'bash',
    ...over,
  })

  it('knows each agent by its built-in profile, its launch command or its process', () => {
    expect(echoAgent(s({ launchProfileId: 'builtin:codex' }))).toBe('codex')
    expect(echoAgent(s({ launchCommand: ['/opt/bin/opencode'] }))).toBe('opencode')
    expect(echoAgent(s({ command: 'codex' }))).toBe('codex')
    expect(echoAgent(s({ command: 'opencode' }))).toBe('opencode')
    expect(echoAgent(s({ command: '2.1.283' }))).toBe('claude')
  })

  it('is null for anything else', () => {
    expect(echoAgent(s({}))).toBe(null)
    expect(echoAgent(s({ command: 'kimi' }))).toBe(null)
    expect(echoAgent(undefined)).toBe(null)
  })
})

describe('isClaudeCode', () => {
  const s = (over: Partial<{ launchProfileId: string; launchCommand: string[]; command: string }>) => ({
    launchProfileId: '',
    launchCommand: [],
    command: 'bash',
    ...over,
  })

  it('knows the built-in profile, the launch command and the process', () => {
    expect(isClaudeCode(s({ launchProfileId: 'builtin:claude' }))).toBe(true)
    expect(isClaudeCode(s({ launchCommand: ['/usr/local/bin/claude', '--resume'] }))).toBe(true)
    expect(isClaudeCode(s({ command: 'claude' }))).toBe(true)
  })

  // Claude Code's native install runs a binary named after its version, so
  // that is what tmux reports -- including for claude started from a shell.
  it('knows the native install, whose process is named after its version', () => {
    expect(isClaudeCode(s({ command: '2.1.283' }))).toBe(true)
  })

  it('leaves everything else alone, shells above all', () => {
    expect(isClaudeCode(s({}))).toBe(false)
    expect(isClaudeCode(s({ command: 'codex' }))).toBe(false)
    expect(isClaudeCode(s({ launchCommand: ['sudo', 'passwd'] }))).toBe(false)
    expect(isClaudeCode(undefined)).toBe(false)
  })
})
