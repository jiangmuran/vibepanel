import { describe, expect, it } from 'vitest'

import {
  GROUP_TITLE,
  SECTION_GROUP,
  SETTINGS_GROUPS,
  SETTINGS_HOME,
  SETTINGS_SECTIONS,
  groupFromKey,
  groupOf,
  sectionsIn,
} from './groups'

/**
 * The rail's arithmetic, without a browser.
 *
 * What can go wrong here is not visible in a screenshot: a section that no
 * group claims is a block nothing can reach, and a section two groups claim is
 * a deep link that lands somewhere different depending on which branch runs.
 */
describe('the settings rail', () => {
  it('puts every section in exactly one group', () => {
    const claimed = SETTINGS_GROUPS.flatMap((g) => sectionsIn(g))
    expect([...claimed].sort()).toEqual([...SETTINGS_SECTIONS].sort())
    expect(new Set(claimed).size).toBe(claimed.length)
  })

  it('leaves no group empty', () => {
    // An empty rail item is a name that promises something and shows a blank
    // panel, which is worse than not having the name.
    for (const g of SETTINGS_GROUPS) {
      expect(sectionsIn(g).length, g).toBeGreaterThan(0)
    }
  })

  it('names every group in both languages', () => {
    const keys = SETTINGS_GROUPS.map((g) => GROUP_TITLE[g])
    expect(new Set(keys).size).toBe(SETTINGS_GROUPS.length)
  })

  it('opens somewhere real when nobody asked for anything', () => {
    expect(SETTINGS_SECTIONS).toContain(SETTINGS_HOME)
    expect(SETTINGS_GROUPS).toContain(groupOf(SETTINGS_HOME))
  })

  it('sends the section a caller asked for to the group that holds it', () => {
    for (const s of SETTINGS_SECTIONS) {
      expect(sectionsIn(groupOf(s)), s).toContain(s)
    }
    // The one deep link in the panel: the "states are being guessed" notice.
    // It is named here so that moving state reporting into another group is a
    // decision somebody makes rather than a link that quietly stops working.
    expect(groupOf('reporting')).toBe(SECTION_GROUP.reporting)
  })
})

describe('arrow keys on the rail', () => {
  const first = SETTINGS_GROUPS[0]
  const last = SETTINGS_GROUPS[SETTINGS_GROUPS.length - 1]

  it('moves on both axes, because the rail is a column or a row', () => {
    expect(groupFromKey('ArrowDown', first)).toBe(SETTINGS_GROUPS[1])
    expect(groupFromKey('ArrowRight', first)).toBe(SETTINGS_GROUPS[1])
    expect(groupFromKey('ArrowUp', SETTINGS_GROUPS[1])).toBe(first)
    expect(groupFromKey('ArrowLeft', SETTINGS_GROUPS[1])).toBe(first)
  })

  it('wraps at both ends', () => {
    expect(groupFromKey('ArrowDown', last)).toBe(first)
    expect(groupFromKey('ArrowUp', first)).toBe(last)
  })

  it('jumps with Home and End', () => {
    expect(groupFromKey('Home', last)).toBe(first)
    expect(groupFromKey('End', first)).toBe(last)
  })

  it('leaves every other key alone', () => {
    // Returning null is what tells the handler not to swallow the event: the
    // group below the rail has its own keys, and Escape closes the dialog.
    for (const key of ['Escape', 'Enter', ' ', 'Tab', 'a', 'PageDown']) {
      expect(groupFromKey(key, first), key).toBeNull()
    }
  })
})
