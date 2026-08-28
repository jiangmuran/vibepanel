import { describe, expect, it, vi } from 'vitest'

/**
 * The reducer's index arithmetic, against a tab universe that is not the
 * product's.
 *
 * `panes.ts` is generic in everything except one import: it reads the list of
 * tabs from `chrome.ts`. The side panel ships two tabs, so `MAX_PANES` is two
 * and no arrangement with three panes exists — which would quietly delete
 * every suite below, because the off-by-one this arithmetic exists for only
 * appears when a pane vanishes *between* two others.
 *
 * That coverage is not about `files` and `notes`. It is about indices, and it
 * has to survive the tab list changing — which it has done twice now, six to
 * four and four to two, breaking a handful of these tests each time on
 * assertions that had nothing to do with what they were checking. So this file
 * mocks the tab list to four synthetic ids and runs the arithmetic against
 * those.
 *
 * The invariant, the stored-layout repair and the storage key stay in
 * panes.test.ts against the real tabs: those *are* about which tabs exist.
 */
vi.mock('./chrome', async (importOriginal) => {
  const real = await importOriginal<typeof import('./chrome')>()
  return { ...real, PANEL_TABS: ['a', 'b', 'c', 'd'] as const }
})

import { PANEL_TABS, type PanelTab } from './chrome'
import {
  MAX_PANES,
  PANE_MIN_HEIGHT,
  PANE_MIN_RATIO,
  activate,
  defaultLayout,
  fitTo,
  groupOf,
  mergeGroup,
  moveTab,
  moveTowards,
  paneKeyCommand,
  readLayout,
  resizeAt,
  serialiseLayout,
  type PaneLayout,
} from './panes'

describe('the universe under test', () => {
  it('is the synthetic one and not the product\'s', () => {
    // Without this the whole file passes by testing two tabs twice, which is
    // precisely the erosion it exists to prevent.
    expect([...PANEL_TABS]).toEqual(['a', 'b', 'c', 'd'])
    expect(MAX_PANES).toBe(4)
  })
})

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

// The tabs, positionally. Which they are is the point of not naming them.
//
// Named positionally rather than spelled out, because everything below is
// about index arithmetic and not about which tabs exist. Three tests broke the
// day a sixth tab was added and several more the day six became four, every
// one of them on a hard-coded name that had nothing to do with what was being

// The tabs, positionally. Which they are is the point of not naming them.
//
// Named positionally rather than spelled out, because everything below is
// about index arithmetic and not about which tabs exist. Three tests broke the
// day a sixth tab was added and several more the day six became four, every
// one of them on a hard-coded name that had nothing to do with what was being
// asserted.
//
// Cast rather than read off PANEL_TABS: the mock replaces the tuple at
// runtime, and TypeScript still sees the real two-element one — so indexing it
// for a third and fourth id is a type error about a value that exists.
const [A, B, C, D] = ['a', 'b', 'c', 'd'] as unknown as [PanelTab, PanelTab, PanelTab, PanelTab]

/** The rest, then B, then C. Three panes, every tab. */
function three(): PaneLayout {
  let l = defaultLayout()
  l = moveTab(l, B, { kind: 'new', at: 1 })
  l = moveTab(l, C, { kind: 'new', at: 2 })
  return l
}

// What is left in the first pane after `moved` have been dragged out of it.
function rest(...moved: PanelTab[]): string {
  return PANEL_TABS.filter((t) => !moved.includes(t)).join('+')
}

/** Four panes: everything else, then B, C and D each on their own. */
function four(): PaneLayout {
  let l = defaultLayout()
  l = moveTab(l, B, { kind: 'new', at: 1 })
  l = moveTab(l, C, { kind: 'new', at: 2 })
  l = moveTab(l, D, { kind: 'new', at: 3 })
  return l
}

