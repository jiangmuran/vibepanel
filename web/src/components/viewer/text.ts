/**
 * The viewer's text, as data: lines, blocks, matches, line endings.
 *
 * Nothing here touches the DOM, so every rule the body draws by is tested in
 * node (vitest here runs there on purpose). docs/file-preview.md, "Size", is
 * the design: every line is in the DOM, in blocks the browser skips while they
 * are off screen, and find searches this text rather than the DOM -- the DOM
 * of a block nobody has scrolled to has not been laid out, and a search that
 * read it would miss what it has not drawn.
 */

/**
 * Lines per block.
 *
 * A block is the unit `content-visibility: auto` skips and the unit
 * highlighting tokenises on first sight. Large enough that 50,000 lines is a
 * couple of hundred elements for the browser to consider, small enough that
 * the block the reader lands on is laid out in one frame.
 */
export const BLOCK_LINES = 256

/**
 * The lines of a text, without their terminators.
 *
 * A trailing newline ends the last line rather than starting another, the way
 * `wc -l` and every editor count it, and the count `countLines` already puts
 * under a preview. A CR before the LF is part of the terminator: drawn, it is
 * an invisible character at the end of every line of a Windows file, which a
 * copy then carries into whatever it is pasted into. A lone CR is a terminator
 * too (classic Mac), because otherwise a whole file is one line.
 */
export function splitLines(text: string): string[] {
  if (text === '') return []
  const lines = text.split(/\r\n|\n|\r/)
  if (lines[lines.length - 1] === '') lines.pop()
  return lines
}

export type LineEndings = 'LF' | 'CRLF' | 'CR' | 'mixed' | 'none'

/**
 * Which line terminator a text uses, for the footer.
 *
 * `mixed` is said rather than averaged: a file whose endings disagree is a
 * file a tool is about to rewrite one way or the other, and that is worth
 * knowing before it does.
 */
export function lineEndings(text: string): LineEndings {
  let lf = 0
  let crlf = 0
  let cr = 0
  for (let i = 0; i < text.length; i++) {
    const c = text.charCodeAt(i)
    if (c === 13) {
      if (text.charCodeAt(i + 1) === 10) {
        crlf++
        i++
      } else {
        cr++
      }
    } else if (c === 10) {
      lf++
    }
  }
  const kinds = (lf > 0 ? 1 : 0) + (crlf > 0 ? 1 : 0) + (cr > 0 ? 1 : 0)
  if (kinds === 0) return 'none'
  if (kinds > 1) return 'mixed'
  return lf > 0 ? 'LF' : crlf > 0 ? 'CRLF' : 'CR'
}

export interface Block {
  /** First line, 0-based. */
  start: number
  /** One past the last line. */
  end: number
}

/** The blocks a text of `count` lines is drawn in. */
export function blocks(count: number, size = BLOCK_LINES): Block[] {
  const out: Block[] = []
  for (let start = 0; start < count; start += size) {
    out.push({ start, end: Math.min(count, start + size) })
  }
  return out
}

/** The block a 0-based line is in. */
export function blockOf(line: number, size = BLOCK_LINES): number {
  return Math.floor(line / size)
}

export interface Match {
  /** 0-based line. */
  line: number
  /** UTF-16 offsets into the line, end exclusive. */
  start: number
  end: number
}

/**
 * The most matches find will count.
 *
 * One letter typed into the find box on a 2 MB log is a million matches, and
 * an array of a million objects, highlighted, is the freeze the bar exists to
 * avoid. Past this the count says "10,000+" and stepping still works through
 * the ones found.
 */
export const MAX_MATCHES = 10_000

/**
 * Every place `query` occurs, line by line, in order.
 *
 * Case-insensitive unless the query has a capital in it -- the rule editors
 * call smart case, which is the one nobody has to be told about: typing
 * `error` finds `Error`, typing `Error` means it. Plain text, never a regular
 * expression: a file's contents decide nothing about what the query means.
 * Matches do not overlap, and none cross a line, because a highlight is drawn
 * per line.
 */
export function findMatches(lines: readonly string[], query: string): { matches: Match[]; capped: boolean } {
  const matches: Match[] = []
  if (query === '') return { matches, capped: false }
  const exact = query !== query.toLowerCase()
  const needle = exact ? query : query.toLowerCase()
  for (let line = 0; line < lines.length; line++) {
    const hay = exact ? lines[line] : lines[line].toLowerCase()
    // toLowerCase can change a string's length (İ is one unit, i̇ is two), and
    // the offsets are into the line as drawn. Where it did, search the line
    // as it is rather than report offsets that point into the wrong place.
    const text = hay.length === lines[line].length ? hay : lines[line]
    let from = 0
    for (;;) {
      const at = text.indexOf(needle, from)
      if (at < 0) break
      if (matches.length === MAX_MATCHES) return { matches, capped: true }
      matches.push({ line, start: at, end: at + needle.length })
      from = at + needle.length
    }
  }
  return { matches, capped: false }
}

/**
 * The match to show after `current` in direction `dir`, wrapping at both ends.
 * `current` -1 means none yet: forward starts at the first, back at the last.
 */
export function stepMatch(current: number, count: number, dir: 1 | -1): number {
  if (count === 0) return -1
  if (current < 0) return dir === 1 ? 0 : count - 1
  return (current + dir + count) % count
}

/**
 * The first match at or after a line, so a find typed while reading starts
 * where the reader is rather than at the top of the file.
 */
export function firstMatchFrom(matches: readonly Match[], line: number): number {
  if (matches.length === 0) return -1
  let lo = 0
  let hi = matches.length
  while (lo < hi) {
    const mid = (lo + hi) >> 1
    if (matches[mid].line < line) lo = mid + 1
    else hi = mid
  }
  return lo === matches.length ? 0 : lo
}

/**
 * The line a go-to-line entry means, 0-based, or null for one that means
 * nothing.
 *
 * 1-based on the way in, because that is what the gutter shows and what a
 * compiler error says. `path:line` and `line:col` are accepted and the line
 * taken, because the thing pasted in is usually one of those. Past the end is
 * the last line rather than nothing: "go to 9999" in a 400-line file means
 * "the bottom".
 */
export function parseGoTo(input: string, lineCount: number): number | null {
  if (lineCount === 0) return null
  const m = /(?:^|:)\s*(\d+)\s*(?::\s*\d+\s*)?$/.exec(input.trim())
  if (!m) return null
  const n = Number(m[1])
  if (!Number.isSafeInteger(n) || n < 1) return null
  return Math.min(n, lineCount) - 1
}

/**
 * Characters of one line drawn before it is cut.
 *
 * A minified bundle is one line, and two megabytes of one line is a paragraph
 * the browser takes more than ten seconds to lay out -- the page frozen while
 * it does, measured on a 9 MB file the viewer opened as text. Past this the
 * line shows its start and a button for the rest; find still searches all of
 * it, and stepping to a match past the cut opens the line.
 */
export const LONG_LINE = 10_000

/** The lines as drawn before any is opened: each long one cut at LONG_LINE. */
export function cutLines(lines: readonly string[]): readonly string[] {
  return lines.some((l) => l.length > LONG_LINE) ? lines.map((l) => (l.length > LONG_LINE ? l.slice(0, LONG_LINE) : l)) : lines
}
