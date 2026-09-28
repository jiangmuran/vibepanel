import type { Session } from '../protocol/wire'

/**
 * Typing into Claude Code at the speed of the device you are holding.
 *
 * Every keystroke used to be a round trip before it was a character: browser,
 * network, panel, tmux, Claude Code redrawing its input box, and back.
 * Measured on this machine with nothing in between that was 40 ms a
 * character; through a 300 ms round trip it was 332 ms, every character,
 * which is what 「打字太卡了」 was about.
 *
 * So the character is drawn here first, at the cursor, in the same frame as
 * the keydown, and the keystroke goes to the server exactly as before. The
 * approach is mosh's and VS Code's terminal typeahead; what makes it simple
 * here is doing it for one program whose behaviour is known rather than
 * guessing at every program.
 *
 * **What is predicted is the input line, not the keystrokes.** The first
 * version predicted characters one at a time and confirmed each against the
 * server's echo, and a backspace broke it: the server echoes a character and
 * only later the deletion of it, so a character already taken back locally
 * came back on screen for a round trip, and the line showed states the
 * typist never passed through ("abxy" on the way from "abc" to "axy"). Now
 * the prediction is what the line should end up as, from the point typing
 * started, drawn over the server's line -- whatever it shows in the meantime
 * -- until the server's line *is* that. Every frame is then a state the typist
 * actually typed.
 *
 * Why only Claude Code. A local echo is a claim about what the program will
 * draw, and a shell reading a password draws nothing: echoing there puts the
 * password on the screen. Claude Code's input box always echoes and keeps the
 * real cursor where the next character goes, so the claim is safe to make and
 * cheap to check.
 *
 * Anything that is not a plain character or a backspace at the end of the
 * input -- Enter, arrows, Escape, Tab, control keys, a paste -- drops the
 * prediction and waits for the server, exactly the behaviour before this
 * existed, and predictions only resume once the typist has paused long
 * enough for the server to have caught up: a prediction anchored on a screen
 * the server has not finished changing is drawn in the wrong place. The
 * failure mode is "as slow as it used to be", never a wrong line on screen for
 * longer than the timeout below.
 */

/** Unconfirmed this long after the last keystroke, a prediction is dropped.
 *  Longer than any round trip worth typing through; shorter than it takes to
 *  wonder why the screen shows something the program never drew. */
export const PREDICTION_TIMEOUT_MS = 2000

/** Pastes are not predicted: they arrive in one piece anyway, and a large one
 *  is the input most likely to be transformed (bracketed paste, collapsed into
 *  "[Pasted text]") rather than echoed. */
const MAX_PREDICTED_INPUT = 16

/** Focus in and out, which xterm sends on its own when focus reporting is
 *  on. */
