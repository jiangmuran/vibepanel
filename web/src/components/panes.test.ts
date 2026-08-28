import { describe, expect, it } from 'vitest'

import { PANEL_TABS, RETIRED_TABS, type PanelTab } from './chrome'
import {
  MAX_PANES,
  PANE_MIN_RATIO,
  defaultLayout,
  dropKindAt,
  dropTargetFrom,
  groupOf,
  layoutStorageKey,
  moveTab,
  parseLayout,
  readLayout,
  serialiseLayout,
  type PaneLayout,
} from './panes'

/**
 * The invariant, in one place, asserted after every operation below.
 *
 * Every tab in exactly one group; every group non-empty; every `active` in its
 * own group; sizes that are shares of one. If any operation can break any of
 * those, the panel it produces is one where a tab is unreachable or a pane is
 * a heading with nothing under it — and the operation that did it will not be
 * the one that looks wrong.
 */
function expectSound(layout: PaneLayout, what: string) {
  const all = layout.groups.flatMap((g) => g.tabs)
  expect(all.slice().sort(), `${what}: the tabs are not the panel's tabs`).toEqual(
    [...PANEL_TABS].sort(),
  )
  expect(new Set(all).size, `${what}: a tab is in two panes at once`).toBe(all.length)
  expect(layout.groups.length, `${what}: no panes at all`).toBeGreaterThan(0)
  expect(layout.groups.length, `${what}: more panes than tabs`).toBeLessThanOrEqual(MAX_PANES)
  for (const g of layout.groups) {
    expect(g.tabs.length, `${what}: an empty pane`).toBeGreaterThan(0)
    expect(g.tabs, `${what}: a pane showing a tab it does not hold`).toContain(g.active)
    expect(g.size, `${what}: a pane with a size that is not a number`).toBeTypeOf('number')
    expect(Number.isFinite(g.size), `${what}: a pane sized ${g.size}`).toBe(true)
    expect(g.size, `${what}: a pane squeezed out of existence`).toBeGreaterThan(0)
  }
  const total = layout.groups.reduce((s, g) => s + g.size, 0)
  expect(total, `${what}: the sizes do not add up to the column`).toBeCloseTo(1, 6)
}


describe('the default', () => {
  it('is every tab in one pane', () => {
    const l = defaultLayout()
    expectSound(l, 'the default')
    expect(l.groups).toHaveLength(1)
    expect(l.groups[0].active).toBe('files')
  })
})

