/**
 * Delimited text as rows of cells: RFC 4180 with the delimiter sniffed.
 *
 * Quoted fields are what a hand-written split gets wrong, and every CSV an
 * agent writes has them: a comma inside quotes is text, `""` inside quotes is
 * one quote, and a quoted field can hold a newline, so rows are not lines.
 */

/** The most rows drawn; the footer says how many there were past it. */
export const MAX_ROWS = 10_000

const CANDIDATES = [',', '\t', ';', '|'] as const
export type Delimiter = (typeof CANDIDATES)[number]

/**
 * The delimiter the first lines agree on.
 *
 * For each candidate, the fields per line across the first lines, counted
 * outside quotes: the one that gives the same count, more than one, on the
 * most lines wins. A `.tsv` is a tab whatever it holds; a `.csv` that is
 * really semicolons -- what a spreadsheet writes where the decimal mark is a
 * comma -- is read as semicolons.
 */
export function sniffDelimiter(text: string, name = ''): Delimiter {
  if (/\.tsv$/i.test(name)) return '\t'
  const lines = text.slice(0, 64 * 1024).split(/\r?\n/).filter((l) => l !== '').slice(0, 20)
  let best: Delimiter = ','
  let bestScore = -1
  for (const d of CANDIDATES) {
    const counts = lines.map((l) => fieldsOutsideQuotes(l, d))
    const first = counts[0] ?? 1
    if (first < 2) continue
    const agree = counts.filter((c) => c === first).length
    const score = agree * 100 + first
    if (score > bestScore) {
      best = d
      bestScore = score
    }
  }
  return best
}

function fieldsOutsideQuotes(line: string, d: string): number {
  let n = 1
  let quoted = false
  for (const c of line) {
    if (c === '"') quoted = !quoted
    else if (c === d && !quoted) n++
  }
  return n
}

/**
 * Rows of cells, at most `max` of them; `more` when the text had others.
 *
 * Lenient where the format is commonly broken: a quote in the middle of an
 * unquoted field is a character, an unclosed quote runs to the end, and a
 * trailing newline does not make an empty last row. Rows are not padded:
 * the table draws what each row has.
 */
export function parseCsv(text: string, d: string, max = MAX_ROWS): { rows: string[][]; more: boolean } {
  const rows: string[][] = []
  let row: string[] = []
  let cell = ''
  let quoted = false
  let atStart = true
  let i = 0
  const endRow = () => {
    row.push(cell)
    rows.push(row)
    row = []
    cell = ''
    atStart = true
  }
  while (i < text.length) {
    const c = text[i]
    if (quoted) {
      if (c === '"') {
        if (text[i + 1] === '"') {
          cell += '"'
          i += 2
          continue
        }
        quoted = false
        i++
        continue
      }
      cell += c
      i++
      continue
    }
    if (c === '"' && atStart) {
      quoted = true
      atStart = false
      i++
      continue
    }
    if (c === d) {
      row.push(cell)
      cell = ''
      atStart = true
      i++
      continue
    }
    if (c === '\r' || c === '\n') {
      endRow()
      if (c === '\r' && text[i + 1] === '\n') i++
      i++
      if (rows.length === max) return { rows, more: i < text.length }
      continue
    }
    cell += c
    atStart = false
    i++
  }
  if (cell !== '' || row.length > 0) endRow()
  return { rows, more: false }
}

/** Whether a file is drawn as a table. */
export function isDelimited(name: string): boolean {
  return /\.(csv|tsv)$/i.test(name)
}
