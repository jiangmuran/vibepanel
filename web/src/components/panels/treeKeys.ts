/**
 * The Files tab's keyboard, as a function of the event: the table in
 * docs/file-preview.md, "Keys", for the tab. Tested as a table, as the
 * viewer's keys are, because which keys are taken from a text field is the
 * thing that drifts.
 */

import { isEnter } from '../enter'

export type TreeAction =
  | { t: 'down' }
  | { t: 'up' }
  | { t: 'first' }
  | { t: 'last' }
  | { t: 'parent' }
  | { t: 'clearFilter' }
  | { t: 'type'; char: string }
  | { t: 'backspaceFilter' }

export interface TreeKeyLike {
  key: string
  ctrlKey: boolean
  metaKey: boolean
  altKey: boolean
  isComposing?: boolean
  keyCode?: number
}

/**
 * The action a key asks for in the listing, or null to leave it alone.
 *
 * `filtering` is whether a filter is being typed: then Backspace takes a
 * character off it rather than going up a level, and Escape clears it. Enter
 * is not here -- the row is a button, and a button already opens on Enter.
 * A key with Ctrl, ⌘ or ⌥ is somebody else's: the panel's own shortcuts, the
 * browser's, an ⌥ character on a Mac.
 */
export function treeAction(e: TreeKeyLike, filtering: boolean): TreeAction | null {
  if (e.isComposing || e.keyCode === 229) return null
  if (e.ctrlKey || e.metaKey || e.altKey) return null
  if (isEnter(e)) return null
  switch (e.key) {
    case 'ArrowDown':
      return { t: 'down' }
    case 'ArrowUp':
      return { t: 'up' }
    case 'Home':
      return { t: 'first' }
    case 'End':
      return { t: 'last' }
    case 'ArrowLeft':
      return { t: 'parent' }
    case 'Backspace':
      return filtering ? { t: 'backspaceFilter' } : { t: 'parent' }
    case 'Escape':
      return filtering ? { t: 'clearFilter' } : null
  }
  // One printable character: a name being typed. Space is one, except as
  // the first, where it would be nothing anybody types to find a file.
  if ([...e.key].length === 1 && (filtering || e.key !== ' ')) return { t: 'type', char: e.key }
  return null
}

/**
 * The entries whose name holds the filter, in the order they were listed.
 * Lower case finds any case and a capital means it, as find does in the
 * viewer.
 */
export function filterEntries<T extends { name: string }>(entries: readonly T[], filter: string): readonly T[] {
  if (filter === '') return entries
  const exact = filter !== filter.toLowerCase()
  const needle = exact ? filter : filter.toLowerCase()
  return entries.filter((e) => (exact ? e.name : e.name.toLowerCase()).includes(needle))
}

/** The row after a move, kept inside the list. */
export function moveRow(at: number, count: number, a: TreeAction): number {
  if (count === 0) return -1
  switch (a.t) {
    case 'down':
      return Math.min(count - 1, at + 1)
    case 'up':
      return Math.max(0, at - 1)
    case 'first':
      return 0
    case 'last':
      return count - 1
    default:
      return Math.min(Math.max(at, 0), count - 1)
  }
}
