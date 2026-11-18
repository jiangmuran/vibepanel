import { describe, expect, it } from 'vitest'

import { editLanguage, lineSeparator } from './editlangs'

describe('editLanguage', () => {
  it('names the ten by extension', () => {
    expect(editLanguage('src/app.ts')).toBe('typescript')
    expect(editLanguage('App.TSX')).toBe('tsx')
    expect(editLanguage('a.mjs')).toBe('javascript')
    expect(editLanguage('tool.py')).toBe('python')
    expect(editLanguage('main.go')).toBe('go')
    expect(editLanguage('lib.rs')).toBe('rust')
    expect(editLanguage('config.json')).toBe('json')
    expect(editLanguage('README.md')).toBe('markdown')
    expect(editLanguage('ci.yml')).toBe('yaml')
    expect(editLanguage('page.html')).toBe('html')
    expect(editLanguage('style.css')).toBe('css')
    expect(editLanguage('x.hpp')).toBe('cpp')
  })

  it('is plain text for the rest', () => {
    expect(editLanguage('Makefile')).toBeNull()
    expect(editLanguage('notes.txt')).toBeNull()
    expect(editLanguage('a.constructor')).toBeNull()
    expect(editLanguage('a.__proto__')).toBeNull()
  })
})

describe('lineSeparator', () => {
  it('keeps the file\'s own', () => {
    expect(lineSeparator('a\nb\n')).toBe('\n')
    expect(lineSeparator('a\r\nb\r\n')).toBe('\r\n')
    expect(lineSeparator('one line')).toBe('\n')
    expect(lineSeparator('a\rb\r')).toBe('\r')
  })

  it('is null for a file that mixes them', () => {
    expect(lineSeparator('a\r\nb\n')).toBeNull()
    expect(lineSeparator('a\nb\r')).toBeNull()
  })
})
