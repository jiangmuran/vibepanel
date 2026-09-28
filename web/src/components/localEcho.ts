import type { Session } from '../protocol/wire'

/**
 * Typing into a coding agent at the speed of the device you are holding.
 *
 * Every keystroke used to be a round trip before it was a character: browser,
 * network, panel, tmux, the agent redrawing its input box, and back. Measured
 * on this machine against Claude Code with nothing in between that was 40 ms a
 * character; through a 300 ms round trip it was 332 ms, every character,
 * which is what 「打字太卡了」 was about.
 *
 * So the character is drawn here first, at the cursor, in the same frame as
 * the keydown, and the keystroke goes to the server exactly as before. The
 * approach is mosh's and VS Code's terminal typeahead; what makes it simple
 * here is doing it for a few programs whose behaviour is known -- Claude Code,
 * Codex and opencode, each described in AGENTS below -- rather than guessing
 * at every program.
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
 * Why only these agents. A local echo is a claim about what the program will
 * draw, and a shell reading a password draws nothing: echoing there puts the
 * password on the screen. These three always echo into their input box and
 * keep the real cursor where the next character goes, so the claim is safe to
 * make and cheap to check.
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
 * What differs between the agents, all of it read off each one in tmux with
 * `display -p '#{cursor_x},#{cursor_y}'` while typing.
 *
 * - `marker` is the character the input line starts with. Everything between
 *   it and the cursor being blank is how an empty input is recognised, and on
 *   an empty input whatever sits after the cursor is the agent's hint ("Try
 *   …", "Ask Codex to do anything", "Ask anything…") rather than typed text.
 *   Claude Code and Codex draw that hint dim; opencode draws it in a grey of
 *   its theme, which is why the test is the marker and not the style.
 * - `modeKeys` are read as mode switches on an empty input and draw no
 *   character: Claude Code's `!` bash, `#` memory and `?` help; Codex's `!`
 *   shell and `?` shortcuts; opencode's `!` shell. Every other first
 *   character is echoed, including `/` and `@`, whose menus open above or
 *   below the input row without moving it.
 * - `pad` is the cells between the marker and the first typed character, so
 *   the marker's column says where the input starts.
 * - `margin` is how far before the end of the input region text wraps.
 *   opencode's input is a box in the middle of the screen with padding inside
 *   it; the other two use the whole row.
 */
export const AGENTS = {
  claude: { marker: '❯', pad: 1, modeKeys: '!#?', margin: 1 },
  codex: { marker: '›', pad: 1, modeKeys: '!?', margin: 1 },
  opencode: { marker: '┃', pad: 2, modeKeys: '!', margin: 3 },
} as const

export type EchoAgent = keyof typeof AGENTS

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

/** One cell, as much as the predictor needs of it. */
export interface EchoCell {
  /** The cell's characters, '' for an empty cell. */
  chars: string
  dim: boolean
  /** The background, as an opaque key: equal keys are the same colour. */
  bg?: string
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
  cell(x: number, y: number): EchoCell | null
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
   *  here to `end` the line is predicted empty. */
  cursorX: number
  /** One past the last cell of the input region: the end of the row, or of
   *  opencode's box. The cover stops here. */
  end: number
}

/** A cell that shows nothing a person would read as typed text: empty, a
 *  space, or dim (the hints and Claude Code's inline suggestions). */
function blankish(c: EchoCell | null): boolean {
  return !c || c.chars.trim() === '' || c.chars === ' ' || c.dim
}

export class EchoPredictor {
  /** The typed line from `anchor`, as it should end up. */
  private text: { ch: string; w: 1 | 2 }[] = []
  private anchor = 0
  private row = -1
  /** One past the input region, found when the prediction starts. */
  private end = 0
  /** The input's first column, or -1 when the marker was not found. */
  private inputStart = -1
  /** Whether the prediction was started by the keystroke being handled. */
  private fresh = false
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

  constructor(private agent: EchoAgent = 'claude') {}