describe('moving a tab', () => {
  it('splits one pane into two', () => {
    const l = moveTab(defaultLayout(), B, { kind: 'new', at: 1 })
    expectSound(l, 'split')
    expect(l.groups).toHaveLength(2)
    expect(l.groups[1].tabs).toEqual([B])
    expect(l.groups[0].tabs).not.toContain(B)
  })

  it('joins two panes back into one', () => {
    const split = moveTab(defaultLayout(), B, { kind: 'new', at: 1 })
    const back = moveTab(split, B, { kind: 'join', group: 0 })
    expectSound(back, 'join')
    expect(back.groups).toHaveLength(1)
  })

  it('leaves the pane it emptied behind rather than an empty strip', () => {
    const l = three()
    const merged = moveTab(l, B, { kind: 'join', group: 0 })
    expectSound(merged, 'emptied pane')
    expect(merged.groups).toHaveLength(2)
  })

  it('shows something in the pane the tab left', () => {
    const l = activate(defaultLayout(), B)
    const after = moveTab(l, B, { kind: 'new', at: 1 })
    expectSound(after, 'the pane left behind')
    expect(after.groups[0].active).not.toBe(B)
  })

  it('is a no-op when the drop changes nothing', () => {
    const l = three()
    // Same object, so a released drag that went nowhere does not rewrite
    // storage or remount four panels.
    expect(moveTab(l, PANEL_TABS[0], { kind: 'join', group: 0 })).toBe(l)
    expect(moveTab(l, B, { kind: 'new', at: 1 })).toBe(l)
    expect(moveTab(l, B, { kind: 'new', at: 2 })).toBe(l)
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
    const l = four() // [rest, B, C, D]
    const moved = moveTab(l, B, { kind: 'new', at: 3 })
    expectSound(moved, 'moved past the pane it left')
    expect(moved.groups.map((g) => g.tabs.join('+'))).toEqual([rest(B, C, D), C, B, D])
  })

  it('joins the pane it was aimed at when the source pane vanishes under it', () => {
    // The same correction on the other arm. Without it the index runs off the
    // end of the shortened list and the drop is silently dropped.
    const l = three() // [everything else, B, C]
    const joined = moveTab(l, B, { kind: 'join', group: 2 })
    expectSound(joined, 'joined past the pane it left')
    expect(joined.groups.map((g) => g.tabs.join('+'))).toEqual([rest(B, C), `${C}+${B}`])
  })

  it('cannot be asked to move a tab that is not in the layout', () => {
    const l = defaultLayout()
    expect(moveTab({ ...l, groups: [{ tabs: [A], active: A, size: 1 }] }, B, {
      kind: 'join',
      group: 0,
    }).groups[0].tabs).toEqual([A])
  })

  it('brings the moved tab to the front of where it landed', () => {
    const l = moveTab(three(), C, { kind: 'join', group: 0 })
    expect(l.groups[0].active).toBe(C)
  })
})

describe('moving a tab without a pointer', () => {
  it('goes to the next pane in that direction', () => {
    const l = three()
    const up = moveTowards(l, C, 'up')
    expectSound(up, 'moved up')
    expect(groupOf(up, C)).toBe(1)
  })

  it('makes a new pane when there is not one to go to', () => {
    const l = defaultLayout()
    const down = moveTowards(l, B, 'down')
    expectSound(down, 'moved off the end')
    expect(down.groups).toHaveLength(2)
    expect(down.groups[1].tabs).toEqual([B])
  })

  it('stops rather than shuffling when there is nowhere to go', () => {
    const l = three()
    // Already a pane of its own at the bottom.
    expect(moveTowards(l, C, 'down')).toBe(l)
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
    expect(l.groups[1].tabs).toEqual([B, C])
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

describe('a layout that does not fit the window it opens in', () => {
  it('merges from the bottom until it does', () => {
    const l = three()
    const fitted = fitTo(l, PANE_MIN_HEIGHT * 2)
    expectSound(fitted, 'fitted')
    expect(fitted.groups).toHaveLength(2)
    // From the bottom, because the top of the column is where the panel opens
    // and what you were looking at is up there.
    expect(fitted.groups[0].tabs.join('+')).toEqual(rest(B, C))
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
