import { MAX_MATCHES } from './text'

/**
 * Find in a PDF, as numbers: the pages' text items joined into one string a
 * page, and a match mapped back to the items the text layer draws, so it can
 * be highlighted where it is on the page.
 *
 * Pure, so the joins and the mapping are tested without pdf.js. The rule is
 * the line body's (text.ts, findMatches): plain text, smart case, no
 * overlaps.
 */

/** What this needs of a pdf.js text item. */
export interface PdfItem {
  str: string
  hasEOL?: boolean
}

/** One page's text, and where each item starts in it. */
export interface PageText {
  text: string
  /** starts[i] is item i's offset in `text`. */
  starts: number[]
}

/**
 * A page's items as one string. An item that ends a line is followed by a
 * newline, and a match never crosses one -- a word at the end of one line and
 * the start of the next are two words, and a highlight across them would be
 * two boxes far apart. Items on the same line are joined as they are: pdf.js
 * already puts the spaces it found into `str`.
 */
export function pageText(items: readonly PdfItem[]): PageText {
  const starts: number[] = []
  let text = ''
  for (const it of items) {
    starts.push(text.length)
    text += it.str
    if (it.hasEOL) text += '\n'
  }
  return { text, starts }
}

export interface PdfMatch {
  /** 0-based page. */
  page: number
  start: number
  end: number
}

/**
 * Every place `query` occurs, page by page, in order; null pages (text not
 * read yet) are skipped, and the caller says the count is not final.
 */
export function findInPages(pages: readonly (PageText | null)[], query: string): { matches: PdfMatch[]; capped: boolean } {
  const matches: PdfMatch[] = []
  if (query === '') return { matches, capped: false }
  const exact = query !== query.toLowerCase()
  const needle = exact ? query : query.toLowerCase()
  for (let page = 0; page < pages.length; page++) {
    const p = pages[page]
    if (!p) continue
    const lower = exact ? p.text : p.text.toLowerCase()
    // As in findMatches: where lowering changed the length, the offsets would
    // point into the wrong place, so search the text as it is.
    const hay = lower.length === p.text.length ? lower : p.text
    let from = 0
    for (;;) {
      const at = hay.indexOf(needle, from)
      if (at < 0) break
      if (hay.slice(at, at + needle.length).includes('\n')) {
        from = at + 1
        continue
      }
      if (matches.length === MAX_MATCHES) return { matches, capped: true }
      matches.push({ page, start: at, end: at + needle.length })
      from = at + needle.length
    }
  }
  return { matches, capped: false }
}

/**
 * The items a match covers, as [first, last] indexes into the page's items:
 * the spans to mark on the text layer.
 */
export function itemsOf(m: PdfMatch, starts: readonly number[]): [number, number] {
  const at = (offset: number) => {
    // The last item that starts at or before the offset.
    let lo = 0
    let hi = starts.length - 1
    while (lo < hi) {
      const mid = (lo + hi + 1) >> 1
      if (starts[mid] <= offset) lo = mid
      else hi = mid - 1
    }
    return lo
  }
  return [at(m.start), at(m.end - 1)]
}
