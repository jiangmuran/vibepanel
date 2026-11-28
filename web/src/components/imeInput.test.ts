import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  attachImeCommitFix,
  releaseImeCommit,
  resetStuckComposition,
  type TextInputLike,
} from './imeInput'

/** The private shape xterm 6.0.0 has, which the fix reaches into. */
function core(overrides: { composing?: boolean; sending?: boolean; seen?: boolean } = {}) {
  const classes = new Set<string>(overrides.composing ? ['active'] : [])
  return {
    _keyDownSeen: overrides.seen ?? true,
    textarea: { value: overrides.composing ? 'zhong' : '' },
    _compositionHelper: {
      _isComposing: overrides.composing ?? false,
      _isSendingComposition: overrides.sending ?? false,
      _dataAlreadySent: overrides.composing ? 'z' : '',
      _compositionView: {
        textContent: overrides.composing ? 'zhong' : '',
        classList: {
          remove: (name: string) => classes.delete(name),
          has: (name: string) => classes.has(name),
        },
      },
    },
  }
}

const event = (overrides: Partial<TextInputLike> = {}): TextInputLike => ({
  inputType: 'insertText',
  data: '！',
  isComposing: false,
  ...overrides,
})

/** Records what attach added, so the capture flag can be asserted. */
class FakeHost {
  added: Array<{ type: string; listener: (ev: unknown) => void; capture: boolean }> = []
  removed = 0

  addEventListener(type: string, listener: (ev: unknown) => void, capture: boolean) {
    this.added.push({ type, listener, capture })
  }

  removeEventListener() {
    this.removed++
  }

  fire(ev: unknown) {
    for (const a of this.added) a.listener(ev)
  }

  terminal(): unknown {
    return { _core: core() }
  }
}

describe('releaseImeCommit', () => {
  // The regression: full-width punctuation typed with the macOS Chinese IME
  // arrives beforeinput -> input -> keydown(229), with no composition events.
  // Shift's keydown set _keyDownSeen and its keyup has not happened, so
  // xterm's input handler discards the character unless this clears the flag
  // first. Without the fix the flag stays true and the input event is lost —
  // the character only lands on the second press.
  it('releases the flag for a macOS IME punctuation commit', () => {
    const term = { _core: core() }
    expect(releaseImeCommit(term, event({ data: '！' }))).toBe(true)
    expect(term._core._keyDownSeen).toBe(false)
  })

  it('covers ？ and ＋, and any other insertText', () => {
    for (const data of ['？', '＋', '。', '你']) {
      const term = { _core: core() }
      expect(releaseImeCommit(term, event({ data })), data).toBe(true)
      expect(term._core._keyDownSeen, data).toBe(false)
    }
  })

  // The line that keeps the fix from becoming the duplicate-send bug: xterm's
  // CompositionHelper sends a committed composition from its own deferred
  // callback, so an input event released here would send it twice.
  it('leaves a composition finalize alone', () => {
    const term = { _core: core({ sending: true }) }
    expect(releaseImeCommit(term, event())).toBe(false)
    expect(term._core._keyDownSeen).toBe(true)
  })

  it('leaves an active composition alone', () => {
    const term = { _core: core({ composing: true }) }
    expect(releaseImeCommit(term, event())).toBe(false)
    expect(term._core._keyDownSeen).toBe(true)
  })

  it('ignores composition input events even when nothing is composing', () => {
    const term = { _core: core() }
    expect(releaseImeCommit(term, event({ inputType: 'insertCompositionText' }))).toBe(false)
    expect(term._core._keyDownSeen).toBe(true)
  })

  it('ignores an event that is itself composing', () => {
    const term = { _core: core() }
    expect(releaseImeCommit(term, event({ isComposing: true }))).toBe(false)
    expect(term._core._keyDownSeen).toBe(true)
  })

  it('ignores non-insertions', () => {
    const term = { _core: core() }
    for (const inputType of ['insertFromPaste', 'deleteContentBackward', 'insertLineBreak']) {
      expect(releaseImeCommit(term, event({ inputType })), inputType).toBe(false)
    }
    expect(releaseImeCommit(term, event({ data: null }))).toBe(false)
    expect(releaseImeCommit(term, event({ data: '' }))).toBe(false)
    expect(term._core._keyDownSeen).toBe(true)
  })

  it('does nothing when the flag is already clear', () => {
    const term = { _core: core({ seen: false }) }
    expect(releaseImeCommit(term, event())).toBe(false)
  })

  // A future xterm that renames any of this must go quiet, not throw inside a
  // keyboard event and not guess at composition state.
  it('goes quiet when xterm internals are not shaped as expected', () => {
    expect(releaseImeCommit(undefined, event())).toBe(false)
    expect(releaseImeCommit({}, event())).toBe(false)
    expect(releaseImeCommit({ _core: {} }, event())).toBe(false)
    expect(releaseImeCommit({ _core: { _keyDownSeen: true } }, event())).toBe(false)
    expect(
      releaseImeCommit({ _core: { _keyDownSeen: true, _compositionHelper: {} } }, event()),
    ).toBe(false)
  })
})

