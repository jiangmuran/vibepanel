import type { Terminal as Xterm } from '@xterm/xterm'

import { EchoPredictor, PREDICTION_TIMEOUT_MS, type EchoScreen } from './localEcho'

/**
 * Draws EchoPredictor's characters over the terminal. localEcho.ts has the
 * why; this is the how, and the part that touches the DOM.
 *
 * A layer of absolutely placed spans inside xterm's own `.xterm-screen`, so
 * it moves with the terminal through every transform the passive-viewer
 * scaling applies, and needs no knowledge of the renderer underneath: DOM and
 * WebGL draw the same grid.
 */

export interface LocalEcho {
  /** Keystrokes on their way to the server. Call before sending them. */
  input(data: string): void
  /** Drop every prediction, for anything that makes positions stale. */
  reset(): void
  dispose(): void
}

/** Kill switch, for a device where it misbehaves: `localStorage
 *  ['vibepanel.localEcho'] = 'off'`, the same shape as the renderer one.
 *  `'debug'` keeps it on and logs every prediction dropped, and why, to the
 *  console -- which is how "it sometimes still lags" gets a reason. */
export const LOCAL_ECHO_KEY = 'vibepanel.localEcho'

function setting(): string {
  try {
    return localStorage.getItem(LOCAL_ECHO_KEY) ?? ''
  } catch {
    return ''
  }
}

/**
 * Whether the program has hidden the cursor (DECTCEM off).
 *
 * xterm has no public API for it, so this reads the flag the renderer draws
 * from. Optional and checked like imeInput.ts does: if an upgrade renames it,
 * this answers "not hidden" and prediction still has the confirmation check
 * and the timeout behind it.
 */
function cursorHidden(term: Xterm): boolean {
  const core = (term as unknown as { _core?: { coreService?: { isCursorHidden?: unknown } } })._core
  return core?.coreService?.isCursorHidden === true
}

/** How long the output has to be quiet before predictions are checked
 *  against it. A frame arrives in bursts well under this. */
const SETTLE_QUIET_MS = 20

