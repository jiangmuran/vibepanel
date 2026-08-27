import type { Terminal as Xterm } from '@xterm/xterm'

/**
 * Every live terminal, by session id, so that what is on screen can be read
 * back from outside the component.
 *
 * The WebGL renderer draws to a canvas. `.xterm-rows` is empty under it, and
 * every browser check that read the screen through the DOM went blind the
 * moment the renderer was loaded -- thirteen of them at once, each reporting an
 * empty terminal about a terminal that was full. The screen had not changed;
 * the only readable copy of it had.
 *
 * So the buffer is the source, which is the more truthful one anyway: the DOM
 * spans were a rendering of the buffer, and a check that reads the rendering
 * can be fooled by the renderer. `vibepanelScreen` reads and cannot write, and
 * it is the same text xterm's own accessibility layer would expose without
 * turning screen-reader mode on for everybody.
 *
 * In its own file rather than in Terminal.tsx, where it was, because the map is
 * now read by two things: the screen reader below, and `focusTerminal` in
 * focus.ts. Importing it from the component would have made focus.ts depend on
 * the component that depends on it -- and the alternative, exporting a plain
 * function from a .tsx, is the thing react-refresh warns about.
 */
export const liveTerminals = new Map<string, Xterm>()

declare global {
  interface Window {
    vibepanelScreen?: (arg?: string | { id?: string; all?: boolean }) => string[] | null
  }
}

if (typeof window !== 'undefined') {
  window.vibepanelScreen = (arg) => {
    const opts = typeof arg === 'string' ? { id: arg } : (arg ?? {})
    const all = [...liveTerminals.values()]
    // The focused one when nothing is named. A check that has just typed into a
    // terminal means *that* terminal, and the panel has several: a main one and
    // a row of scratch ones underneath. Picking the first in the map read the
    // wrong screen and reported that typing had produced no output.
    const term = opts.id
      ? liveTerminals.get(opts.id)
      : (all.find((t) => t.textarea === document.activeElement) ?? all[0])
    if (!term) return null
    const buf = term.buffer.active
    // The viewport by default, which is what "what is on screen" means and what
    // the DOM used to return. `all` reaches the scrollback, for the checks that
    // ask whether something from a minute ago survived.
    const from = opts.all ? 0 : buf.viewportY
    const to = opts.all ? buf.length : Math.min(buf.length, buf.viewportY + term.rows)
    const rows: string[] = []
    for (let i = from; i < to; i++) rows.push(buf.getLine(i)?.translateToString(true) ?? '')
    return rows
  }
}