describe('resetStuckComposition', () => {
  it('clears orphaned composition flags left behind by blur or display:none', () => {
    const term = { _core: core({ composing: true, sending: true, seen: true }) }
    expect(resetStuckComposition(term)).toBe(true)
    expect(term._core._keyDownSeen).toBe(false)
    expect(term._core._compositionHelper._isComposing).toBe(false)
    expect(term._core._compositionHelper._isSendingComposition).toBe(false)
    expect(term._core._compositionHelper._dataAlreadySent).toBe('')
    expect(term._core._compositionHelper._compositionView.textContent).toBe('')
    expect(term._core._compositionHelper._compositionView.classList.has('active')).toBe(false)
    expect(term._core.textarea.value).toBe('')
  })

  it('returns false when nothing is stuck', () => {
    const term = { _core: core({ composing: false, sending: false, seen: false }) }
    expect(resetStuckComposition(term)).toBe(false)
  })
})

describe('attachImeCommitFix', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('listens for beforeinput and blur in capture phase and clears orphaned composition', () => {
    vi.useFakeTimers()
    const host = new FakeHost()
    const term = { _core: core({ composing: true, seen: true }) }
    const detach = attachImeCommitFix(host as unknown as EventTarget, term)

    expect(host.added.map((a) => [a.type, a.capture])).toEqual([
      ['beforeinput', true],
      ['blur', true],
    ])

    // Trigger blur listener: waits one macrotask so a legitimate compositionend can finalize first.
    host.added.find((a) => a.type === 'blur')?.listener({})
    expect(term._core._compositionHelper._isComposing).toBe(true)
    vi.advanceTimersByTime(1)
    expect(term._core._compositionHelper._isComposing).toBe(false)
    expect(term._core._keyDownSeen).toBe(false)

    detach()
    expect(host.removed).toBe(2)
  })
})

// The workaround reaches into private fields, so it is pinned against the
// installed bundle rather than trusted. If an xterm upgrade renames one, this
// fails in the fast gate and says what to re-derive; without it the fix would
// silently stop working and the first full-width `！` would need two presses
// again, on somebody's machine, months later.
describe('the xterm internals the workaround depends on', () => {
  const bundle = fileURLToPath(
    new URL('../../node_modules/@xterm/xterm/lib/xterm.js', import.meta.url),
  )

  it('are still present in the installed xterm', () => {
    const source = readFileSync(bundle, 'utf8')
    for (const name of [
      '_keyDownSeen',
      '_compositionHelper',
      '_isComposing',
      '_isSendingComposition',
      '_compositionView',
      '_dataAlreadySent',
    ]) {
      expect(
        source.includes(name),
        `${name} is not in ${bundle}. xterm's input internals changed; ` +
          're-derive the macOS IME workaround in imeInput.ts before removing this.',
      ).toBe(true)
    }
  })
})
