/**
 * A database listing as the table body draws it: each cell as the text find
 * searches, and whether it is a value or a stand-in for one (NULL, a BLOB's
 * size), which is drawn apart from text that happens to read `NULL`.
 */

import type { DBCell, DBListing, DBTable } from '../../protocol/listings'
import { formatBytes } from '../panels/preview'

export function cellText(c: DBCell): { text: string; stand: boolean } {
  switch (c.kind) {
    case 'null':
      return { text: 'NULL', stand: true }
    case 'number':
      return { text: String(c.value), stand: false }
    case 'int':
    case 'real':
      return { text: c.value, stand: false }
    case 'text':
      return { text: c.more ? `${c.value}…` : c.value, stand: false }
    case 'blob':
      return { text: `BLOB · ${formatBytes(c.bytes)}`, stand: true }
    case 'unknown':
      return { text: '?', stand: true }
  }
}

/**
 * The rows as the table body takes them -- the column names first -- and the
 * cells that are stand-ins, keyed `row:column` as the body keys them.
 */
export function tableRows(db: DBListing): { rows: string[][]; stand: Set<string> } {
  const stand = new Set<string>()
  const rows: string[][] = [db.columns.map((c) => c.name)]
  db.rows.forEach((r, ri) =>
    rows.push(
      r.map((cell, ci) => {
        const { text, stand: s } = cellText(cell)
        if (s) stand.add(`${ri + 1}:${ci}`)
        return text
      }),
    ),
  )
  return { rows, stand }
}

/** Every table's statement, for Source: the schema as the database holds it. */
export function schema(db: DBListing): string {
  return db.tables
    .map((t) => t.sql.trim())
    .filter((s) => s !== '')
    .map((s) => (s.endsWith(';') ? s : `${s};`))
    .join('\n\n')
}

/** The table `step` away from the one shown, or null at either end. */
export function stepTable(tables: readonly DBTable[], name: string, step: 1 | -1): string | null {
  const i = tables.findIndex((t) => t.name === name)
  if (i < 0) return null
  return tables[i + step]?.name ?? null
}