describe('a stored layout is data, not code', () => {
  const bad: [string, unknown][] = [
    ['null', null],
    ['a string', 'panes'],
    ['an array', [{ tabs: ['files'] }]],
    ['a number', 7],
    ['no version', { groups: [{ tabs: ['files'], active: 'files', size: 1 }] }],
    ['a version from the future', { version: 99, groups: [] }],
    ['no groups', { version: 1 }],
    ['groups that are not an array', { version: 1, groups: 'files' }],
    ['no group with anything in it', { version: 1, groups: [{}, { tabs: [] }, 3] }],
  ]
  for (const [what, raw] of bad) {
    it(`falls back to the default for ${what}`, () => {
      const l = parseLayout(raw)
      expectSound(l, what)
      expect(l).toEqual(defaultLayout())
    })
  }

  it('drops a tab this build has never heard of', () => {
    const l = parseLayout({
      version: 1,
      groups: [{ tabs: ['files', 'sockets', 'monitor'], active: 'files', size: 1 }],
    })
    expectSound(l, 'an unknown tab')
    expect(l.groups[0].tabs).not.toContain('sockets' as PanelTab)
  })

  // The repairs above were written for a key edited by hand or a build from a
  // branch. The case that actually happened is a tab this build *retired*:
  // `git` and `todos` stopped being tabs, and every layout in every browser
  // named at least one of them -- most of them in a pane of its own, because
  // giving the repository or the checklist its own pane was the point of the
  // feature.
  //
  // Same code path, different blast radius, and nothing pinned it with the
  // names that were really in those keys.
  describe('a layout written by yesterday, naming tabs that are gone', () => {
    it('keeps the panes that survive and drops the ones that do not', () => {
      const l = parseLayout({
        version: 1,
        groups: [
          { tabs: ['files', 'git'], active: 'git', size: 0.4 },
          { tabs: ['monitor', 'tokens'], active: 'tokens', size: 0.3 },
          { tabs: ['notes', 'todos'], active: 'todos', size: 0.3 },
        ],
      })
      expectSound(l, 'yesterday, three panes')
      // The middle pane held nothing but retired tabs and is dropped whole;
      // the two that still hold something keep their order.
      expect(l.groups.map((g) => g.tabs.join('+'))).toEqual(['files', 'notes'])
      // The pane whose selected tab was retired shows the one that is left,
      // rather than a pane with a body and no tab selected.
      expect(l.groups[0].active).toBe('files')
      expect(l.groups[1].active).toBe('notes')
    })

    it('does not let a pane that emptied cost a pane that did not', () => {
      // MAX_PANES is the tab count, which is two. Six stored panes with four
      // of them retired is exactly two survivors -- and only if the cap is
      // counted after a group is dropped rather than before. Counted before,
      // the loop stops at the second stored pane, `notes` never gets a pane of
      // its own, and it is appended to whichever one survived.
      //
      // The retired panes are deliberately first and in the middle, because
      // the failure this catches is positional: a cap that counts the panes it
      // threw away loses whatever came last.
      const l = parseLayout({
        version: 1,
        groups: [
          { tabs: ['git'], active: 'git', size: 0.2 },
          { tabs: ['todos'], active: 'todos', size: 0.2 },
          { tabs: ['files'], active: 'files', size: 0.2 },
          { tabs: ['monitor'], active: 'monitor', size: 0.1 },
          { tabs: ['tokens'], active: 'tokens', size: 0.2 },
          { tabs: ['notes'], active: 'notes', size: 0.1 },
        ],
      })
      expectSound(l, 'yesterday, six panes')
      expect(l.groups.map((g) => g.tabs.join('+'))).toEqual(['files', 'notes'])
    })

    it('comes back as a panel when every stored pane was a retired tab', () => {
      // Not empty, and not a throw. Somebody who had dragged the repository
      // and the checklist out and closed everything else opens on the default,
      // which is a panel they can use.
      const l = parseLayout({
        version: 1,
        groups: [
          { tabs: ['git'], active: 'git', size: 0.5 },
          { tabs: ['todos'], active: 'todos', size: 0.5 },
        ],
      })
      expectSound(l, 'yesterday, nothing left')
      expect(l).toEqual(defaultLayout())
    })

    it('survives every retired name in every position', () => {
      // Each one on its own, in a pane of its own, beside a pane that holds
      // everything else. Driven from RETIRED_TABS so the next tab that is
      // retired is covered by adding one string.
      for (const gone of RETIRED_TABS) {
        const l = parseLayout({
          version: 1,
          groups: [
            { tabs: [gone], active: gone, size: 0.5 },
            { tabs: [...PANEL_TABS], active: PANEL_TABS[0], size: 0.5 },
          ],
        })
        expectSound(l, `a pane holding only ${gone}`)
        expect(l.groups, gone).toHaveLength(1)
      }
    })

    it('falls back to the first tab the pane still holds', () => {
      const l = parseLayout({
        version: 1,
        groups: [{ tabs: [...PANEL_TABS], active: 'todos', size: 1 }],
      })
      expectSound(l, 'a retired active tab')
      expect(l.groups[0].active).toBe(PANEL_TABS[0])
    })
  })

  it('keeps a duplicated tab in the first pane that claims it', () => {
    const l = parseLayout({
      version: 1,
      groups: [
        { tabs: ['files', 'notes'], active: 'notes', size: 0.5 },
        { tabs: ['notes', 'monitor'], active: 'monitor', size: 0.5 },
      ],
    })
    expectSound(l, 'a duplicated tab')
    expect(groupOf(l, 'notes')).toBe(0)
  })

  it('gives a tab the layout forgot somewhere to live', () => {
    // The repair that matters most and shows least: a tab in no group has no
    // strip anywhere, so the file tree simply is not in the panel and nothing
    // says why.
    const l = parseLayout({
      version: 1,
      groups: [{ tabs: ['notes'], active: 'notes', size: 1 }],
    })
    expectSound(l, 'a missing tab')
    expect(groupOf(l, 'files')).toBeGreaterThanOrEqual(0)
  })

  it('replaces an active tab the pane does not hold', () => {
    const l = parseLayout({
      version: 1,
      groups: [{ tabs: ['files', 'monitor'], active: 'notes', size: 1 }],
    })
    expectSound(l, 'a stray active tab')
    expect(l.groups[0].active).toBe('files')
  })

  it('equalises sizes that are not shares of anything', () => {
    for (const size of [0, -1, Number.NaN, 'half', undefined, Infinity]) {
      const l = parseLayout({
        version: 1,
        groups: [
          { tabs: ['files'], active: 'files', size },
          { tabs: ['monitor', 'tokens', 'notes'], active: 'monitor', size: 1 },
        ],
      })
      expectSound(l, `size ${String(size)}`)
      expect(l.groups[0].size).toBeCloseTo(0.5, 6)
    }
  })

  it('equalises rather than keeping a pane below the floor', () => {
    const l = parseLayout({
      version: 1,
      groups: [
        { tabs: ['files'], active: 'files', size: 0.001 },
        { tabs: ['monitor', 'tokens', 'notes'], active: 'monitor', size: 0.999 },
      ],
    })
    expectSound(l, 'a hairline pane')
    expect(l.groups[0].size).toBeGreaterThanOrEqual(PANE_MIN_RATIO)
  })

  it('drops panes past the last one there is a tab for', () => {
    const l = parseLayout({
      version: 1,
      groups: Array.from({ length: 40 }, () => ({ tabs: ['files'], active: 'files', size: 1 })),
    })
    expectSound(l, 'forty panes')
    expect(l.groups.length).toBeLessThanOrEqual(MAX_PANES)
  })

  it('survives whatever came back from the key', () => {
    for (const json of ['', 'null', '{', '[]', '﻿{"version":1}', 'undefined']) {
      expectSound(readLayout(json), JSON.stringify(json))
    }
    expectSound(readLayout(null), 'nothing stored')
  })

  it('round-trips what it wrote', () => {
    const l = moveTab(defaultLayout(), PANEL_TABS[1], { kind: 'new', at: 1 })
    expect(l.groups).toHaveLength(2)
    expect(readLayout(serialiseLayout(l))).toEqual(l)
  })
})

