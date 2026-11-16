import { describe, expect, it } from 'vitest'

import { isDelimited, parseCsv, sniffDelimiter } from './csv'

describe('parseCsv', () => {
  it('reads quoted fields the way RFC 4180 does', () => {
    const { rows } = parseCsv('a,"b, c","say ""hi"""\n', ',')
    expect(rows).toEqual([['a', 'b, c', 'say "hi"']])
  })

  it('keeps a newline inside quotes in its cell, so rows are not lines', () => {
    const { rows } = parseCsv('id,note\n1,"two\nlines"\n2,x\n', ',')
    expect(rows).toEqual([
      ['id', 'note'],
      ['1', 'two\nlines'],
      ['2', 'x'],
    ])
  })

  it('reads CRLF, and makes no empty row of the last newline', () => {
    expect(parseCsv('a,b\r\nc,d\r\n', ',').rows).toEqual([
      ['a', 'b'],
      ['c', 'd'],
    ])
    expect(parseCsv('a,b', ',').rows).toEqual([['a', 'b']])
  })

  it('keeps empty cells', () => {
    expect(parseCsv('a,,c\n,,\n', ',').rows).toEqual([
      ['a', '', 'c'],
      ['', '', ''],
    ])
  })

  it('is lenient where the format is commonly broken', () => {
    // A quote in the middle of an unquoted field is a character.
    expect(parseCsv('5" pipe,x\n', ',').rows).toEqual([['5" pipe', 'x']])
    // An unclosed quote runs to the end.
    expect(parseCsv('"open,x\ny\n', ',').rows).toEqual([['open,x\ny\n']])
  })

  it('stops at the bound and says there was more', () => {
    const text = Array.from({ length: 25 }, (_, i) => `${i},v`).join('\n') + '\n'
    const { rows, more } = parseCsv(text, ',', 10)
    expect(rows).toHaveLength(10)
    expect(more).toBe(true)
    expect(parseCsv(text, ',', 25).more).toBe(false)
  })
})

describe('sniffDelimiter', () => {
  it('finds what the lines agree on', () => {
    expect(sniffDelimiter('a,b,c\n1,2,3\n')).toBe(',')
    expect(sniffDelimiter('a;b;c\n1,5;2,5;3\n')).toBe(';')
    expect(sniffDelimiter('a\tb\n1\t2\n')).toBe('\t')
    expect(sniffDelimiter('a|b|c\n1|2|3\n')).toBe('|')
  })

  it('does not count a delimiter inside quotes', () => {
    expect(sniffDelimiter('"a;b",c\n"1;2",3\n')).toBe(',')
  })

  it('takes a .tsv at its word', () => {
    expect(sniffDelimiter('a,b\n', 'x.tsv')).toBe('\t')
  })
})

describe('isDelimited', () => {
  it('knows the extensions', () => {
    expect(isDelimited('a.csv')).toBe(true)
    expect(isDelimited('A.TSV')).toBe(true)
    expect(isDelimited('a.txt')).toBe(false)
  })
})
