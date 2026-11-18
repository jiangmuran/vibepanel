import { describe, expect, it } from 'vitest'

import { viewerAction, type KeyContext, type KeyLike } from './keys'

const key = (k: string, mods: Partial<KeyLike> = {}): KeyLike => ({
  key: k,
  ctrlKey: false,
  metaKey: false,
  altKey: false,
  shiftKey: false,
  ...mods,
})
const body: KeyContext = { inField: false, findOpen: false, mac: false }
const finding: KeyContext = { inField: true, findOpen: true, mac: false }

describe('viewerAction', () => {
  it('maps the table in docs/file-preview.md', () => {
    const table: Record<string, string> = {
      '[': 'prevFile',
      ']': 'nextFile',
      w: 'toggleWrap',
      n: 'toggleLineNumbers',
      s: 'toggleSource',
      '+': 'zoomIn',
      '=': 'zoomIn',
      '-': 'zoomOut',
      '0': 'zoomReset',
      d: 'download',
      c: 'copyPath',
      Escape: 'close',
    }
    for (const [k, want] of Object.entries(table)) expect(viewerAction(key(k), body), k).toBe(want)
  })

  it('uses the platform command key for find and go to line', () => {
    expect(viewerAction(key('f', { ctrlKey: true }), body)).toBe('find')
    expect(viewerAction(key('g', { ctrlKey: true }), body)).toBe('goToLine')
    expect(viewerAction(key('f', { metaKey: true }), { ...body, mac: true })).toBe('find')
    // The other platform's key is not this one's: Ctrl+F on a Mac is a
    // cursor movement in a text field.
    expect(viewerAction(key('f', { ctrlKey: true }), { ...body, mac: true })).toBeNull()
    expect(viewerAction(key('f', { metaKey: true }), body)).toBeNull()
  })

  it('closes find before it closes the viewer', () => {
    expect(viewerAction(key('Escape'), finding)).toBe('closeFind')
    expect(viewerAction(key('Escape'), { ...body, findOpen: true })).toBe('closeFind')
    expect(viewerAction(key('Escape'), body)).toBe('close')
  })

  it('steps matches with Enter while find is open, anywhere', () => {
    expect(viewerAction(key('Enter'), finding)).toBe('nextMatch')
    expect(viewerAction(key('Enter', { shiftKey: true }), finding)).toBe('prevMatch')
    expect(viewerAction(key('Enter'), { ...body, findOpen: true })).toBe('nextMatch')
    // Without find, Enter is the focused button's.
    expect(viewerAction(key('Enter'), body)).toBeNull()
  })

  it('leaves letters to a field', () => {
    for (const k of ['[', 'w', 'n', 's', 'd', 'c', '0', '-']) {
      expect(viewerAction(key(k), finding), k).toBeNull()
    }
    // The command-key shortcuts still work from inside one.
    expect(viewerAction(key('g', { ctrlKey: true }), finding)).toBe('goToLine')
  })

  it('leaves a modified letter to whoever owns it', () => {
    // Ctrl+C copies the selection; ⌥C on a Mac types ç.
    expect(viewerAction(key('c', { ctrlKey: true }), body)).toBeNull()
    expect(viewerAction(key('c', { metaKey: true }), { ...body, mac: true })).toBeNull()
    expect(viewerAction(key('c', { altKey: true }), body)).toBeNull()
    expect(viewerAction(key('f', { ctrlKey: true, altKey: true }), body)).toBeNull()
  })

  it('takes nothing from an IME composition', () => {
    expect(viewerAction(key('Escape', { isComposing: true }), finding)).toBeNull()
    expect(viewerAction(key('Enter', { isComposing: true }), finding)).toBeNull()
    // Safari reports the composition only as keyCode 229.
    expect(viewerAction(key('Enter', { keyCode: 229 }), finding)).toBeNull()
  })

  it('pages through a PDF, and leaves PgUp and PgDn to the browser elsewhere', () => {
    const pdf: KeyContext = { ...body, pages: true }
    expect(viewerAction(key('PageUp'), pdf)).toBe('prevPage')
    expect(viewerAction(key('PageDown'), pdf)).toBe('nextPage')
    expect(viewerAction(key('g'), pdf)).toBe('firstPage')
    expect(viewerAction(key('G', { shiftKey: true }), pdf)).toBe('lastPage')
    for (const k of ['PageUp', 'PageDown', 'g', 'G']) expect(viewerAction(key(k), body), k).toBeNull()
    // Typing a g into the find box is typing.
    expect(viewerAction(key('g'), { ...finding, pages: true })).toBeNull()
  })

  it('steps through the tables of a database with the arrows, and leaves them alone elsewhere', () => {
    const db: KeyContext = { ...body, tables: true }
    expect(viewerAction(key('ArrowLeft'), db)).toBe('prevTable')
    expect(viewerAction(key('ArrowRight'), db)).toBe('nextTable')
    for (const k of ['ArrowLeft', 'ArrowRight']) {
      expect(viewerAction(key(k), body), k).toBeNull()
      // Shift with an arrow extends a selection; in the find box it moves the caret.
      expect(viewerAction(key(k, { shiftKey: true }), db), k).toBeNull()
      expect(viewerAction(key(k), { ...finding, tables: true }), k).toBeNull()
      expect(viewerAction(key(k, { altKey: true }), db), k).toBeNull()
    }
  })
})
