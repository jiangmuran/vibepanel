/**
 * The one keystroke the terminal does not get to keep: ctrl+V.
 *
 * A structure rather than a `KeyboardEvent`, for the reason `focus.ts` gives:
 * vitest runs these files in node, and a rule that can only be exercised
 * through a browser is a rule nothing checks between browser runs.
 */
export interface KeyPress {
  /** 'keydown', 'keyup', 'keypress' -- xterm hands its handler all three. */
  type: string
  /** The character the layout produces, so a non-QWERTY keyboard agrees. */
  key: string
  ctrlKey: boolean
  metaKey: boolean
  altKey: boolean
}

/**
 * Is this the browser's paste rather than a keystroke for the pty?
 *
 * xterm maps ctrl+letter to a control character and cancels the event, so
 * ctrl+V became `\x16` and the browser never fired `paste` at all: the only
 * way to get text into a session was the right-click menu. 「我按下 ctrl v 是在
 * 粘贴图片，而我实际上是文字剪贴板，只有右键再点击粘贴是粘贴」.
 *
 * What `\x16` did next is the half that made it look like a different bug.
 * It reached whatever the pane is running, and for Claude Code and Codex
 * ctrl+V means "paste an image from the clipboard" -- so the agent shelled out
 * to *the panel host's* clipboard and answered `Failed to paste image:
 * clipboard unavailable: ... X11 server connection timed out`. It is not a
 * broken clipboard. The clipboard being asked about is on the machine running
 * the agent, and the one holding the text is in front of the person, several
 * networks away. There is no arrangement in which `\x16` out of a browser tab
 * means what the agent takes it to mean, so it is not sent.
 *
 * Handing the keystroke back to the browser rather than reading the clipboard
 * here: `navigator.clipboard.readText()` needs a secure origin and a
 * permission, and this panel is served over plain http on a LAN more often
 * than not -- `clipboard.ts` exists because of that. The browser's own paste
 * needs neither. It is the same path the right-click menu already takes: the
 * event reaches xterm's hidden textarea, and xterm brackets it exactly as it
 * brackets a right-click paste.
 *
 * The cost, stated rather than discovered later: a literal ^V cannot be typed
 * into a session any more -- blockwise-visual in vim, quoted-insert in
 * readline. One of the two meanings of this key had to win, and the other one
 * is somebody's clipboard.
 *
 * Three details that are load-bearing:
 *
 *   - Only `keydown`. xterm calls its handler for keyup as well, and answering
 *     there would skip the refocus and cursor update it does on the way out.
 *   - `!altKey`, because AltGr is ctrl+alt on Windows and Linux. A layout
 *     where AltGr+V types a character would have that character swallowed by
 *     a handler that only looked at ctrl.
 *   - `key`, not `code`. The browser pastes for the key that *produces* v, and
 *     so does xterm; on Dvorak `code` is the physical position and would claim
 *     ctrl+W instead.
 */
export function isBrowserPaste(e: KeyPress): boolean {
  if (e.type !== 'keydown') return false
  if (e.altKey || !(e.ctrlKey || e.metaKey)) return false
  return e.key === 'v' || e.key === 'V'
}
