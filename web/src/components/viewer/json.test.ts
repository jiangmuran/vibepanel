import { describe, expect, it } from 'vitest'

import {
  ARRAY_FOLD,
  ancestors,
  childPath,
  initialExpanded,
  isJson,
  parseJson,
  revealing,
  treeMatches,
  visibleRows,
} from './json'

describe('parseJson', () => {
  it('reads a document, and JSON Lines as a list of them', () => {
    expect(parseJson('{"a":1}', 'x.json', false)).toEqual({ value: { a: 1 } })
    expect(parseJson('{"t":1}\n\n{"t":2}\n', 'x.jsonl', false)).toEqual({ value: [{ t: 1 }, { t: 2 }] })
  })

  it('is nothing for what does not parse, or was cut', () => {
    expect(parseJson('{"a":', 'x.json', false)).toBeNull()
    expect(parseJson('{"a":1}', 'x.json', true)).toBeNull()
    expect(parseJson('{"t":1}\nnot json\n', 'x.jsonl', false)).toBeNull()
  })

  it('knows the extensions', () => {
    for (const n of ['a.json', 'a.jsonl', 'a.ndjson', 'm.webmanifest']) expect(isJson(n), n).toBe(true)
    expect(isJson('a.json5')).toBe(false)
  })
})

describe('pointers', () => {
  it('escape what a key can hold', () => {
    expect(childPath('', 'a/b')).toBe('/a~1b')
    expect(childPath('', 'a~b')).toBe('/a~0b')
    expect(childPath('/x', 3)).toBe('/x/3')
  })

  it('name their ancestors', () => {
    expect(ancestors('/a/0/b')).toEqual(['', '/a', '/a/0'])
    expect(ancestors('')).toEqual([''])
  })
})

describe('visibleRows', () => {
  const doc = { name: 'x', list: [1, 2], deep: { inner: { v: true } } }

  it('opens a small document fully', () => {
    const rows = visibleRows(doc, initialExpanded(doc))
    expect(rows.map((r) => r.path)).toEqual(['', '/name', '/list', '/list/0', '/list/1', '/deep', '/deep/inner', '/deep/inner/v'])
    expect(rows.find((r) => r.path === '/deep/inner/v')?.depth).toBe(3)
  })

  it('opens a large one only at its root', () => {
    const big = { items: Array.from({ length: 1000 }, (_, i) => ({ i })) }
    const rows = visibleRows(big, initialExpanded(big))
    expect(rows.map((r) => r.path)).toEqual(['', '/items'])
  })

  it('shows a collapsed container as one row with its size', () => {
    const rows = visibleRows(doc, new Set(['']))
    expect(rows.find((r) => r.path === '/list')).toMatchObject({ expanded: false, size: 2 })
    expect(rows.some((r) => r.path === '/list/0')).toBe(false)
  })

  it('folds a long array past its first two hundred, until the fold is opened', () => {
    const arr = { a: Array.from({ length: ARRAY_FOLD + 50 }, (_, i) => i) }
    const rows = visibleRows(arr, new Set(['', '/a']))
    expect(rows.filter((r) => r.depth === 2)).toHaveLength(ARRAY_FOLD + 1)
    expect(rows[rows.length - 1]).toMatchObject({ path: '/a#all', folded: 50 })
    const all = visibleRows(arr, new Set(['', '/a', '/a#all']))
    expect(all.filter((r) => r.depth === 2)).toHaveLength(ARRAY_FOLD + 50)
  })
})

describe('treeMatches', () => {
  const doc = { Name: 'Alpha', tags: ['beta', 'gamma'], n: 42, ok: null }

  it('finds keys and values, in document order, smart case', () => {
    expect(treeMatches(doc, 'a').paths).toEqual(['/Name', '/tags', '/tags/0', '/tags/1'])
    expect(treeMatches(doc, 'Alpha').paths).toEqual(['/Name'])
    expect(treeMatches(doc, 'ALPHA').paths).toEqual([])
    expect(treeMatches(doc, '42').paths).toEqual(['/n'])
    expect(treeMatches(doc, 'null').paths).toEqual(['/ok'])
    // An index is not text: '1' is not the second tag.
    expect(treeMatches(doc, '1').paths).toEqual([])
  })

  it('finds nothing for nothing', () => {
    expect(treeMatches(doc, '').paths).toEqual([])
  })
})

describe('revealing', () => {
  it('opens the ancestors, and the fold that hides a late index', () => {
    expect(revealing('/a/0/b')).toEqual(['', '/a', '/a/0'])
    expect(revealing(`/a/${ARRAY_FOLD + 3}`)).toEqual(['', '/a', '/a#all'])
  })
})
