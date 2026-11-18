import { describe, expect, it } from 'vitest'

import { readArchiveListing, readCell, readDBListing } from './listings'

describe('readArchiveListing', () => {
  it('reads the documented answer', () => {
    const l = readArchiveListing({
      format: 'zip',
      entries: [
        { path: 'dir/b.txt', size: 2, modified: 1700000000, wall: true, isDir: false },
        { path: '../../etc/passwd', size: 1, modified: 0, isDir: false, unsafe: true },
      ],
      truncated: false,
    })
    expect(l).toEqual({
      format: 'zip',
      entries: [
        { path: 'dir/b.txt', size: 2, modified: 1700000000, wall: true, isDir: false, unsafe: false },
        { path: '../../etc/passwd', size: 1, modified: 0, wall: false, isDir: false, unsafe: true },
      ],
      truncated: false,
      note: '',
    })
  })

  it('is null for a format it does not know, and for no entries list', () => {
    expect(readArchiveListing({ format: '7z', entries: [] })).toBeNull()
    expect(readArchiveListing({ format: 'zip' })).toBeNull()
    expect(readArchiveListing(null)).toBeNull()
    expect(readArchiveListing([])).toBeNull()
  })

  it('drops an entry with no path and reads a wrong field as not known', () => {
    const l = readArchiveListing({
      format: 'tar',
      entries: [{ size: 3 }, { path: 'a', size: 'big', modified: null, isDir: 'yes', unsafe: 1 }],
      truncated: 'true',
      note: 7,
    })
    expect(l!.entries).toEqual([{ path: 'a', size: -1, modified: 0, wall: false, isDir: false, unsafe: false }])
    expect(l!.truncated).toBe(false)
    expect(l!.note).toBe('')
  })
})

describe('readCell', () => {
  it('reads each shape the documentation lists', () => {
    expect(readCell(null)).toEqual({ kind: 'null' })
    expect(readCell(1.5)).toEqual({ kind: 'number', value: 1.5 })
    expect(readCell('x')).toEqual({ kind: 'text', value: 'x', more: false })
    expect(readCell({ int: '9007199254740993' })).toEqual({ kind: 'int', value: '9007199254740993' })
    expect(readCell({ real: '+Inf' })).toEqual({ kind: 'real', value: '+Inf' })
    expect(readCell({ blob: 4096 })).toEqual({ kind: 'blob', bytes: 4096 })
    expect(readCell({ text: 'long', more: true })).toEqual({ kind: 'text', value: 'long', more: true })
  })

  it('is unknown for anything else, and never carries it on', () => {
    for (const v of [true, [1], {}, { blob: '4' }, { int: 5 }, undefined]) expect(readCell(v), JSON.stringify(v)).toEqual({ kind: 'unknown' })
  })
})

describe('readDBListing', () => {
  it('reads the documented answer', () => {
    const l = readDBListing({
      tables: [
        { name: 'people', kind: 'table', rows: 250, columns: [{ name: 'id', type: 'INTEGER' }], sql: 'CREATE TABLE people (id INTEGER)' },
        { name: 'adults', kind: 'view', rows: -1, columns: [], sql: 'CREATE VIEW adults AS SELECT 1' },
      ],
      table: 'people',
      columns: [{ name: 'id', type: 'INTEGER' }],
      rows: [[1], [null]],
      more: true,
    })
    expect(l!.tables.map((t) => [t.name, t.kind, t.rows])).toEqual([
      ['people', 'table', 250],
      ['adults', 'view', -1],
    ])
    expect(l!.rows).toEqual([[{ kind: 'number', value: 1 }], [{ kind: 'null' }]])
    expect(l!.more).toBe(true)
  })

  it('is null without a tables list, and drops a table with no name', () => {
    expect(readDBListing({ table: 'x' })).toBeNull()
    expect(readDBListing({ tables: [{ rows: 1 }, { name: 't', kind: 'virtual' }] })!.tables).toEqual([
      { name: 't', kind: 'table', rows: -1, columns: [], sql: '' },
    ])
  })
})
