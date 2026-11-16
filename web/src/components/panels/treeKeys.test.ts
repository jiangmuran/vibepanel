import { describe, expect, it } from 'vitest'

import { filterEntries, moveRow, treeAction, type TreeKeyLike } from './treeKeys'

const key = (k: string, mods: Partial<TreeKeyLike> = {}): TreeKeyLike => ({
  key: k,
  ctrlKey: false,
  metaKey: false,
  altKey: false,
  ...mods,
})

describe('treeAction', () => {
  it('maps the table in docs/file-preview.md', () => {
    expect(treeAction(key('ArrowDown'), false)).toEqual({ t: 'down' })
    expect(treeAction(key('ArrowUp'), false)).toEqual({ t: 'up' })
    expect(treeAction(key('Home'), false)).toEqual({ t: 'first' })
    expect(treeAction(key('End'), false)).toEqual({ t: 'last' })
    expect(treeAction(key('ArrowLeft'), false)).toEqual({ t: 'parent' })
    expect(treeAction(key('Backspace'), false)).toEqual({ t: 'parent' })
    expect(treeAction(key('r'), false)).toEqual({ t: 'type', char: 'r' })
  })

  it('edits the filter while one is being typed', () => {
    expect(treeAction(key('Backspace'), true)).toEqual({ t: 'backspaceFilter' })
    expect(treeAction(key('Escape'), true)).toEqual({ t: 'clearFilter' })
    expect(treeAction(key(' '), true)).toEqual({ t: 'type', char: ' ' })
  })

  it('leaves Escape alone with no filter, and a first space', () => {
    // Escape closes whatever the panel is in; a leading space finds nothing.
    expect(treeAction(key('Escape'), false)).toBeNull()
    expect(treeAction(key(' '), false)).toBeNull()
  })

  it('leaves Enter to the row, which is a button', () => {
    expect(treeAction(key('Enter'), false)).toBeNull()
  })

  it('leaves a modified key to whoever owns it', () => {
    for (const m of [{ ctrlKey: true }, { metaKey: true }, { altKey: true }]) {
      expect(treeAction(key('r', m), false)).toBeNull()
      expect(treeAction(key('ArrowDown', m), false)).toBeNull()
    }
  })

  it('takes nothing from an IME composition', () => {
    expect(treeAction(key('a', { isComposing: true }), false)).toBeNull()
    expect(treeAction(key('a', { keyCode: 229 }), false)).toBeNull()
  })

  it('types a character outside the basic plane as one', () => {
    expect(treeAction(key('中'), false)).toEqual({ t: 'type', char: '中' })
    expect(treeAction(key('🎉'), false)).toEqual({ t: 'type', char: '🎉' })
    expect(treeAction(key('F5'), false)).toBeNull()
  })
})

describe('filterEntries', () => {
  const es = [{ name: 'README.md' }, { name: 'src' }, { name: 'readme-old.txt' }, { name: 'Makefile' }]

  it('finds a name holding the filter, any case, in listed order', () => {
    expect(filterEntries(es, 'readme').map((e) => e.name)).toEqual(['README.md', 'readme-old.txt'])
  })

  it('means a capital', () => {
    expect(filterEntries(es, 'README').map((e) => e.name)).toEqual(['README.md'])
  })

  it('is the listing for no filter', () => {
    expect(filterEntries(es, '')).toBe(es)
  })
})

describe('moveRow', () => {
  it('stays inside the list', () => {
    expect(moveRow(0, 3, { t: 'up' })).toBe(0)
    expect(moveRow(2, 3, { t: 'down' })).toBe(2)
    expect(moveRow(1, 3, { t: 'down' })).toBe(2)
    expect(moveRow(1, 3, { t: 'first' })).toBe(0)
    expect(moveRow(0, 3, { t: 'last' })).toBe(2)
    expect(moveRow(5, 3, { t: 'type', char: 'x' })).toBe(2)
    expect(moveRow(0, 0, { t: 'down' })).toBe(-1)
  })
})
