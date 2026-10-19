import { describe, expect, it } from 'vitest'

import { neighbour, panelShortcut } from './shortcuts'

const key = (k: string, mods: Partial<Record<'ctrlKey' | 'shiftKey' | 'altKey' | 'metaKey', boolean>> = {}) => ({
  key: k,
  ctrlKey: false,
  shiftKey: false,
  altKey: false,
  metaKey: false,
  ...mods,
})

describe('the panel keys', () => {
  it('are Ctrl+Shift with an arrow', () => {
    expect(panelShortcut(key('ArrowUp', { ctrlKey: true, shiftKey: true }))).toEqual({ act: 'prev' })
    expect(panelShortcut(key('ArrowDown', { ctrlKey: true, shiftKey: true }))).toEqual({ act: 'next' })
    expect(panelShortcut(key('ArrowLeft', { ctrlKey: true, shiftKey: true }))).toEqual({ act: 'leave' })
  })

  it('leave every key a terminal needs to the terminal', () => {
    // Ctrl+K kills to the end of the line and Tab completes. (Alt+← and → move
    // by word, and stay the terminal's by default: see the ⌥ keys below.)
    expect(panelShortcut(key('k', { ctrlKey: true }))).toBeNull()
    expect(panelShortcut(key('Tab'))).toBeNull()
    expect(panelShortcut(key('ArrowLeft', { ctrlKey: true }))).toBeNull()
    expect(panelShortcut(key('ArrowRight', { ctrlKey: true, shiftKey: true }))).toBeNull()
  })

  it('is not taken with another modifier as well', () => {
    expect(panelShortcut(key('ArrowUp', { ctrlKey: true, shiftKey: true, altKey: true }))).toBeNull()
    expect(panelShortcut(key('ArrowUp', { ctrlKey: true, shiftKey: true, metaKey: true }))).toBeNull()
  })

  it('acts on the press, not the release', () => {
    expect(panelShortcut({ ...key('ArrowUp', { ctrlKey: true, shiftKey: true }), type: 'keyup' })).toBeNull()
  })
})

describe('the next session', () => {
  it('follows the sidebar order and wraps', () => {
    expect(neighbour(['a', 'b', 'c'], 'a', 'next')).toBe('b')
    expect(neighbour(['a', 'b', 'c'], 'c', 'next')).toBe('a')
    expect(neighbour(['a', 'b', 'c'], 'a', 'prev')).toBe('c')
  })

  it('starts at the top when nothing is selected', () => {
    expect(neighbour(['a', 'b'], null, 'next')).toBe('a')
    expect(neighbour([], 'a', 'next')).toBeNull()
  })
})

describe('the panel keys in a text field', () => {
  const el = (tagName: string, extra: Record<string, unknown> = {}, classes: string[] = []) => ({
    tagName,
    classList: { contains: (c: string) => classes.includes(c) },
    ...extra,
  })
  const leave = (target: unknown) =>
    panelShortcut({ ...key('ArrowLeft', { ctrlKey: true, shiftKey: true }), target })

  it("are the field's own: select by word or paragraph", () => {
    expect(leave(el('TEXTAREA'))).toBeNull()
    expect(leave(el('INPUT', { type: 'text' }))).toBeNull()
    expect(leave(el('INPUT', { type: 'search' }))).toBeNull()
    expect(leave(el('DIV', { isContentEditable: true }))).toBeNull()
  })

  it("are the panel's in a terminal, whose focus sits in a textarea", () => {
    expect(leave(el('TEXTAREA', {}, ['xterm-helper-textarea']))).toEqual({ act: 'leave' })
  })

  it("are the panel's on a button or a checkbox", () => {
    expect(leave(el('BUTTON'))).toEqual({ act: 'leave' })
    expect(leave(el('INPUT', { type: 'checkbox' }))).toEqual({ act: 'leave' })
    expect(leave(null)).toEqual({ act: 'leave' })
  })
})

describe('the ⌥ / Alt keys', () => {
  const alt = (k: string, code: string, extra: Record<string, boolean> = {}) => ({
    ...key(k, { altKey: true, ...extra }),
    code,
  })

  it('move between sessions with ⌥↑ and ⌥↓', () => {
    expect(panelShortcut(alt('ArrowUp', 'ArrowUp'))).toEqual({ act: 'prev' })
    expect(panelShortcut(alt('ArrowDown', 'ArrowDown'))).toEqual({ act: 'next' })
  })

  it('jump to a session by the digit key, whatever character a Mac makes of it', () => {
    // ⌥1 on a Mac is "¡" and ⌥9 is "ª"; the physical key is what was meant.
    expect(panelShortcut(alt('¡', 'Digit1'))).toEqual({ act: 'goto', n: 1 })
    expect(panelShortcut(alt('ª', 'Digit9'))).toEqual({ act: 'goto', n: 9 })
    expect(panelShortcut(alt('3', 'Digit3'))).toEqual({ act: 'goto', n: 3 })
    expect(panelShortcut(alt('º', 'Digit0'))).toBeNull()
  })

  it('open the list with ⌥/, which a Mac reports as ÷', () => {
    expect(panelShortcut(alt('÷', 'Slash'))).toEqual({ act: 'help' })
  })

  it('leave ⌥← and ⌥→ to the terminal unless the setting gives them to the panel', () => {
    expect(panelShortcut(alt('ArrowLeft', 'ArrowLeft'))).toBeNull()
    expect(panelShortcut(alt('ArrowRight', 'ArrowRight'), { sideArrows: 'terminal' })).toBeNull()
    expect(panelShortcut(alt('ArrowLeft', 'ArrowLeft'), { sideArrows: 'panel' })).toEqual({ act: 'prevProject' })
    expect(panelShortcut(alt('ArrowRight', 'ArrowRight'), { sideArrows: 'panel' })).toEqual({ act: 'nextProject' })
  })

  it('move between regions with ⌥Tab on a Mac only', () => {
    // Alt+Tab elsewhere belongs to the window manager and never arrives.
    expect(panelShortcut(alt('Tab', 'Tab'), { mac: true })).toEqual({ act: 'regionNext' })
    expect(panelShortcut(alt('Tab', 'Tab', { shiftKey: true }), { mac: true })).toEqual({ act: 'regionPrev' })
    expect(panelShortcut(alt('Tab', 'Tab'), { mac: false })).toBeNull()
  })

  it('are never AltGr, which types characters on a European Windows layout', () => {
    expect(panelShortcut(alt('@', 'Digit2', { ctrlKey: true }))).toBeNull()
    expect(panelShortcut(alt('ArrowUp', 'ArrowUp', { ctrlKey: true }))).toBeNull()
  })

  it('are not taken with ⌘ held, which is the browser\'s', () => {
    expect(panelShortcut(alt('¡', 'Digit1', { metaKey: true }))).toBeNull()
  })

  it('type in a text field as usual', () => {
    const field = { tagName: 'TEXTAREA', classList: { contains: () => false } }
    expect(panelShortcut({ ...alt('¡', 'Digit1'), target: field })).toBeNull()
    expect(panelShortcut({ ...alt('ArrowUp', 'ArrowUp'), target: field })).toBeNull()
  })
})
