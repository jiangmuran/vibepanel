/**
 * JSON as a tree to draw: the rows on screen for what is expanded, and the
 * nodes a search finds. Pure, so the folding rules are tested.
 *
 * A node is named by its JSON Pointer (RFC 6901): `/a/0/b`, with `~` and `/`
 * in a key written `~0` and `~1`. That is what lets a match deep inside be
 * revealed -- its ancestors are its pointer's prefixes -- without the tree
 * keeping parent links.
 */

export type JsonType = 'object' | 'array' | 'string' | 'number' | 'boolean' | 'null'

/** Children of an array drawn before the rest are folded into one row. */
export const ARRAY_FOLD = 200

/** A document with fewer nodes than this opens fully expanded. */
const EXPAND_ALL_BELOW = 400

function typeOf(v: unknown): JsonType {
  if (v === null) return 'null'
  if (Array.isArray(v)) return 'array'
  return typeof v === 'object' ? 'object' : (typeof v as 'string' | 'number' | 'boolean')
}

const esc = (k: string) => k.replace(/~/g, '~0').replace(/\//g, '~1')

export function childPath(parent: string, key: string | number): string {
  return `${parent}/${typeof key === 'number' ? key : esc(key)}`
}

/** The pointers of every container above a node, outermost first. */
export function ancestors(path: string): string[] {
  const out = ['']
  let at = 0
  for (;;) {
    at = path.indexOf('/', at + 1)
    if (at < 0) break
    out.push(path.slice(0, at))
  }
  return out
}

/**
 * The document, or null when it is not one to draw as a tree: it does not
 * parse, or it was cut by the server's bound (half a document does not
 * parse, and parsing what is left of one would show a wrong tree).
 *
 * JSON Lines is a list of documents, one a line: the tree's root is that list.
 */
export function parseJson(text: string, name: string, truncated: boolean): { value: unknown } | null {
  if (truncated) return null
  try {
    if (/\.(jsonl|ndjson)$/i.test(name)) {
      const lines = text.split(/\r?\n/).filter((l) => l.trim() !== '')
      return { value: lines.map((l) => JSON.parse(l)) }
    }
    return { value: JSON.parse(text) }
  } catch {
    return null
  }
}

export function isJson(name: string): boolean {
  return /\.(json|jsonl|ndjson|webmanifest)$/i.test(name)
}

export interface Row {
  path: string
  depth: number
  /** The key in its parent: a name, an index, or null for the root. */
  key: string | number | null
  type: JsonType
  value: unknown
  /** Children, for a container. */
  size: number
  expanded: boolean
  /** A folded run of an array: how many children it stands for. */
  folded?: number
}

/** How many nodes a value is, counting itself, stopping once past `limit`. */
function count(v: unknown, limit: number): number {
  let n = 0
  const walk = (x: unknown) => {
    if (n > limit) return
    n++
    if (x && typeof x === 'object') for (const c of Object.values(x)) walk(c)
  }
  walk(v)
  return n
}

/**
 * What opens expanded: everything for a small document, the root and its
 * children's first level for a large one -- enough to see its shape without
 * laying out a hundred thousand rows.
 */
export function initialExpanded(value: unknown): Set<string> {
  const out = new Set<string>([''])
  if (count(value, EXPAND_ALL_BELOW) < EXPAND_ALL_BELOW) {
    const walk = (v: unknown, path: string) => {
      if (!v || typeof v !== 'object') return
      out.add(path)
      for (const [k, c] of entries(v)) walk(c, childPath(path, k))
    }
    walk(value, '')
  }
  return out
}

function entries(v: object): [string | number, unknown][] {
  return Array.isArray(v) ? v.map((c, i) => [i, c]) : Object.entries(v)
}

/**
 * The rows on screen: a container's children when it is expanded, an array's
 * first ARRAY_FOLD and then one row for the rest unless that row has been
 * opened (its pointer with `#all` in the set).
 */
export function visibleRows(value: unknown, expanded: ReadonlySet<string>): Row[] {
  const rows: Row[] = []
  const walk = (v: unknown, path: string, key: string | number | null, depth: number) => {
    const type = typeOf(v)
    const container = type === 'object' || type === 'array'
    const kids = container ? entries(v as object) : []
    const open = container && expanded.has(path)
    rows.push({ path, depth, key, type, value: v, size: kids.length, expanded: open })
    if (!open) return
    const all = type !== 'array' || expanded.has(`${path}#all`)
    const shown = all ? kids : kids.slice(0, ARRAY_FOLD)
    for (const [k, c] of shown) walk(c, childPath(path, k), k, depth + 1)
    if (!all && kids.length > ARRAY_FOLD) {
      rows.push({
        path: `${path}#all`,
        depth: depth + 1,
        key: null,
        type: 'array',
        value: null,
        size: 0,
        expanded: false,
        folded: kids.length - ARRAY_FOLD,
      })
    }
  }
  walk(value, '', null, 0)
  return rows
}

/**
 * The text a node is searched by: its name, and its value when it is not a
 * container. An array index is not a name: counted, a search for `1` would
 * find the second item of every array in the document.
 */
function nodeText(key: string | number | null, v: unknown): string {
  const k = typeof key === 'string' ? key : ''
  const t = typeOf(v)
  return t === 'object' || t === 'array' ? k : `${k} ${t === 'string' ? (v as string) : String(v)}`
}

/** The most matches counted, as in the line body. */
export const MAX_TREE_MATCHES = 10_000

/**
 * Every node whose key or value holds the query, in document order, with the
 * line body's smart case: lower case finds any case, a capital means it.
 */
export function treeMatches(value: unknown, query: string): { paths: string[]; capped: boolean } {
  const paths: string[] = []
  if (query === '') return { paths, capped: false }
  const exact = query !== query.toLowerCase()
  const needle = exact ? query : query.toLowerCase()
  let capped = false
  const walk = (v: unknown, path: string, key: string | number | null) => {
    if (capped) return
    const text = nodeText(key, v)
    if ((exact ? text : text.toLowerCase()).includes(needle)) {
      if (paths.length === MAX_TREE_MATCHES) {
        capped = true
        return
      }
      paths.push(path)
    }
    if (v && typeof v === 'object') for (const [k, c] of entries(v)) walk(c, childPath(path, k), k)
  }
  walk(value, '', null)
  return { paths, capped }
}

/**
 * What has to be expanded to show a node: its ancestors, and the `#all` of
 * any array that folds it away.
 */
export function revealing(path: string): string[] {
  const out: string[] = []
  const parts = path.split('/').slice(1)
  let at = ''
  for (const p of parts) {
    out.push(at)
    if (/^\d+$/.test(p) && Number(p) >= ARRAY_FOLD) out.push(`${at}#all`)
    at = `${at}/${p}`
  }
  return out
}
