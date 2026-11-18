/**
 * The two listings the viewer draws an archive and a database from
 * (docs/api.md, archive-entries and sqlite), read field by field.
 *
 * Both are JSON the server built from a file in a project, so every name in
 * them is the file's. They are checked here rather than cast: a field of the
 * wrong type is dropped, never handed on as `undefined` to a component that
 * assumed it, and a cell is one of the shapes the documentation lists or it
 * is `unknown`.
 */

export interface ArchiveEntry {
  path: string
  /** -1 where the archive does not record it: a gzip stream. */
  size: number
  /** Unix seconds, 0 when the archive does not say. */
  modified: number
  /**
   * `modified` is a wall-clock reading read as UTC, not an instant: a zip's
   * DOS time, written in whatever zone the zipping machine was in. Shown in
   * UTC it is the time as written.
   */
  wall: boolean
  isDir: boolean
  /** A name an extractor could be led outside its directory by. */
  unsafe: boolean
}

export type ArchiveFormat = 'zip' | 'tar' | 'tar.gz' | 'gzip'

export interface ArchiveListing {
  format: ArchiveFormat
  entries: ArchiveEntry[]
  /** The listing stopped at one of the server's bounds. */
  truncated: boolean
  /** The server's sentence about a bound, a damaged tail or a gzip's size. */
  note: string
}

export interface DBColumn {
  name: string
  type: string
}

export interface DBTable {
  name: string
  kind: 'table' | 'view'
  /** -1 when it was not counted. */
  rows: number
  columns: DBColumn[]
  sql: string
}

/** One value, in the shapes the documentation gives and nothing else. */
export type DBCell =
  | { kind: 'null' }
  | { kind: 'number'; value: number }
  | { kind: 'text'; value: string; more: boolean }
  /** An INTEGER a double does not hold exactly, as its digits. */
  | { kind: 'int'; value: string }
  /** An infinity or a NaN, as SQLite prints it. */
  | { kind: 'real'; value: string }
  | { kind: 'blob'; bytes: number }
  | { kind: 'unknown' }

export interface DBListing {
  tables: DBTable[]
  /** The table the rows are from; empty when there are no tables. */
  table: string
  columns: DBColumn[]
  rows: DBCell[][]
  more: boolean
  note: string
}

const obj = (v: unknown): Record<string, unknown> =>
  v !== null && typeof v === 'object' && !Array.isArray(v) ? (v as Record<string, unknown>) : {}
const str = (v: unknown): string => (typeof v === 'string' ? v : '')
const num = (v: unknown, otherwise: number): number => (typeof v === 'number' && Number.isFinite(v) ? v : otherwise)
const list = (v: unknown): unknown[] => (Array.isArray(v) ? v : [])

/** For the footer: files, directories, and the files' total size where known. */
export function archiveSummary(entries: readonly ArchiveEntry[]): { files: number; dirs: number; bytes: number; sized: boolean } {
  let files = 0
  let bytes = 0
  let sized = true
  const dirs = new Set<string>()
  for (const e of entries) {
    if (e.isDir) {
      dirs.add(e.path.replace(/\/+$/, ''))
      continue
    }
    files++
    if (e.size < 0) sized = false
    else bytes += e.size
    if (e.unsafe) continue
    const parts = e.path.split('/').filter((p) => p !== '' && p !== '.')
    for (let i = 1; i < parts.length; i++) dirs.add(parts.slice(0, i).join('/'))
  }
  return { files, dirs: dirs.size, bytes, sized }
}

const FORMATS: readonly ArchiveFormat[] = ['zip', 'tar', 'tar.gz', 'gzip']

/** An archive listing, or null when the answer is not one. */
export function readArchiveListing(body: unknown): ArchiveListing | null {
  const b = obj(body)
  const format = FORMATS.find((f) => f === b.format)
  if (!format || !Array.isArray(b.entries)) return null
  const entries: ArchiveEntry[] = []
  for (const raw of b.entries) {
    const e = obj(raw)
    if (typeof e.path !== 'string') continue
    entries.push({
      path: e.path,
      size: num(e.size, -1),
      modified: num(e.modified, 0),
      wall: e.wall === true,
      isDir: e.isDir === true,
      unsafe: e.unsafe === true,
    })
  }
  return { format, entries, truncated: b.truncated === true, note: str(b.note) }
}

function column(raw: unknown): DBColumn {
  const c = obj(raw)
  return { name: str(c.name), type: str(c.type) }
}

export function readCell(v: unknown): DBCell {
  if (v === null) return { kind: 'null' }
  if (typeof v === 'number') return Number.isFinite(v) ? { kind: 'number', value: v } : { kind: 'unknown' }
  if (typeof v === 'string') return { kind: 'text', value: v, more: false }
  const o = obj(v)
  if (typeof o.int === 'string') return { kind: 'int', value: o.int }
  if (typeof o.real === 'string') return { kind: 'real', value: o.real }
  if (typeof o.blob === 'number' && Number.isFinite(o.blob)) return { kind: 'blob', bytes: o.blob }
  if (typeof o.text === 'string') return { kind: 'text', value: o.text, more: o.more === true }
  return { kind: 'unknown' }
}

/** A database listing, or null when the answer is not one. */
export function readDBListing(body: unknown): DBListing | null {
  const b = obj(body)
  if (!Array.isArray(b.tables)) return null
  const tables: DBTable[] = []
  for (const raw of b.tables) {
    const t = obj(raw)
    if (typeof t.name !== 'string') continue
    tables.push({
      name: t.name,
      kind: t.kind === 'view' ? 'view' : 'table',
      rows: num(t.rows, -1),
      columns: list(t.columns).map(column),
      sql: str(t.sql),
    })
  }
  return {
    tables,
    table: str(b.table),
    columns: list(b.columns).map(column),
    rows: list(b.rows).map((r) => list(r).map(readCell)),
    more: b.more === true,
    note: str(b.note),
  }
}
