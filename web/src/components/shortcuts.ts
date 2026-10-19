/**
 * The panel's own keys, which work from inside a terminal.
 *
 * There were none: a keyboard could not switch sessions at all, and Tab
 * inside a terminal is the terminal's (completion, an agent's own menus), so
 * once there it could not leave either. Ctrl+K is the terminal's too (kill to
 * the end of the line), and ⌥/Alt + ← → move by word -- those two are only the
 * panel's when the setting says so. See panelShortcut for the whole table.
 */
export type PanelAction =
  | 'prev'
  | 'next'
  | 'leave'
  | 'prevProject'
  | 'nextProject'
  | 'goto'
  | 'regionNext'
  | 'regionPrev'
  | 'help'

/** What a key asks the panel to do; `n` is the session number for `goto`. */
export interface PanelShortcut {
  act: PanelAction
  n?: number
}

/** What the answer depends on besides the key. */
export interface ShortcutOptions {
  /** ⌥Tab exists only where the window manager has not taken Alt+Tab. */
  mac?: boolean
  /** Whether ⌥/Alt + ← → belong to the panel or to the terminal. */
  sideArrows?: 'terminal' | 'panel'
}

/** Enough of a DOM element to say whether it edits text. */
export interface KeyTarget {
  tagName?: string
  type?: string
  isContentEditable?: boolean
  classList?: { contains(name: string): boolean }
}

const NOT_TEXT = new Set(['button', 'checkbox', 'radio', 'range', 'submit', 'reset', 'file', 'color', 'image'])

/**
 * Whether a key pressed here belongs to a text field.
 *
 * Ctrl+Shift+Left and Right select by word in every input and textarea, and
 * Ctrl+Shift+Up and Down by paragraph in some; a window listener that took
 * them moved the person to the sidebar in the middle of selecting a word in
 * the notes, a settings field or the phone's compose box. Except xterm's own
 * helper textarea: it is where a terminal's keyboard focus sits, and the
 * panel's keys exist above all for there.
 */
function editsText(t: KeyTarget | null | undefined): boolean {
  if (!t) return false
  if (t.classList?.contains('xterm-helper-textarea')) return false
  if (t.isContentEditable) return true
  const tag = (t.tagName ?? '').toUpperCase()
  if (tag === 'TEXTAREA') return true
  if (tag === 'INPUT') return !NOT_TEXT.has((t.type ?? 'text').toLowerCase())
  return false
}

/**
 * The keys, in two families.
 *
 * Ctrl+Shift with an arrow was the first: nothing in a shell or an agent binds
 * it. ⌥/Alt is the second, asked for by name -- ⌥↑ ⌥↓ to move between
 * sessions, ⌥1…⌥9 to jump to one, ⌥/ for the list of keys, and on a Mac ⌥Tab to
 * move between the sidebar, the terminal, the side panel and the bottom strip.
 * ⌥← ⌥→ are the terminal's unless the setting gives them to the panel, where
 * they move between projects.
 *
 * Read by `code` as well as `key` for the digits, the slash and Tab: on a Mac
 * ⌥1 arrives as key "¡" and ⌥/ as "÷", and the physical key is what was meant.
 * Ctrl together with Alt is never ours, because that is AltGr on a European
 * Windows layout, and it types characters.
 */
export function panelShortcut(
  e: {
    key: string
    code?: string
    ctrlKey: boolean
    shiftKey: boolean
    altKey: boolean
    metaKey: boolean
    type?: string
    target?: unknown
  },
  opts: ShortcutOptions = {},
): PanelShortcut | null {
  if (e.type !== undefined && e.type !== 'keydown') return null
  if (editsText(e.target as KeyTarget | null | undefined)) return null
  if (e.metaKey) return null

  if (e.ctrlKey && e.shiftKey && !e.altKey) {
    switch (e.key) {
      case 'ArrowUp':
        return { act: 'prev' }
      case 'ArrowDown':
        return { act: 'next' }
      case 'ArrowLeft':
        return { act: 'leave' }
    }
    return null
  }

  if (!e.altKey || e.ctrlKey) return null
  const code = e.code ?? ''
  if ((code === 'Tab' || e.key === 'Tab') && opts.mac) {
    return { act: e.shiftKey ? 'regionPrev' : 'regionNext' }
  }
  if (e.shiftKey) return null
  const digit = /^Digit([1-9])$/.exec(code)
  if (digit) return { act: 'goto', n: Number(digit[1]) }
  if (code === 'Slash') return { act: 'help' }
  switch (e.key) {
    case 'ArrowUp':
      return { act: 'prev' }
    case 'ArrowDown':
      return { act: 'next' }
    case 'ArrowLeft':
      return opts.sideArrows === 'panel' ? { act: 'prevProject' } : null
    case 'ArrowRight':
      return opts.sideArrows === 'panel' ? { act: 'nextProject' } : null
  }
  return null
}

/** The session after or before `current` in `order`, wrapping. */
export function neighbour(order: string[], current: string | null, dir: 'prev' | 'next'): string | null {
  if (order.length === 0) return null
  const i = current ? order.indexOf(current) : -1
  if (i < 0) return order[0]
  const step = dir === 'next' ? 1 : -1
  return order[(i + step + order.length) % order.length]
}
