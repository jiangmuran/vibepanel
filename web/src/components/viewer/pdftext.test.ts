import { describe, expect, it } from 'vitest'

import { findInPages, itemsOf, pageText } from './pdftext'
import { MAX_MATCHES } from './text'

describe('pageText', () => {
  it('joins items, with a newline where a line ends', () => {
    const p = pageText([{ str: 'Fixture ' }, { str: 'PDF', hasEOL: true }, { str: 'page two' }])
    expect(p.text).toBe('Fixture PDF\npage two')
    expect(p.starts).toEqual([0, 8, 12])
  })
})

describe('findInPages', () => {
  const pages = [pageText([{ str: 'Alpha beta', hasEOL: true }]), null, pageText([{ str: 'beta ' }, { str: 'Beta' }])]

  it('finds by page with smart case, skipping pages not read yet', () => {
    expect(findInPages(pages, 'beta').matches).toEqual([
      { page: 0, start: 6, end: 10 },
      { page: 2, start: 0, end: 4 },
      { page: 2, start: 5, end: 9 },
    ])
    expect(findInPages(pages, 'Beta').matches).toEqual([{ page: 2, start: 5, end: 9 }])
    expect(findInPages(pages, '').matches).toEqual([])
  })

  it('never matches across the end of a line', () => {
    const p = [pageText([{ str: 'end', hasEOL: true }, { str: 'start' }])]
    expect(findInPages(p, 'd\ns').matches).toEqual([])
    expect(findInPages(p, 'start').matches).toEqual([{ page: 0, start: 4, end: 9 }])
  })

  it('stops counting at the cap, and says so', () => {
    const p = [pageText([{ str: 'a'.repeat(MAX_MATCHES + 5) }])]
    const r = findInPages(p, 'a')
    expect(r.matches).toHaveLength(MAX_MATCHES)
    expect(r.capped).toBe(true)
  })
})

describe('itemsOf', () => {
  it('maps a match back to the items it covers', () => {
    const { starts } = pageText([{ str: 'Fixture ' }, { str: 'PDF', hasEOL: true }, { str: 'page two' }])
    expect(itemsOf({ page: 0, start: 0, end: 7 }, starts)).toEqual([0, 0])
    expect(itemsOf({ page: 0, start: 6, end: 10 }, starts)).toEqual([0, 1])
    expect(itemsOf({ page: 0, start: 12, end: 16 }, starts)).toEqual([2, 2])
  })
})
