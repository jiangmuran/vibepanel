import { describe, expect, it } from 'vitest'

import { shortcutLines } from './ShortcutsDialog'

/**
 * The list of keys says them the way the keyboard does, and says only the
 * ones that work here: ⌥Tab exists on a Mac and nowhere else, and ⌥← ⌥→ are
 * described as whatever the setting has made them.
 */
describe('the list of keys', () => {
  const combos = (mac: boolean, side: 'terminal' | 'panel') =>
    shortcutLines(mac, side).flatMap((g) => g.lines.map((l) => l.combo.join(' ')))
  const whats = (mac: boolean, side: 'terminal' | 'panel') =>
    shortcutLines(mac, side).flatMap((g) => g.lines.map((l) => l.what))

  it('writes ⌥ on a Mac and Alt elsewhere', () => {
    expect(combos(true, 'terminal')).toContain('⌥↑ ⌥↓')
    expect(combos(false, 'terminal')).toContain('Alt+↑ Alt+↓')
    expect(combos(false, 'terminal').join(' ')).not.toContain('⌥')
  })

  it('lists ⌥Tab on a Mac only', () => {
    expect(whats(true, 'terminal')).toContain('keys.regions')
    expect(whats(false, 'terminal')).not.toContain('keys.regions')
  })

  it('describes ⌥← ⌥→ as the setting has them', () => {
    expect(whats(true, 'terminal')).toContain('keys.wordJump')
    expect(whats(true, 'panel')).toContain('keys.projects')
    expect(whats(true, 'panel')).not.toContain('keys.wordJump')
  })
})
