/**
 * A Jupyter notebook, read into what the viewer draws: cells in order, each
 * code cell with its execution count and outputs. Pure, so what is drawn of
 * each kind of output -- and what is never drawn -- is tested.
 *
 * nbformat 4, which is every notebook written since 2015. Anything else, or
 * anything that is not a notebook when it is parsed, is null, and the file is
 * the JSON it is.
 */

export type Output =
  | { kind: 'text'; text: string; stderr: boolean }
  /** A picture, as a data URL whose type this side chose. */
  | { kind: 'image'; src: string }
  /** HTML, as its source: drawn as text and never rendered. */
  | { kind: 'html'; text: string }
  | { kind: 'error'; name: string; value: string; traceback: string }

export interface Cell {
  type: 'markdown' | 'code' | 'raw'
  source: string
  /** The execution count, for a code cell that has run. */
  count: number | null
  outputs: Output[]
}

export interface Notebook {
  /** The kernel's language, as it names it. */
  language: string
  cells: Cell[]
}

export function isNotebook(name: string): boolean {
  return /\.ipynb$/i.test(name)
}

/** `source` and `text` are a string or a list of the lines of one. */
function joined(v: unknown): string {
  if (typeof v === 'string') return v
  if (Array.isArray(v)) return v.filter((x) => typeof x === 'string').join('')
  return ''
}

const obj = (v: unknown): Record<string, unknown> =>
  v !== null && typeof v === 'object' && !Array.isArray(v) ? (v as Record<string, unknown>) : {}

/**
 * Terminal colour codes: a traceback carries them for the console it was made
 * for, and as text they are noise in every line.
 */
// eslint-disable-next-line no-control-regex
const ANSI = /\x1b\[[0-9;?]*[ -/]*[@-~]/g

/**
 * The pictures an output may be drawn as, by the type it says it is. A data
 * URL of a type outside this list is not made, so nothing a notebook holds
 * becomes a document in an <img>: SVG is in, because an <img> runs nothing.
 */
const PICTURES = ['image/png', 'image/jpeg', 'image/gif', 'image/webp', 'image/svg+xml'] as const

/** Base64 and nothing else, as nbformat stores a raster picture. */
const BASE64 = /^[A-Za-z0-9+/\s]*={0,2}\s*$/

function output(o: Record<string, unknown>): Output | null {
  switch (o.output_type) {
    case 'stream':
      return { kind: 'text', text: joined(o.text), stderr: o.name === 'stderr' }
    case 'error':
      return {
        kind: 'error',
        name: typeof o.ename === 'string' ? o.ename : '',
        value: typeof o.evalue === 'string' ? o.evalue : '',
        traceback: (Array.isArray(o.traceback) ? o.traceback.filter((x) => typeof x === 'string').join('\n') : '').replace(ANSI, ''),
      }
    case 'execute_result':
    case 'display_data': {
      const data = obj(o.data)
      for (const type of PICTURES) {
        if (!(type in data)) continue
        const body = joined(data[type])
        if (type === 'image/svg+xml') return { kind: 'image', src: `data:image/svg+xml;charset=utf-8,${encodeURIComponent(body)}` }
        if (BASE64.test(body)) return { kind: 'image', src: `data:${type};base64,${body.replace(/\s/g, '')}` }
      }
      // Plain text before HTML: a table from pandas carries both, and the
      // plain one is the one written to be read as text.
      if ('text/plain' in data) return { kind: 'text', text: joined(data['text/plain']), stderr: false }
      if ('text/html' in data) return { kind: 'html', text: joined(data['text/html']) }
      if ('text/markdown' in data) return { kind: 'text', text: joined(data['text/markdown']), stderr: false }
      return null
    }
  }
  return null
}

export function parseNotebook(text: string): Notebook | null {
  let doc: unknown
  try {
    doc = JSON.parse(text)
  } catch {
    return null
  }
  const nb = obj(doc)
  if (nb.nbformat !== 4 || !Array.isArray(nb.cells)) return null
  const meta = obj(nb.metadata)
  const language =
    (typeof obj(meta.kernelspec).language === 'string' && (obj(meta.kernelspec).language as string)) ||
    (typeof obj(meta.language_info).name === 'string' && (obj(meta.language_info).name as string)) ||
    ''
  const cells: Cell[] = []
  for (const raw of nb.cells) {
    const c = obj(raw)
    const type = c.cell_type === 'markdown' || c.cell_type === 'code' || c.cell_type === 'raw' ? c.cell_type : null
    if (!type) continue
    cells.push({
      type,
      source: joined(c.source),
      count: type === 'code' && typeof c.execution_count === 'number' ? c.execution_count : null,
      outputs:
        type === 'code' && Array.isArray(c.outputs)
          ? c.outputs.map((o) => output(obj(o))).filter((o): o is Output => o !== null)
          : [],
    })
  }
  return { language, cells }
}
