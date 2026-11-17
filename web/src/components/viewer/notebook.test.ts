import { describe, expect, it } from 'vitest'

import { isNotebook, parseNotebook } from './notebook'

const nb = (cells: unknown[], metadata: unknown = { kernelspec: { language: 'python' } }) =>
  JSON.stringify({ nbformat: 4, nbformat_minor: 5, metadata, cells })

describe('parseNotebook', () => {
  it('reads cells in order, with sources as lists of lines', () => {
    const got = parseNotebook(
      nb([
        { cell_type: 'markdown', source: ['# Title\n', 'text'] },
        { cell_type: 'code', execution_count: 3, source: 'x = 1', outputs: [] },
        { cell_type: 'raw', source: 'raw' },
        { cell_type: 'code', execution_count: null, source: ['y'], outputs: [] },
      ]),
    )
    expect(got?.language).toBe('python')
    expect(got?.cells.map((c) => [c.type, c.source, c.count])).toEqual([
      ['markdown', '# Title\ntext', null],
      ['code', 'x = 1', 3],
      ['raw', 'raw', null],
      ['code', 'y', null],
    ])
  })

  it('takes the language from language_info when the kernelspec has none', () => {
    expect(parseNotebook(nb([], { language_info: { name: 'julia' } }))?.language).toBe('julia')
    expect(parseNotebook(nb([], {}))?.language).toBe('')
  })

  it('draws each output as what it is', () => {
    const got = parseNotebook(
      nb([
        {
          cell_type: 'code',
          execution_count: 1,
          source: '',
          outputs: [
            { output_type: 'stream', name: 'stdout', text: ['a\n', 'b\n'] },
            { output_type: 'stream', name: 'stderr', text: 'warn\n' },
            { output_type: 'execute_result', data: { 'text/plain': ['   a\n0  1'], 'text/html': '<table>' } },
            { output_type: 'display_data', data: { 'image/png': 'iVBORw0KGgo=\n', 'text/plain': '<Figure>' } },
            { output_type: 'display_data', data: { 'text/html': '<b>x</b><script>alert(1)</script>' } },
            { output_type: 'display_data', data: { 'image/svg+xml': ['<svg>', '</svg>'] } },
            {
              output_type: 'error',
              ename: 'ZeroDivisionError',
              evalue: 'division by zero',
              traceback: ['\u001b[0;31mZeroDivisionError\u001b[0m', 'line 1'],
            },
          ],
        },
      ]),
    )
    expect(got?.cells[0].outputs).toEqual([
      { kind: 'text', text: 'a\nb\n', stderr: false },
      { kind: 'text', text: 'warn\n', stderr: true },
      // Plain before HTML.
      { kind: 'text', text: '   a\n0  1', stderr: false },
      // A picture before its text, base64 without the newline nbformat adds.
      { kind: 'image', src: 'data:image/png;base64,iVBORw0KGgo=' },
      // HTML alone is its source, to be drawn as text.
      { kind: 'html', text: '<b>x</b><script>alert(1)</script>' },
      { kind: 'image', src: 'data:image/svg+xml;charset=utf-8,%3Csvg%3E%3C%2Fsvg%3E' },
      // The terminal's colours are taken out.
      { kind: 'error', name: 'ZeroDivisionError', value: 'division by zero', traceback: 'ZeroDivisionError\nline 1' },
    ])
  })

  it('makes no data URL of a type it did not choose, or of what is not base64', () => {
    const got = parseNotebook(
      nb([
        {
          cell_type: 'code',
          source: '',
          outputs: [
            { output_type: 'display_data', data: { 'text/html+fake': 'x', 'application/javascript': 'alert(1)' } },
            { output_type: 'display_data', data: { 'image/png': '"><script>', 'text/plain': 'fallback' } },
          ],
        },
      ]),
    )
    expect(got?.cells[0].outputs).toEqual([{ kind: 'text', text: 'fallback', stderr: false }])
  })

  it('is null for what is not an nbformat 4 notebook', () => {
    expect(parseNotebook('{')).toBeNull()
    expect(parseNotebook('[]')).toBeNull()
    expect(parseNotebook(JSON.stringify({ nbformat: 3, worksheets: [] }))).toBeNull()
    expect(parseNotebook(JSON.stringify({ nbformat: 4 }))).toBeNull()
  })

  it('skips a cell of a type it does not know, and outputs it does not', () => {
    const got = parseNotebook(nb([{ cell_type: 'heading', source: 'x' }, { cell_type: 'code', source: 'y', outputs: [{ output_type: 'nope' }, null] }]))
    expect(got?.cells).toHaveLength(1)
    expect(got?.cells[0].outputs).toEqual([])
  })
})

describe('isNotebook', () => {
  it('goes by the extension', () => {
    expect(isNotebook('a/b.ipynb')).toBe(true)
    expect(isNotebook('B.IPYNB')).toBe(true)
    expect(isNotebook('b.json')).toBe(false)
  })
})