// eslint-disable-next-line no-control-regex -- matching escape sequences is the point
const FOCUS_REPORT = /^\x1b\[[IO]$/

/** SGR mouse reports, one or several in one chunk. */
// eslint-disable-next-line no-control-regex -- matching escape sequences is the point
const MOUSE_REPORT = /^(\x1b\[<\d+;\d+;\d+[Mm])+$/

/** Round trip assumed before one has been measured. */
const INITIAL_RTT_MS = 300

/**
 * How many cells a character takes, or null for one this does not predict.
 *
 * Deliberately a short list rather than a Unicode width table: ASCII, Latin
 * letters with accents, and the CJK blocks people type through an input
 * method. Anything else -- emoji, combining marks, ambiguous-width symbols --
 * is where terminals and fonts disagree about the width, and a wrong width
 * would push every following character a cell off. Those wait for the
 * server, as they always did.
 */
export function predictWidth(cp: number): 1 | 2 | null {
  if (cp >= 0x20 && cp <= 0x7e) return 1
  if (cp >= 0xa1 && cp <= 0x24f && cp !== 0xad) return 1
  if (
    (cp >= 0x3000 && cp <= 0x303e) || // CJK punctuation: 「」、。
    (cp >= 0x3041 && cp <= 0x33ff) || // kana, CJK compatibility
    (cp >= 0x3400 && cp <= 0x4dbf) ||
    (cp >= 0x4e00 && cp <= 0x9fff) ||
    (cp >= 0xac00 && cp <= 0xd7a3) || // Hangul syllables
    (cp >= 0xf900 && cp <= 0xfaff) ||
    (cp >= 0xfe30 && cp <= 0xfe4f) ||
    (cp >= 0xff01 && cp <= 0xff60) || // full-width forms: ！？，：
    (cp >= 0xffe0 && cp <= 0xffe6)
  ) {
    return 2
  }
  return null
}

/** What the predictor needs to know about the terminal, and nothing else. */
export interface EchoScreen {
  cols: number
  /** Cursor column. */
  cursorX: number
  /**
   * Cursor row on the screen, not in the buffer.
   *
   * Screen rows, because that is tmux's grid: the panel keeps tmux's client
   * off the alternate screen, so a redraw can scroll the browser's buffer a
   * line while every cell stays where it was on screen. Buffer rows moved on
   * nearly every keystroke under Claude Code and dropped every prediction.
   */
  cursorY: number
  /** Whether the program has hidden the cursor, when that is known. */
  cursorHidden?: boolean
  /** The cell's characters ('' for an empty cell) and whether it is dim. */
  cell(x: number, y: number): { chars: string; dim: boolean } | null
}

export interface Predicted {
  ch: string
  x: number
  w: 1 | 2
}

/** What the layer draws. */
export interface EchoView {
  row: number
  chars: Predicted[]
  /** Where the drawn cursor goes, which is also where the cover starts: from
   *  here to the end of the row the line is predicted empty. */
  cursorX: number
}

/** A cell that shows nothing a person would read as typed text: empty, a
 *  space, or dim (Claude Code's "Try …" hint and its inline suggestions). */
function blankish(c: { chars: string; dim: boolean } | null): boolean {
  return !c || c.chars.trim() === '' || c.chars === ' ' || c.dim
}

export class EchoPredictor {
  /** The typed line from `anchor`, as it should end up. */
  private text: { ch: string; w: 1 | 2 }[] = []
  private anchor = 0
  private row = -1
  private lastKey = -Infinity
  /** Predictions are off until the typist pauses; see `suspend`. */
  private suspended = false
  private rtt = INITIAL_RTT_MS
  /** When the keystroke that started the current prediction was sent, until
   *  the first output after it arrives: a clean round trip, because the
   *  server was idle when it left. */
  private probe: number | null = null
  private measured = 0

  /** Why the last prediction was dropped, for the debug switch. */
  lastReset = ''
  private resets = 0

  get active(): boolean {
    return this.row >= 0
  }

  /** The round trip as measured from confirmations, for the debug switch. */
  get roundTrip(): number {
    return this.rtt
  }

  reset(why = 'reset'): void {
    if (this.active) this.lastReset = `#${++this.resets} ${why}`
    this.text = []
    this.row = -1
  }

  /**
   * Drop the prediction and stop predicting until the typist pauses.
   *
   * After a key this does not predict, the server's screen is about to change
   * in a way this cannot draw -- a submitted prompt, a moved cursor, a mode
   * switch -- and a new prediction anchored on the screen as it is now would
   * start from the wrong place. A pause of a round trip and a half is long
   * enough for the server to have caught up with everything typed.
   */
  private suspend(why: string): void {
    this.reset(why)
    this.suspended = true
  }

  private width(): number {
    return this.text.reduce((n, c) => n + c.w, 0)
  }

  view(): EchoView | null {
    if (!this.active) return null
    const chars: Predicted[] = []
    let x = this.anchor
    for (const c of this.text) {
      chars.push({ ch: c.ch, x, w: c.w })
      x += c.w
    }
    return { row: this.row, chars, cursorX: x }
  }

  /**
   * Keystrokes on their way to the server. Returns whether anything is now
   * predicted, which is the caller's cue to redraw.
   */
  input(data: string, screen: EchoScreen, now: number): boolean {
    if (data.length === 0) return this.active
    // Not keystrokes: what the terminal says about itself. A focus report is
    // sent on every click into the terminal, which is how nearly everybody
    // starts typing, and treating it as an unpredictable key switched
    // prediction off for exactly the first characters typed.
    if (FOCUS_REPORT.test(data)) return this.active
    // A mouse report can move Claude Code's cursor, so the line typed so far
    // is no longer anchored anywhere -- but it is not a keystroke either, and
    // the next one should still be predicted.
    if (MOUSE_REPORT.test(data)) {
      this.reset('mouse')
      return false
    }
    const since = now - this.lastKey
    this.lastKey = now
    if (this.suspended) {
      if (since < this.rtt * 1.5 + 100) return false
      this.suspended = false
    }
    if (data.length > MAX_PREDICTED_INPUT) {
      this.suspend('paste')
      return false
    }
    for (const ch of data) {
      if (!this.active && !this.start(ch, screen)) return false
      if (ch === '\x7f') {
        if (!this.backspace(screen)) return false
        continue
      }
      const w = predictWidth(ch.codePointAt(0) ?? 0)
      if (w === null) {
        this.suspend('key ' + JSON.stringify(ch))
        return false
      }
      // No prediction across the edge: where Claude Code wraps a long input
      // is its own layout decision, and guessing it wrong puts a character on
      // a row it will never be on.
      if (this.anchor + this.width() + w > screen.cols - 1) {
        this.suspend('edge')
        return false
      }
      this.text.push({ ch, w })
    }
    return this.active
  }

  /** Anchor a new prediction at the server's cursor, if typing there is
   *  something this can predict. */
  private start(ch: string, screen: EchoScreen): boolean {
    const x = screen.cursorX
    const y = screen.cursorY
    if (screen.cursorHidden) {
      this.suspend('cursor hidden')
      return false
    }
    // Only typing at the end of the input. With text after the cursor --
    // the arrows moved it back -- the rest of the line shifts on every key,
    // and that is Claude Code's layout, not this file's.
    for (let i = x; i < screen.cols; i++) {
      if (!blankish(screen.cell(i, y))) {
        this.suspend('text after cursor')
        return false
      }
    }
    // Claude Code reads these three at the start of an empty input as mode
    // switches (bash, memory, help) and draws no character for them.
    const prompt = screen.cell(x - 2, y)?.chars === '❯' && blankish(screen.cell(x - 1, y))
    if (prompt && (ch === '!' || ch === '#' || ch === '?')) {
      this.suspend('mode key ' + ch)
      return false
    }
    this.anchor = x
    this.row = y
    this.text = []
    this.probe = this.lastKey
    return true
  }

  /** Take back the last predicted character, or erase the one before the
   *  anchor. Returns false when that cannot be predicted. */
  private backspace(screen: EchoScreen): boolean {
    if (this.text.length > 0) {
      this.text.pop()
      return true
    }
    // Erasing text the server already has: move the anchor back over it, and
    // the line from there is predicted empty. The cell before the anchor may
    // be the second half of a wide character, which xterm stores as an empty
    // cell after the character itself.
    let x = this.anchor - 1
    let prev = screen.cell(x, this.row)
    if (prev && prev.chars === '' && x > 0) {
      const wide = screen.cell(x - 1, this.row)
      if (wide && predictWidth(wide.chars.codePointAt(0) ?? 0) === 2) {
        x -= 1
        prev = wide
      }
    }
    // At the start of the input there is only the prompt before the cursor
    // and Claude Code ignores the key; predicting an erase there would blank
    // the prompt.
    if (x < 0 || blankish(prev) || prev?.chars === '❯') {
      this.suspend('backspace at start')
      return false
    }
    this.anchor = x
    return true
  }

  /**
   * Output arrived. The first after a prediction started is the echo of the
   * keystroke that started it, sent to a server that was idle, so the time
   * between them is the round trip.
   */
  output(now: number): void {
    if (this.probe === null) return
    const sample = Math.min(Math.max(now - this.probe, 1), PREDICTION_TIMEOUT_MS)
    this.probe = null
    // Up at once, down slowly. Too short and a match is believed while
    // keystrokes are still on the wire, which shows the line going backwards;
    // too long only keeps a correct prediction on screen a little longer.
    // The first sample replaces the guess outright.
    if (this.measured === 0 || sample > this.rtt) this.rtt = sample
    else this.rtt = this.rtt * 0.8 + sample * 0.2
    this.measured++
  }

  /**
   * How long after the last keystroke a match can be believed.
   *
   * A match on its own is not enough, and the first version of this that
   * trusted one showed "abc" come back after the typist had deleted the "c":
   * the server stopped at "ab" on its way to "abc", which is also where the
   * typist ended up after the backspace, and the prediction was dropped while
   * the "c" and its deletion were still on the wire. Once a round trip has
   * passed since the last keystroke, everything typed has been echoed.
   */
  confirmDelay(now: number): number {
    return Math.max(0, this.lastKey + this.rtt * 1.25 + 30 - now)
  }

  /**
   * The server's output has gone quiet: drop the prediction if the server's
   * line now is the predicted line, or if it has moved somewhere this cannot
   * follow. Returns whether anything changed.
   */
  settle(screen: EchoScreen, now: number): boolean {
    if (!this.active) return false
    // The input moved -- output above it, the box grew, the screen cleared.
    // Positions computed against the old row mean nothing now.
    if (screen.cursorY !== this.row) {
      this.suspend(`row ${this.row}->${screen.cursorY}`)
      return true
    }
    if (this.confirmDelay(now) === 0 && this.matches(screen)) {
      // The server has caught up with everything typed.
      this.text = []
      this.row = -1
      return true
    }
    return this.expire(now)
  }

  /** Whether the server's line from the anchor is exactly the prediction. */
  private matches(screen: EchoScreen): boolean {
    let x = this.anchor
    for (const c of this.text) {
      const cell = screen.cell(x, this.row)
      const chars = cell?.chars ?? ''
      if (!(chars === c.ch || (c.ch === ' ' && (chars === '' || chars === ' ')))) return false
      x += c.w
    }
    if (screen.cursorX !== x) return false
    for (let i = x; i < screen.cols; i++) if (!blankish(screen.cell(i, this.row))) return false
    return true
  }

  /** The timeout, counted from the last keystroke: while somebody is still
   *  typing, what they typed is the right thing to show. */
  expire(now: number): boolean {
    if (this.active && now - this.lastKey > PREDICTION_TIMEOUT_MS) {
      this.suspend('timeout')
      return true
    }
    return false
  }
}

/**
 * Whether a session is Claude Code.
 *
 * Three ways in, because a session remembers how it was started and not what
 * is running now: the built-in profile, a launch command that is `claude`, or
 * the process in the pane. The last one has a wrinkle worth knowing: Claude
 * Code's native install runs a binary named after its version
 * (`~/.local/share/claude/versions/2.1.283`), so tmux reports the pane's
 * command as `2.1.283` and never as `claude`. That is also the only way to
 * recognise Claude Code started by hand from a shell session.
 */
export function isClaudeCode(s: Pick<Session, 'launchProfileId' | 'launchCommand' | 'command'> | undefined): boolean {
  if (!s) return false
  if (s.launchProfileId === 'builtin:claude') return true
  const argv0 = s.launchCommand[0] ?? ''
  if (argv0.split('/').pop() === 'claude') return true
  return s.command === 'claude' || /^\d+\.\d+\.\d+$/.test(s.command)
}