  /** The program in the pane changed; what was predicted for the old one
   *  means nothing for the new one. */
  setAgent(agent: EchoAgent): void {
    if (agent === this.agent) return
    this.agent = agent
    this.reset('agent changed')
  }

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
    return { row: this.row, chars, cursorX: x, end: this.end }
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
    // A mouse report can move the agent's cursor, so the line typed so far is
    // no longer anchored anywhere -- but it is not a keystroke either, and the
    // next one should still be predicted.
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
        const ok = this.backspace(screen)
        this.fresh = false
        if (!ok) return false
        continue
      }
      this.fresh = false
      const w = predictWidth(ch.codePointAt(0) ?? 0)
      if (w === null) {
        this.suspend('key ' + JSON.stringify(ch))
        return false
      }
      // No prediction across the edge: where the agent wraps a long input is
      // its own layout decision, and guessing it wrong puts a character on a
      // row it will never be on.
      if (this.anchor + this.width() + w > this.end - AGENTS[this.agent].margin) {
        this.suspend('edge')
        return false
      }
      this.text.push({ ch, w })
    }
    return this.active
  }

  /** Whether the input is empty: nothing but blanks between the agent's
   *  marker and the cursor. */
  private emptyInput(screen: EchoScreen, x: number, y: number): boolean {
    const marker = AGENTS[this.agent].marker
    for (let i = x - 1; i >= 0 && i >= x - 4; i--) {
      const c = screen.cell(i, y)
      if (c?.chars === marker) return true
      if (!blankish(c) || c?.dim) return false
    }
    return false
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
    // The input region runs to the end of the row, or, for an input drawn as
    // a box with its own background, to where that background stops.
    const bg = screen.cell(x, y)?.bg
    let end = x
    while (end < screen.cols && screen.cell(end, y)?.bg === bg) end++
    const empty = this.emptyInput(screen, x, y)
    // Only typing at the end of the input. With text after the cursor --
    // the arrows moved it back -- the rest of the line shifts on every key,
    // and that is the agent's layout, not this file's. On an empty input what
    // follows the cursor is the hint, which the first character replaces.
    if (!empty) {
      for (let i = x; i < end; i++) {
        if (!blankish(screen.cell(i, y))) {
          this.suspend('text after cursor')
          return false
        }
      }
    }
    if (empty && AGENTS[this.agent].modeKeys.includes(ch)) {
      this.suspend('mode key ' + ch)
      return false
    }
    this.anchor = x
    this.row = y
    this.end = end
    this.inputStart = this.findStart(screen, x, y)
    this.fresh = true
    this.text = []
    this.probe = this.lastKey
    return true
  }

  /** The column the input starts at: the nearest marker to the left of the
   *  cursor, and the agent's padding after it. -1 if there is none. */
  private findStart(screen: EchoScreen, x: number, y: number): number {
    const { marker, pad } = AGENTS[this.agent]
    for (let i = x - 1; i >= 0; i--) {
      if (screen.cell(i, y)?.chars === marker) return i + 1 + pad
    }
    return -1
  }

  /** Take back the last predicted character, or erase the one before the
   *  anchor. Returns false when that cannot be predicted. */
  private backspace(screen: EchoScreen): boolean {
    if (this.text.length > 0) {
      this.text.pop()
      return true
    }
    // Nothing left to erase. The agent ignores the key here, and so does
    // this: a held backspace goes on past the start of the input, and
    // dropping the prediction at that point put back on screen everything
    // the server had not deleted yet, which then deleted itself again.
    if (this.inputStart >= 0 && this.anchor <= this.inputStart) {
      // A backspace on an input that was already empty, with nothing
      // predicted before it: nothing to hold on to.
      //
      // Only then. Mid-way through a held backspace the cursor can read as
      // at the start while the server is still drawing the line -- a frame
      // moves the cursor first and rewrites the row after -- and ending the
      // prediction on that showed the last undeleted character come back.
      if (this.fresh) {
        this.lastReset = `#${++this.resets} backspace on an empty input`
        this.text = []
        this.row = -1
        return false
      }
      return true
    }
    // Erasing text the server already has: move the anchor back over it, and
    // the line from there is predicted empty. The cell before the anchor may
    // be the second half of a wide character, which xterm stores as an empty
    // cell after the character itself.
    let x = this.anchor - 1
    const prev = screen.cell(x, this.row)
    if (prev && prev.chars === '' && x > 0) {
      const wide = screen.cell(x - 1, this.row)
      if (wide && predictWidth(wide.chars.codePointAt(0) ?? 0) === 2) x -= 1
    }
    // Where the input starts is known from the marker, not from what the cell
    // before the anchor looks like: a space inside the input looks exactly
    // like the padding before it, and treating it as the start dropped the
    // prediction in the middle of a held backspace.
    if (this.inputStart < 0 || x < this.inputStart) {
      this.suspend('backspace without a known start')
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
      this.lastReset = `#${++this.resets} confirmed (round trip ${Math.round(this.rtt)} ms)`
      this.text = []
      this.row = -1
      return true
    }
    return this.expire(now)
  }

  /** Whether the server's line from the anchor is exactly the prediction. */
  private matches(screen: EchoScreen): boolean {
    // Predicted empty, and the server's input is empty: whatever follows the
    // cursor is the agent's hint, which for opencode is not dim and would
    // otherwise never match.
    if (this.text.length === 0 && this.anchor === this.inputStart && screen.cursorX === this.anchor) {
      if (this.emptyInput(screen, screen.cursorX, this.row)) return true
    }
    let x = this.anchor
    for (const c of this.text) {
      const cell = screen.cell(x, this.row)
      const chars = cell?.chars ?? ''
      if (!(chars === c.ch || (c.ch === ' ' && (chars === '' || chars === ' ')))) return false
      x += c.w
    }
    if (screen.cursorX !== x) return false
    for (let i = x; i < this.end; i++) if (!blankish(screen.cell(i, this.row))) return false
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

type Launch = Pick<Session, 'launchProfileId' | 'launchCommand' | 'command'>

/**
 * Which agent a session is running, if it is one of the three this predicts
 * for.
 *
 * Three ways in, because a session remembers how it was started and not what
 * is running now: the built-in profile, the launch command, or the process in
 * the pane. The last one has a wrinkle worth knowing: Claude Code's native
 * install runs a binary named after its version
 * (`~/.local/share/claude/versions/2.1.283`), so tmux reports the pane's
 * command as `2.1.283` and never as `claude`. That is also the only way to
 * recognise an agent started by hand from a shell session.
 */
export function echoAgent(s: Launch | undefined): EchoAgent | null {
  if (!s) return null
  for (const agent of ['claude', 'codex', 'opencode'] as const) {
    if (s.launchProfileId === `builtin:${agent}`) return agent
  }
  const argv0 = (s.launchCommand[0] ?? '').split('/').pop()
  if (argv0 === 'claude' || argv0 === 'codex' || argv0 === 'opencode') return argv0
  if (s.command === 'claude' || /^\d+\.\d+\.\d+$/.test(s.command)) return 'claude'
  if (s.command === 'codex' || s.command === 'opencode') return s.command
  return null
}

/** Whether a session is Claude Code. */
export function isClaudeCode(s: Launch | undefined): boolean {
  return echoAgent(s) === 'claude'
}