export function attachLocalEcho(term: Xterm, enabled: () => boolean): LocalEcho {
  const predictor = new EchoPredictor()
  const screenEl = term.element?.querySelector<HTMLElement>('.xterm-screen') ?? null
  const layer = document.createElement('div')
  layer.dataset.testid = 'local-echo'
  layer.setAttribute('aria-hidden', 'true')
  layer.style.cssText = 'position:absolute;inset:0;pointer-events:none;z-index:5;overflow:hidden'
  screenEl?.appendChild(layer)

  const screen = (): EchoScreen => {
    const buf = term.buffer.active
    return {
      cols: term.cols,
      cursorX: buf.cursorX,
      cursorY: buf.cursorY,
      cursorHidden: cursorHidden(term),
      cell(x, y) {
        const c = buf.getLine(buf.baseY + y)?.getCell(x)
        return c ? { chars: c.getChars(), dim: c.isDim() !== 0 } : null
      },
    }
  }

  // Predictions only where they can be right: the program is Claude Code and
  // the view is at the bottom -- scrolled up, the cursor's row is not where
  // it would be drawn. Whether the cursor is showing is the predictor's
  // question, and only when it starts: Claude Code hides it while it draws a
  // frame, and a keystroke landing mid-frame must not drop the line typed so
  // far.
  const usable = () => {
    if (!enabled() || setting() === 'off' || !screenEl) return false
    const buf = term.buffer.active
    return buf.viewportY === buf.baseY
  }

  let expiry: ReturnType<typeof setTimeout> | null = null
  const armExpiry = () => {
    if (expiry !== null) clearTimeout(expiry)
    expiry = predictor.active
      ? setTimeout(() => {
          expiry = null
          if (predictor.expire(performance.now())) draw()
          else armExpiry()
        }, PREDICTION_TIMEOUT_MS + 50)
      : null
  }

  let lastReset = ''
  const draw = () => {
    const view = predictor.view()
    layer.dataset.pending = view ? view.chars.map((c) => c.ch).join('') : ''
    if (predictor.lastReset !== lastReset) {
      lastReset = predictor.lastReset
      if (setting() === 'debug') console.log('[vibepanel] local echo dropped:', lastReset)
    }
    if (!view || !screenEl) {
      layer.replaceChildren()
      delete layer.dataset.row
      return
    }
    // Measured every draw rather than cached: a font load, a fit and the
    // passive scaling all change it, and this runs on a keystroke, not a
    // frame. offsetWidth, not getBoundingClientRect: the layer lives inside
    // the transform, so it wants the untransformed size.
    const cw = screenEl.offsetWidth / term.cols
    const ch = screenEl.offsetHeight / term.rows
    const top = view.row * ch
    const theme = term.options.theme ?? {}
    const fg = theme.foreground ?? '#000'
    const bg = theme.background ?? '#fff'
    const font = `${term.options.fontSize ?? 13}px ${term.options.fontFamily ?? 'monospace'}`
    const box = (x: number, w: number): HTMLSpanElement => {
      const s = document.createElement('span')
      s.style.cssText =
        `position:absolute;left:${x * cw}px;top:${top}px;width:${w * cw}px;height:${ch}px;` +
        `line-height:${ch}px;font:${font};color:${fg};background:${bg};` +
        'text-align:center;white-space:pre;overflow:hidden'
      return s
    }
    const out: HTMLElement[] = []
    // The line from the anchor is predicted to be exactly these characters
    // and nothing after them, so the cover runs from the anchor to the end of
    // the row: whatever the server is showing there in the meantime -- the
    // dim hint, a character already taken back -- is a state the typist has
    // already left.
    const from = view.chars[0]?.x ?? view.cursorX
    out.push(box(from, term.cols - from))
    for (const p of view.chars) {
      const s = box(p.x, p.w)
      s.textContent = p.ch
      out.push(s)
    }
    // The real cursor is under the first predicted character; draw one where
    // the next character will go, in xterm's own bar style.
    const cursor = document.createElement('span')
    cursor.style.cssText =
      `position:absolute;left:${view.cursorX * cw}px;top:${top}px;width:${Math.max(1, Math.round(cw / 8))}px;` +
      `height:${ch}px;background:${theme.cursor ?? fg}`
    out.push(cursor)
    layer.replaceChildren(...out)
    // The row as a person sees it, overlay included: what the browser checks
    // read, since the characters drawn here are in no text node xterm owns.
    const line = term.buffer.active.getLine(term.buffer.active.baseY + view.row)
    if (line) {
      const cells: string[] = []
      for (let x = 0; x < term.cols; x++) {
        const c = line.getCell(x)
        // The second half of a wide character is a cell of width 0 and no
        // characters; it is part of the character before it, not a space.
        cells.push(c?.getWidth() === 0 ? '' : c?.getChars() || ' ')
      }
      const from = view.chars[0]?.x ?? view.cursorX
      for (let x = from; x < term.cols; x++) cells[x] = ' '
      for (const p of view.chars) {
        cells[p.x] = p.ch
        if (p.w === 2) cells[p.x + 1] = ''
      }
      layer.dataset.row = cells.join('')
    }
  }

  // Checked once the output has gone quiet, not after every chunk.
  //
  // tmux draws one of Claude Code's frames as several writes, and the cursor
  // is wherever that frame is being drawn until the last of them puts it back
  // in the input box. Checking after every chunk saw the cursor a row up in
  // the middle of nearly every frame and dropped every prediction as "the
  // input moved". Waiting costs nothing a person can see: until the check
  // runs, the character on screen is the predicted one, which is the same
  // character in the same cell.
  let settleTimer: ReturnType<typeof setTimeout> | null = null
  const scheduleSettle = (ms: number) => {
    if (settleTimer !== null) clearTimeout(settleTimer)
    settleTimer = setTimeout(() => {
      settleTimer = null
      const now = performance.now()
      if (predictor.settle(screen(), now)) {
        draw()
        armExpiry()
      }
      // Quiet, but too soon after the last keystroke to believe a match: look
      // again once a round trip has passed, even if nothing else arrives.
      if (predictor.active) {
        const wait = predictor.confirmDelay(now)
        if (wait > 0) scheduleSettle(wait)
      }
    }, ms)
  }
  const parsed = term.onWriteParsed(() => {
    if (!predictor.active) return
    predictor.output(performance.now())
    scheduleSettle(SETTLE_QUIET_MS)
  })
  const resized = term.onResize(() => {
    predictor.reset()
    draw()
  })

  return {
    input(data) {
      if (!usable()) {
        if (predictor.active) {
          predictor.reset()
          draw()
        }
        return
      }
      predictor.input(data, screen(), performance.now())
      draw()
      armExpiry()
    },
    reset() {
      predictor.reset()
      draw()
    },
    dispose() {
      if (expiry !== null) clearTimeout(expiry)
      if (settleTimer !== null) clearTimeout(settleTimer)
      parsed.dispose()
      resized.dispose()
      layer.remove()
    },
  }
}
