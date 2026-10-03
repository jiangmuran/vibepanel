/**
 * Mouse reports for an application that asked for the mouse, from a viewer
 * that is drawn at somebody else's grid.
 *
 * A passive viewer renders the owner's grid and is scaled to fit with a CSS
 * transform (Terminal.tsx, "passive"). xterm maps a pointer to a cell by
 * dividing its distance from the screen's edge by the cell size it rendered
 * at -- the unscaled one -- so at 0.69 scale a click on column 40, row 10
 * was reported as column 28, row 7. pi 1.0 runs full-screen with mouse
 * reporting on, and from the second browser nothing in it could be clicked.
 * The touch path never had this problem because it measures the box the
 * finger is in; this is the same arithmetic for a pointer, in the SGR
 * encoding, which is the only one a modern program asks for.
 */

export type MouseKind = 'down' | 'up' | 'move'

export interface Modifiers {
  shift?: boolean
  alt?: boolean
  ctrl?: boolean
}

/** The DOM button number, 0 left, 1 middle, 2 right. */
export function mouseButtonCode(button: number, mods: Modifiers, motion: boolean): number {
  // 3 is "no button", which is what a motion report without a button held
  // says in any-motion mode.
  let code = button >= 0 && button <= 2 ? button : 3
  if (mods.shift) code += 4
  if (mods.alt) code += 8
  if (mods.ctrl) code += 16
  if (motion) code += 32
  return code
}

/**
 * One SGR (1006) report. Columns and rows are 0-based here and 1-based on the
 * wire. A release is the same code with a final `m`.
 */
export function sgrMouseReport(kind: MouseKind, button: number, col: number, row: number, mods: Modifiers = {}): string {
  const code = mouseButtonCode(button, mods, kind === 'move')
  return `\x1b[<${code};${col + 1};${row + 1}${kind === 'up' ? 'm' : 'M'}`
}

/**
 * Whether a report of this kind is wanted under the tracking mode xterm
 * reports (`term.modes.mouseTrackingMode`): x10 is presses only, vt200 adds
 * releases, drag adds motion with a button held, any adds all motion.
 */
export function wantsReport(mode: string, kind: MouseKind, buttonHeld: boolean): boolean {
  switch (mode) {
    case 'x10':
      return kind === 'down'
    case 'vt200':
      return kind !== 'move'
    case 'drag':
      return kind !== 'move' || buttonHeld
    case 'any':
      return true
    default:
      return false
  }
}
