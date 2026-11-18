import { describe, expect, it } from 'vitest'

import type { DBListing } from '../../protocol/listings'
import { cellText, schema, stepTable, tableRows } from './sqlite'

const db = (more: Partial<DBListing> = {}): DBListing => ({
  tables: [
    { name: 'a', kind: 'table', rows: 2, columns: [], sql: 'CREATE TABLE a (x)' },
    { name: 'b', kind: 'view', rows: -1, columns: [], sql: 'CREATE VIEW b AS SELECT 1;' },
    { name: 'c', kind: 'table', rows: 0, columns: [], sql: '' },
  ],
  table: 'a',
  columns: [
    { name: 'x', type: 'TEXT' },
    { name: 'y', type: 'BLOB' },
  ],
  rows: [
    [{ kind: 'text', value: 'NULL', more: false }, { kind: 'null' }],
    [{ kind: 'number', value: 3 }, { kind: 'blob', bytes: 2048 }],
  ],
  more: false,
  note: '',
  ...more,
})

describe('cellText', () => {
  it('draws a stand-in apart from text that reads the same', () => {
    expect(cellText({ kind: 'null' })).toEqual({ text: 'NULL', stand: true })
    expect(cellText({ kind: 'text', value: 'NULL', more: false })).toEqual({ text: 'NULL', stand: false })
    expect(cellText({ kind: 'blob', bytes: 2048 }).stand).toBe(true)
    expect(cellText({ kind: 'unknown' }).stand).toBe(true)
  })

  it('says a cut text is cut, and keeps a big integer as its digits', () => {
    expect(cellText({ kind: 'text', value: 'abc', more: true }).text).toBe('abc…')
    expect(cellText({ kind: 'int', value: '9007199254740993' }).text).toBe('9007199254740993')
  })
})

describe('tableRows', () => {
  it('puts the column names first and keys the stand-ins as the body does', () => {
    const { rows, stand } = tableRows(db())
    expect(rows[0]).toEqual(['x', 'y'])
    expect(rows[1]).toEqual(['NULL', 'NULL'])
    expect([...stand].sort()).toEqual(['1:1', '2:1'])
  })
})

describe('schema', () => {
  it('is every statement, each ended, and nothing for a table without one', () => {
    expect(schema(db())).toBe('CREATE TABLE a (x);\n\nCREATE VIEW b AS SELECT 1;')
  })
})

describe('stepTable', () => {
  it('steps along the strip and stops at either end', () => {
    const t = db().tables
    expect(stepTable(t, 'a', 1)).toBe('b')
    expect(stepTable(t, 'b', -1)).toBe('a')
    expect(stepTable(t, 'a', -1)).toBeNull()
    expect(stepTable(t, 'c', 1)).toBeNull()
    expect(stepTable(t, 'gone', 1)).toBeNull()
  })
})