describe('the layout belongs to the screen it was made on', () => {
  it('gives a phone and a 4K monitor different keys', () => {
    expect(layoutStorageKey(390, 844)).not.toBe(layoutStorageKey(3840, 2160))
  })

  it('keeps the same key while a window is nudged', () => {
    // Exact pixels as a key would make every layout single-use: one drag of
    // the window edge and the arrangement is gone.
    expect(layoutStorageKey(1445, 905)).toBe(layoutStorageKey(1500, 950))
  })

  it('changes when the window changes shape, not only size', () => {
    // Panes stack vertically, so height is the dimension this feature spends.
    expect(layoutStorageKey(1440, 900)).not.toBe(layoutStorageKey(1440, 400))
  })

  it('answers for nonsense sizes rather than producing "NaNxNaN"', () => {
    for (const [w, h] of [[0, 0], [-1, -1], [Number.NaN, 10]]) {
      expect(layoutStorageKey(w, h)).toMatch(/^vibepanel\.panes\.\d+x\d+$/)
    }
  })

  it('is a localStorage key and nothing else', () => {
    // Nothing about a layout is sent to the server. Two devices signed into
    // the same panel keep their own arrangements, which is the whole ask.
    expect(layoutStorageKey(1440, 900)).toMatch(/^vibepanel\./)
  })
})

describe('where a drop lands', () => {
  it('reads the three bands off the body of a pane', () => {
    expect(dropKindAt(10, 300)).toBe('before')
    expect(dropKindAt(150, 300)).toBe('join')
    expect(dropKindAt(290, 300)).toBe('after')
  })

  it('treats the strip above the body as its own tabs', () => {
    // The offset is measured from the top of the *body*, so anything at or
    // above zero is the tab strip. Bands over the whole pane put the strip
    // inside the "new pane above" third, and a fourteen-pixel sideways wiggle
    // on a tab — which is a clumsy click — split the panel in two. Found by
    // driving it in a browser, not by reading it.
    expect(dropKindAt(0, 300)).toBe('join')
    expect(dropKindAt(-30, 300)).toBe('join')
  })

  it('answers something for a pane with no height rather than dividing by it', () => {
    expect(dropKindAt(10, 0)).toBe('join')
    expect(dropKindAt(10, Number.NaN)).toBe('join')
  })

  it('turns a band and a pane index into a target', () => {
    expect(dropTargetFrom('join', '2')).toEqual({ kind: 'join', group: 2 })
    expect(dropTargetFrom('before', '2')).toEqual({ kind: 'new', at: 2 })
    expect(dropTargetFrom('after', '2')).toEqual({ kind: 'new', at: 3 })
  })

  it('refuses anything that did not come from a drop zone', () => {
    // Both halves arrive as strings out of the document, which is the same
    // place a stored layout arrives from and deserves the same suspicion.
    expect(dropTargetFrom('join', null)).toBeNull()
    expect(dropTargetFrom('join', 'two')).toBeNull()
    expect(dropTargetFrom('join', '-1')).toBeNull()
    expect(dropTargetFrom('join', '1.5')).toBeNull()
    expect(dropTargetFrom('sideways', '0')).toBeNull()
    expect(dropTargetFrom(null, '0')).toBeNull()
  })
})
