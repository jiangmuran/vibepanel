/**
 * Release notes, from a GitHub release body, as blocks the page can draw.
 *
 * The body is the annotated tag's message -- `.github/workflows/release.yml`
 * publishes with `--notes-from-tag` -- so it is prose in paragraphs, hard
 * wrapped at eighty columns, with the occasional heading or list. It was
 * shown in a `<pre>`, which kept every hard line break: on a phone each
 * eighty-column line wrapped once more, so a paragraph read as alternating
 * long and short lines, and a heading was a line with two hashes in front.
 *
 * This is not a Markdown renderer and must not grow into one. It recognises
 * the four shapes a tag message here uses -- headings, bullet lists, fenced
 * code and paragraphs -- and returns them as data. Nothing in it produces
 * HTML: the component draws the blocks as elements, every string goes through
 * safeText, and the inline runs (bold, code, a link's text) are the only
 * decoration. A link keeps its text and drops its address; the release page
 * itself is linked beside the notes, and that is the one address on this
 * page that the panel vouches for.
 */

export type NotesBlock =
  | { kind: 'heading'; level: 1 | 2 | 3; runs: Run[] }
  | { kind: 'paragraph'; runs: Run[] }
  | { kind: 'list'; items: Run[][] }
  | { kind: 'code'; text: string }

export type Run = { kind: 'text' | 'bold' | 'code'; text: string }

const HEADING = /^(#{1,6})\s+(.*?)\s*#*\s*$/
const BULLET = /^\s*[-*+]\s+(.*)$/
const FENCE = /^\s*```/

export function parseReleaseNotes(body: string): NotesBlock[] {
  const out: NotesBlock[] = []
  const lines = body.replace(/\r\n?/g, '\n').split('\n')
  let para: string[] = []
  let items: string[] = []
  const flushPara = () => {
    if (para.length === 0) return
    // Hard wraps are joined with a space; the paragraph is one run of text
    // and the browser breaks it where the column is.
    out.push({ kind: 'paragraph', runs: inline(para.join(' ')) })
    para = []
  }
  const flushList = () => {
    if (items.length === 0) return
    out.push({ kind: 'list', items: items.map(inline) })
    items = []
  }
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]
    if (FENCE.test(line)) {
      flushPara()
      flushList()
      const code: string[] = []
      for (i++; i < lines.length && !FENCE.test(lines[i]); i++) code.push(lines[i])
      out.push({ kind: 'code', text: code.join('\n') })
      continue
    }
    const h = HEADING.exec(line)
    if (h) {
      flushPara()
      flushList()
      // Levels beyond three all draw as three: a release note is not a book,
      // and a page that has to size six headings is a page with none of them
      // legible.
      const level = Math.min(3, h[1].length) as 1 | 2 | 3
      out.push({ kind: 'heading', level, runs: inline(h[2]) })
      continue
    }
    const b = BULLET.exec(line)
    if (b) {
      flushPara()
      items.push(b[1])
      continue
    }
    if (line.trim() === '') {
      flushPara()
      flushList()
      continue
    }
    if (items.length > 0 && /^\s{2,}/.test(line)) {
      // A wrapped bullet: continuation lines are indented under the text.
      items[items.length - 1] += ' ' + line.trim()
      continue
    }
    flushList()
    para.push(line.trim())
  }
  flushPara()
  flushList()
  return out
}

/**
 * Inline runs: `**bold**`, `` `code` ``, and `[text](url)` reduced to text.
 * Anything else is text, including a lone asterisk or backtick.
 */
export function inline(text: string): Run[] {
  const runs: Run[] = []
  // Links first, because a link's text may itself be bold or code.
  const unlinked = text.replace(/\[([^\]]+)\]\([^)]*\)/g, '$1')
  const re = /(\*\*([^*]+)\*\*)|(`([^`]+)`)/g
  let last = 0
  for (let m = re.exec(unlinked); m; m = re.exec(unlinked)) {
    if (m.index > last) runs.push({ kind: 'text', text: unlinked.slice(last, m.index) })
    if (m[2] !== undefined) runs.push({ kind: 'bold', text: m[2] })
    else if (m[4] !== undefined) runs.push({ kind: 'code', text: m[4] })
    last = m.index + m[0].length
  }
  if (last < unlinked.length) runs.push({ kind: 'text', text: unlinked.slice(last) })
  return runs
}

/** Everything the notes say, as one string, for a test or a title. */
export function plainText(blocks: NotesBlock[]): string {
  return blocks
    .map((b) => {
      switch (b.kind) {
        case 'code':
          return b.text
        case 'list':
          return b.items.map((runs) => runs.map((r) => r.text).join('')).join('\n')
        default:
          return b.runs.map((r) => r.text).join('')
      }
    })
    .join('\n')
}
