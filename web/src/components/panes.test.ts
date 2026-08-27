import { describe, expect, it } from 'vitest'

import { PANEL_TABS, type PanelTab } from './chrome'
import {
  MAX_PANES,
  PANE_MIN_HEIGHT,
  PANE_MIN_RATIO,
  activate,
  defaultLayout,
  dropKindAt,
  dropTargetFrom,
  fitTo,
  groupOf,
  layoutStorageKey,
  mergeGroup,
  moveTab,
  moveTowards,
  notesTodosSplit,
  paneKeyCommand,
  parseLayout,
  readLayout,
  resizeAt,
  serialiseLayout,
  toggleNotesTodos,
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
  expect(all.slice().sort(), `${what}: the tabs are not the five tabs`).toEqual(
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

/**
 * The same layout, allowing for the last bit of a float.
 *
 * Reading a layout back renormalises its shares, which moves them by about
 * 1e-16. That is not drift worth failing on and it is not drift worth
 * "fixing" by rounding on write either — what has to survive a reload is the
 * arrangement, and this says so exactly.
 */
function expectSameLayout(got: PaneLayout, want: PaneLayout, what: string) {
  expect(got.groups.map((g) => ({ tabs: g.tabs, active: g.active })), what).toEqual(
    want.groups.map((g) => ({ tabs: g.tabs, active: g.active })),
  )
  got.groups.forEach((g, i) => {
    expect(g.size, `${what}: pane ${i}`).toBeCloseTo(want.groups[i].size, 9)
  })
}

/** files+monitor+tokens over notes over todos. Three panes, all five tabs. */
function three(): PaneLayout {
  let l = defaultLayout()
  l = moveTab(l, 'notes', { kind: 'new', at: 1 })
  l = moveTab(l, 'todos', { kind: 'new', at: 2 })
  return l
}

/** Four panes: files+git+monitor, then notes, todos and tokens each on their own. */
function four(): PaneLayout {
  let l = defaultLayout()
  l = moveTab(l, 'notes', { kind: 'new', at: 1 })
  l = moveTab(l, 'todos', { kind: 'new', at: 2 })
  l = moveTab(l, 'tokens', { kind: 'new', at: 3 })
  return l
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
      groups: [{ tabs: ['files', 'monitor'], active: 'todos', size: 1 }],
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
          { tabs: ['monitor', 'notes', 'todos', 'tokens'], active: 'monitor', size: 1 },
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
        { tabs: ['monitor', 'notes', 'todos', 'tokens'], active: 'monitor', size: 0.999 },
      ],
    })
    expectSound(l, 'a hairline pane')
    expect(l.groups[0].size).toBeGreaterThanOrEqual(PANE_MIN_RATIO)
  })

  it('drops panes past the fifth', () => {
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
    const l = three()
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

describe('moving a tab', () => {
  it('splits one pane into two', () => {
    const l = moveTab(defaultLayout(), 'monitor', { kind: 'new', at: 1 })
    expectSound(l, 'split')
    expect(l.groups).toHaveLength(2)
    expect(l.groups[1].tabs).toEqual(['monitor'])
    expect(l.groups[0].tabs).not.toContain('monitor')
  })

  it('joins two panes back into one', () => {
    const split = moveTab(defaultLayout(), 'monitor', { kind: 'new', at: 1 })
    const back = moveTab(split, 'monitor', { kind: 'join', group: 0 })
    expectSound(back, 'join')
    expect(back.groups).toHaveLength(1)
  })

  it('leaves the pane it emptied behind rather than an empty strip', () => {
    const l = three()
    const merged = moveTab(l, 'notes', { kind: 'join', group: 0 })
    expectSound(merged, 'emptied pane')
    expect(merged.groups).toHaveLength(2)
  })

  it('shows something in the pane the tab left', () => {
    const l = activate(defaultLayout(), 'notes')
    const after = moveTab(l, 'notes', { kind: 'new', at: 1 })
    expectSound(after, 'the pane left behind')
    expect(after.groups[0].active).not.toBe('notes')
  })

  it('is a no-op when the drop changes nothing', () => {
    const l = three()
    // Same object, so a released drag that went nowhere does not rewrite
    // storage or remount four panels.
    expect(moveTab(l, 'files', { kind: 'join', group: 0 })).toBe(l)
    expect(moveTab(l, 'notes', { kind: 'new', at: 1 })).toBe(l)
    expect(moveTab(l, 'notes', { kind: 'new', at: 2 })).toBe(l)
  })

  it('lands where it was aimed when the source pane vanishes under it', () => {
    // The off-by-one this arithmetic exists for: removing the source group
    // moves every index after it up by one, so an insertion point below the
    // source is not the index it was when the drag started.
    //
    // Four panes, not three, and not the last slot. With three panes the
    // insertion index is clamped to the end of the list anyway, so the wrong
    // answer and the right one agree — this test passed against the arithmetic
    // removed until it was written this way.
    const l = four() // [files+git+monitor, notes, todos, tokens]
    const moved = moveTab(l, 'notes', { kind: 'new', at: 3 })
    expectSound(moved, 'moved past the pane it left')
    expect(moved.groups.map((g) => g.tabs.join('+'))).toEqual([
      'files+git+monitor',
      'todos',
      'notes',
      'tokens',
    ])
  })

  it('joins the pane it was aimed at when the source pane vanishes under it', () => {
    // The same correction on the other arm. Without it the index runs off the
    // end of the shortened list and the drop is silently dropped.
    const l = three() // [files+git+monitor+tokens, notes, todos]
    const joined = moveTab(l, 'notes', { kind: 'join', group: 2 })
    expectSound(joined, 'joined past the pane it left')
    expect(joined.groups.map((g) => g.tabs.join('+'))).toEqual([
      'files+git+monitor+tokens',
      'todos+notes',
    ])
  })

  it('cannot be asked to move a tab that is not in the layout', () => {
    const l = defaultLayout()
    expect(moveTab({ ...l, groups: [{ tabs: ['files'], active: 'files', size: 1 }] }, 'notes', {
      kind: 'join',
      group: 0,
    }).groups[0].tabs).toEqual(['files'])
  })

  it('brings the moved tab to the front of where it landed', () => {
    const l = moveTab(three(), 'todos', { kind: 'join', group: 0 })
    expect(l.groups[0].active).toBe('todos')
  })
})

describe('moving a tab without a pointer', () => {
  it('goes to the next pane in that direction', () => {
    const l = three()
    const up = moveTowards(l, 'todos', 'up')
    expectSound(up, 'moved up')
    expect(groupOf(up, 'todos')).toBe(1)
  })

  it('makes a new pane when there is not one to go to', () => {
    const l = defaultLayout()
    const down = moveTowards(l, 'monitor', 'down')
    expectSound(down, 'moved off the end')
    expect(down.groups).toHaveLength(2)
    expect(down.groups[1].tabs).toEqual(['monitor'])
  })

  it('stops rather than shuffling when there is nowhere to go', () => {
    const l = three()
    // Already a pane of its own at the bottom.
    expect(moveTowards(l, 'todos', 'down')).toBe(l)
  })

  it('is reachable from Alt and an arrow, and from nothing else', () => {
    expect(paneKeyCommand('ArrowUp', true)).toBe('up')
    expect(paneKeyCommand('ArrowDown', true)).toBe('down')
    // The bare arrows move between tabs; taking them here would break that.
    expect(paneKeyCommand('ArrowUp', false)).toBeNull()
    expect(paneKeyCommand('ArrowLeft', true)).toBeNull()
    expect(paneKeyCommand('Enter', true)).toBeNull()
  })
})

describe('merging', () => {
  it('folds a pane into its neighbour', () => {
    const l = mergeGroup(three(), 2, 'up')
    expectSound(l, 'merged up')
    expect(l.groups).toHaveLength(2)
    expect(l.groups[1].tabs).toEqual(['notes', 'todos'])
  })

  it('gives the merged pane the room the other one had', () => {
    const l = three()
    const want = l.groups[1].size + l.groups[2].size
    expect(mergeGroup(l, 2, 'up').groups[1].size).toBeCloseTo(want, 6)
  })

  it('refuses to merge off either end', () => {
    const l = three()
    expect(mergeGroup(l, 0, 'up')).toBe(l)
    expect(mergeGroup(l, 2, 'down')).toBe(l)
    expect(mergeGroup(l, 9, 'up')).toBe(l)
  })
})

describe('resizing', () => {
  it('moves one boundary and leaves the others where they were', () => {
    const l = three()
    const before = l.groups[0].size
    const after = resizeAt(l, 1, before + 0.4)
    expectSound(after, 'resized')
    expect(after.groups[0].size).toBeCloseTo(before, 6)
    expect(after.groups[1].size + after.groups[2].size).toBeCloseTo(
      l.groups[1].size + l.groups[2].size,
      6,
    )
  })

  it('never drags a pane below the floor', () => {
    const l = three()
    for (const ratio of [-5, 0, 0.0001, 0.999, 5]) {
      const after = resizeAt(l, 1, ratio)
      expectSound(after, `ratio ${ratio}`)
      expect(after.groups[1].size).toBeGreaterThanOrEqual(PANE_MIN_RATIO - 1e-9)
      expect(after.groups[2].size).toBeGreaterThanOrEqual(PANE_MIN_RATIO - 1e-9)
    }
  })

  it('has no boundary below the last pane', () => {
    const l = three()
    expect(resizeAt(l, 2, 0.5)).toBe(l)
    expect(resizeAt(l, -1, 0.5)).toBe(l)
  })
})

describe('notes and todo together, in one press', () => {
  it('is off in the default layout', () => {
    expect(notesTodosSplit(defaultLayout())).toBe(false)
  })

  it('builds the pair and takes it apart again', () => {
    const on = toggleNotesTodos(defaultLayout())
    expectSound(on, 'the pair')
    expect(notesTodosSplit(on)).toBe(true)
    expect(groupOf(on, 'notes')).not.toBe(groupOf(on, 'todos'))

    const off = toggleNotesTodos(on)
    expectSound(off, 'the pair, undone')
    expect(notesTodosSplit(off)).toBe(false)
  })

  it('works from any tab, which is what its label promises', () => {
    for (const from of PANEL_TABS) {
      const on = toggleNotesTodos(activate(defaultLayout(), from))
      expectSound(on, `from ${from}`)
      expect(notesTodosSplit(on), `from ${from}`).toBe(true)
    }
  })

  it('does not report the pair when one of them is behind another tab', () => {
    // Both in panes, but the notes pane is showing the file tree: they are not
    // "together" in any sense a person would recognise.
    let l = toggleNotesTodos(defaultLayout())
    const at = groupOf(l, 'notes')
    l = moveTab(l, 'files', { kind: 'join', group: at })
    expect(l.groups[at].active).toBe('files')
    expect(notesTodosSplit(l)).toBe(false)
  })
})

describe('a layout that does not fit the window it opens in', () => {
  it('merges from the bottom until it does', () => {
    const l = three()
    const fitted = fitTo(l, PANE_MIN_HEIGHT * 2)
    expectSound(fitted, 'fitted')
    expect(fitted.groups).toHaveLength(2)
    // From the bottom, because the top of the column is where the panel opens
    // and what you were looking at is up there.
    expect(fitted.groups[0].tabs).toEqual(['files', 'git', 'monitor', 'tokens'])
  })

  it('leaves a layout that fits alone', () => {
    const l = three()
    expect(fitTo(l, 2000)).toBe(l)
  })

  it('never merges away the last pane', () => {
    expectSound(fitTo(three(), 1), 'a window with no room at all')
    expect(fitTo(three(), 1).groups).toHaveLength(1)
  })

  it('does nothing at all when nothing has been measured yet', () => {
    // The frame before the first paint reports zero, and treating that as "no
    // room" would collapse everybody's layout on load.
    const l = three()
    expect(fitTo(l, 0)).toBe(l)
    expect(fitTo(l, Number.NaN)).toBe(l)
    expect(fitTo(l, -100)).toBe(l)
  })
})

describe('every operation leaves a sound layout', () => {
  // A crude fuzz over the reducer. The invariant is asserted after each step,
  // so the failure names the operation that broke it rather than the render
  // three actions later that could not cope.
  it('holds across a long run of arbitrary moves', () => {
    let seed = 12345
    const rand = (n: number) => {
      seed = (seed * 1103515245 + 12345) & 0x7fffffff
      return seed % n
    }
    let l = defaultLayout()
    for (let step = 0; step < 400; step++) {
      const tab = PANEL_TABS[rand(PANEL_TABS.length)]
      switch (rand(6)) {
        case 0:
          l = moveTab(l, tab, { kind: 'join', group: rand(l.groups.length) })
          break
        case 1:
          l = moveTab(l, tab, { kind: 'new', at: rand(l.groups.length + 1) })
          break
        case 2:
          l = moveTowards(l, tab, rand(2) ? 'up' : 'down')
          break
        case 3:
          l = mergeGroup(l, rand(l.groups.length), rand(2) ? 'up' : 'down')
          break
        case 4:
          l = resizeAt(l, rand(l.groups.length), rand(100) / 100)
          break
        default:
          l = activate(l, tab)
      }
      expectSound(l, `step ${step}`)
      // And it must survive the round trip through storage at every step, so a
      // reload in the middle of somebody's afternoon is not where this breaks.
      expectSameLayout(readLayout(serialiseLayout(l)), l, `step ${step} through storage`)
    }
  })
})
