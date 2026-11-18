/**
 * An archive's table of contents as a tree: directories folded, opened by
 * hand or by find. Pure, so what is shown where -- and where a name that
 * climbs out of its directory is shown -- is tested.
 */

import type { ArchiveEntry } from '../../protocol/listings'

export interface Node {
  /** The key: the entry's path for a file, its path with a `/` for a directory. */
  id: string
  name: string
  dir: boolean
  /** -1 for a directory, and for a file whose size the archive does not hold. */
  size: number
  modified: number
  /** The time is a wall-clock reading: shown in UTC (listings.ts). */
  wall: boolean
  unsafe: boolean
  children: Node[]
  /** Files and directories under a directory, all the way down. */
  count: number
}

/**
 * The tree. A directory is there whether the archive lists it or only names
 * files inside it, as most zips do.
 *
 * An unsafe name is not split into the tree: `../../etc/passwd` filed under a
 * directory called `..` would show it as the harmless thing it is not. Each
 * one is a row of its own at the top, under the name the archive gives it.
 */
export function buildTree(entries: readonly ArchiveEntry[]): Node {
  const root: Node = { id: '/', name: '', dir: true, size: -1, modified: 0, wall: false, unsafe: false, children: [], count: 0 }
  const dirs = new Map<string, Node>([['', root]])
  const dirFor = (parts: string[]): Node => {
    let at = root
    let key = ''
    for (const part of parts) {
      key = key === '' ? part : `${key}/${part}`
      let d = dirs.get(key)
      if (!d) {
        d = { id: `${key}/`, name: part, dir: true, size: -1, modified: 0, wall: false, unsafe: false, children: [], count: 0 }
        dirs.set(key, d)
        at.children.push(d)
      }
      at = d
    }
    return at
  }
  const unsafe: Node[] = []
  for (const e of entries) {
    if (e.unsafe) {
      unsafe.push({ id: `!${e.path}`, name: e.path, dir: e.isDir, size: e.isDir ? -1 : e.size, modified: e.modified, wall: e.wall, unsafe: true, children: [], count: 0 })
      continue
    }
    const parts = e.path.split('/').filter((p) => p !== '' && p !== '.')
    if (parts.length === 0) continue
    if (e.isDir) {
      const d = dirFor(parts)
      d.modified = e.modified
      d.wall = e.wall
      continue
    }
    const name = parts.pop()!
    const parent = dirFor(parts)
    // A file listed twice -- a zip appended to, a tar with a later copy --
    // is shown twice: which one an extractor keeps is the extractor's, and
    // the listing says what is there.
    parent.children.push({ id: `${e.path}#${parent.children.length}`, name, dir: false, size: e.size, modified: e.modified, wall: e.wall, unsafe: false, children: [], count: 0 })
  }
  const sort = (n: Node): number => {
    n.children.sort((a, b) => (a.dir !== b.dir ? (a.dir ? -1 : 1) : a.name < b.name ? -1 : a.name > b.name ? 1 : 0))
    n.count = n.children.reduce((k, c) => k + 1 + sort(c), 0)
    return n.count
  }
  sort(root)
  root.children.unshift(...unsafe)
  return root
}

/**
 * Open at the start: the directories an archive is wrapped in. `pkg-1.0/`
 * holding everything, or `pkg-1.0/src/` holding everything again, says
 * nothing until it is open, so a chain of only children is opened down to the
 * first directory with more than one thing in it.
 */
export function initialOpen(root: Node): Set<string> {
  const open = new Set<string>()
  let at = root
  while (at.children.length === 1 && at.children[0].dir && !at.children[0].unsafe) {
    at = at.children[0]
    open.add(at.id)
  }
  return open
}

export interface Row {
  node: Node
  depth: number
  open: boolean
  /** The directory's ancestors' ids, so find can open them. */
  parents: string[]
}

export function visibleRows(root: Node, open: ReadonlySet<string>): Row[] {
  const out: Row[] = []
  const walk = (n: Node, depth: number, parents: string[]) => {
    for (const c of n.children) {
      const isOpen = c.dir && open.has(c.id)
      out.push({ node: c, depth, open: isOpen, parents })
      if (isOpen) walk(c, depth + 1, [...parents, c.id])
    }
  }
  walk(root, 0, [])
  return out
}

/**
 * Every node whose name holds the query, in the order the tree shows them
 * fully open, each with the directories that hold it. Smart case, as
 * everywhere else in the viewer.
 */
export function archiveMatches(root: Node, query: string): { id: string; parents: string[] }[] {
  const out: { id: string; parents: string[] }[] = []
  if (query === '') return out
  const exact = query !== query.toLowerCase()
  const needle = exact ? query : query.toLowerCase()
  const walk = (n: Node, parents: string[]) => {
    for (const c of n.children) {
      if ((exact ? c.name : c.name.toLowerCase()).includes(needle)) out.push({ id: c.id, parents })
      if (c.dir) walk(c, [...parents, c.id])
    }
  }
  walk(root, [])
  return out
}

/**
 * A modification time as date and minutes; null when there is none. An
 * instant in the reader's zone; a wall-clock reading in UTC, which is the
 * time as it was written -- in the reader's zone it would move by the
 * zone's offset, eight hours in California, for a time that was never UTC.
 */
export function stamp(seconds: number, wall = false): string | null {
  if (seconds <= 0) return null
  const d = new Date(seconds * 1000)
  const p = (n: number) => String(n).padStart(2, '0')
  if (wall) return `${d.getUTCFullYear()}-${p(d.getUTCMonth() + 1)}-${p(d.getUTCDate())} ${p(d.getUTCHours())}:${p(d.getUTCMinutes())}`
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}
