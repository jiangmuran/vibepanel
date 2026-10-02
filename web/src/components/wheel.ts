/**
 * Wheel events for an application that asked for the mouse, on a desktop.
 *
 * xterm sends one report per wheel *event* once enough pixels have added up
 * to a row, whatever the event's size. A terminal emulator sends one per row
 * of travel: that is what the application's scroll is calibrated against, and
 * why a full-screen program moves about as far as the wheel did. Measured
 * against pi 1.0's transcript, which moves one line per report: six notches of
 * a mouse moved it six lines, and 320px of trackpad travel moved it six lines
 * too -- 「无法滚动」, scrolling that is technically happening. The touch path
 * in `touchSelect.ts` has its own version of this arithmetic because a finger
 * is not a wheel; this is the pointer's.
 *
 * The count is rows of travel, carried across events so a trackpad's small
 * deltas are not each rounded to nothing, and bounded so one runaway event
 * cannot flood the pty.
 */

/** DOM `WheelEvent.deltaMode` values, named. */
const DELTA_PIXEL = 0
const DELTA_LINE = 1
const DELTA_PAGE = 2

/** Reports in one event, at most. A page of a tall terminal is about this. */
export const MAX_REPORTS_PER_EVENT = 40

export interface WheelPlan {
  /** Reports to send. Zero when the travel so far rounds to nothing. */
  count: number
  /** Wheel-up (towards what came before), the direction every report shares. */
  up: boolean
  /** Fractional rows left over, to carry into the next event. */
  carry: number
}

/**
 * How many wheel reports one event is worth.
 *
 * `rowHeight` is in the same CSS pixels as `deltaY`; `rows` is the grid height,
 * for page-mode deltas. `carry` is the previous plan's remainder.
 */
export function wheelReports(
  deltaY: number,
  deltaMode: number,
  rowHeight: number,
  rows: number,
  carry: number,
): WheelPlan {
  if (!(rowHeight > 0) || !(rows > 0) || !Number.isFinite(deltaY)) {
    return { count: 0, up: deltaY < 0, carry }
  }
  let lines: number
  switch (deltaMode) {
    case DELTA_LINE:
      lines = deltaY
      break
    case DELTA_PAGE:
      lines = deltaY * rows
      break
    case DELTA_PIXEL:
    default:
      lines = deltaY / rowHeight
      break
  }
  // A change of direction drops the carry: the remainder of a scroll one way
  // is not a head start the other way.
  const total = (carry < 0) === (lines < 0) || carry === 0 ? carry + lines : lines
  const whole = Math.trunc(total)
  const count = Math.min(Math.abs(whole), MAX_REPORTS_PER_EVENT)
  return { count, up: total < 0, carry: total - whole }
}
