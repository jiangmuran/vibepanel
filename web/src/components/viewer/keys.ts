import { isEnter } from '../enter'

/**
 * What a key does in the viewer: the table in docs/file-preview.md, "Keys",
 * as a function of the event and nothing else.
 *
 * A function so the table is tested rather than read: which keys work while
 * the find box has focus, and which modifiers turn a letter into somebody
 * else's shortcut, are exactly the things that drift when a handler is edited
 * by hand.
 */

export type ViewerAction =
  | 'close'
  | 'closeFind'
  | 'prevFile'
  | 'nextFile'
  | 'find'
  | 'goToLine'
  | 'nextMatch'
  | 'prevMatch'
  | 'toggleWrap'
  | 'toggleLineNumbers'
  | 'toggleSource'
  | 'zoomIn'
  | 'zoomOut'
  | 'zoomReset'
  | 'download'
  | 'copyPath'
  | 'prevPage'
  | 'nextPage'
  | 'firstPage'
  | 'lastPage'
  | 'selectAll'
  | 'edit'
  | 'save'
  | 'stopEditing'

export interface KeyLike {
  key: string
  ctrlKey: boolean
  metaKey: boolean
  altKey: boolean
  shiftKey: boolean
  /** An IME composition in progress: the key is the composition's. */
  isComposing?: boolean
  /** 229 is Safari's way of saying the same while `isComposing` is false. */
  keyCode?: number
}

export interface KeyContext {
  /** The focus is in the find box or the go-to-line box. */
  inField: boolean
  /** The find bar is open. */
  findOpen: boolean
  /** ⌘ rather than Ctrl is the platform's command key. */
  mac: boolean
  /**
   * The file on screen has pages (a PDF). Only then are PgUp, PgDn, `g` and
   * `G` the viewer's: everywhere else PgUp and PgDn are the browser's scroll,
   * and taking them would leave a log with no way to page through it.
   */
  pages?: boolean
  /**
   * The body is the editor. Every key is then the editor's -- letters, the
   * command-key shortcuts, select-all, its own find -- except saving and
   * Escape, which leaves it.
   */
  editing?: boolean
}

/**
 * The action a key asks for, or null to leave the event alone.
 *
 * Inside a field only the keys that make sense there act -- Escape, Enter for
 * stepping through matches, and the command-key shortcuts -- and every bare
 * letter is typing. Outside one, a bare letter acts only with no modifier at
 * all: Ctrl+C is copying the selection and ⌥C on a Mac is a character, and
 * neither is "copy the path".
 */
export function viewerAction(e: KeyLike, ctx: KeyContext): ViewerAction | null {
  if (e.isComposing || e.keyCode === 229) return null
  const command = ctx.mac ? e.metaKey && !e.ctrlKey : e.ctrlKey && !e.metaKey
  if (ctx.editing) {
    if (command && !e.altKey && e.key.toLowerCase() === 's') return 'save'
    if (e.key === 'Escape' && !e.ctrlKey && !e.metaKey && !e.altKey && !e.shiftKey) return 'stopEditing'
    return null
  }
  if (command && !e.altKey) {
    const k = e.key.toLowerCase()
    if (k === 'f') return 'find'
    if (k === 'g') return 'goToLine'
    // The body's text and nothing around it. In a field it is the field's:
    // the find box's text is what somebody selecting there means.
    if (k === 'a') return ctx.inField ? null : 'selectAll'
    return null
  }
  if (e.key === 'Escape') return ctx.findOpen ? 'closeFind' : 'close'
  if (isEnter(e) && ctx.findOpen && !e.ctrlKey && !e.metaKey && !e.altKey) {
    return e.shiftKey ? 'prevMatch' : 'nextMatch'
  }
  if (ctx.inField) return null
  if (e.ctrlKey || e.metaKey || e.altKey) return null
  if (ctx.pages) {
    switch (e.key) {
      case 'PageUp':
        return 'prevPage'
      case 'PageDown':
        return 'nextPage'
      // Shift is what makes `G`; the key is the letter it made.
      case 'g':
        return 'firstPage'
      case 'G':
        return 'lastPage'
    }
  }
  switch (e.key) {
    case '[':
      return 'prevFile'
    case ']':
      return 'nextFile'
    case 'w':
      return 'toggleWrap'
    case 'n':
      return 'toggleLineNumbers'
    case 's':
      return 'toggleSource'
    // `=` is `+` without Shift on most layouts; both zoom in, as in every
    // browser.
    case '+':
    case '=':
      return 'zoomIn'
    case '-':
      return 'zoomOut'
    case '0':
      return 'zoomReset'
    case 'd':
      return 'download'
    case 'c':
      return 'copyPath'
    case 'e':
      return 'edit'
  }
  return null
}
