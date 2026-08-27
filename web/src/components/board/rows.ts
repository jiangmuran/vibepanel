import type { ShareSession } from '../../protocol/wire'

/**
 * Which rows a session widget shows, and in what order.
 *
 * In their own file rather than beside the components, because both widgets use
 * them and so does the "does anything need me" tile — and because a module that
 * exports a component and a helper loses fast refresh for the component.
 */

/** Which rows a widget's filter keeps. */
export function filterRows(rows: ShareSession[], filter: string): ShareSession[] {
  switch (filter) {
    case 'waiting':
      return rows.filter((r) => !r.exited && r.state === 'waiting')
    case 'active':
      return rows.filter((r) => !r.exited)
    case 'trouble':
      // Ended, and not cleanly. Deliberately not "state is waiting": a session
      // waiting for an answer is working exactly as designed, and a board that
      // called that trouble would be red all day. EXIT_VANISHED is included by
      // being non-zero — a tmux session that disappeared is a thing that went
      // wrong, even though nothing was around to see how.
      return rows.filter((r) => r.exited && r.exitStatus !== 0)
    default:
      return rows
  }
}

const STATE_WEIGHT: Record<string, number> = { waiting: 0, working: 1, done: 2 }

/** How a widget's order sorts them. */
export function orderRows(rows: ShareSession[], order: string): ShareSession[] {
  const out = [...rows]
  if (order === 'cpu') {
    out.sort((a, b) => b.cpuPercent - a.cpuPercent)
    return out
  }
  if (order === 'waited') {
    // Oldest state change first, so the thing that has been ignored longest is
    // at the top. A session with no timestamp sorts last rather than first,
    // which is where a zero would have put it.
    out.sort((a, b) => (a.stateChangedAt || Infinity) - (b.stateChangedAt || Infinity))
    return out
  }
  out.sort(
    (a, b) =>
      (STATE_WEIGHT[a.state] ?? 3) - (STATE_WEIGHT[b.state] ?? 3) ||
      (a.stateChangedAt || Infinity) - (b.stateChangedAt || Infinity),
  )
  return out
}

