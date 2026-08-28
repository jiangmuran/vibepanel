import { describe, expect, it } from 'vitest'

import {
  BOTTOM_MIN_HEIGHT,
  BOTTOM_MIN_MAIN_HEIGHT,
  PANEL_DENSE_WIDTH,
  PANEL_MAX_WIDTH,
  PANEL_MIN_WIDTH,
  DETAIL_BLOCKS,
  DOCK_BLOCKS,
  PANEL_TABS,
  RESIZE_STEP,
  RESIZE_STEP_LARGE,
  RETIRED_TABS,
  STACKED_TABS,
  bottomControls,
  clampBottomHeight,
  clampPanelWidth,
  isDetailBlock,
  paneControls,
  panelDensity,
  panelFocusOrder,
  resizeStep,
  swapDirection,
  tabFromKey,
  tabOwnsHeight,
  type PanelTab,
} from './chrome'

/** Every width the panel can be, one pixel at a time. */
function everyWidth(): number[] {
  const out: number[] = []
  for (let w = PANEL_MIN_WIDTH; w <= PANEL_MAX_WIDTH; w++) out.push(w)
  return out
}

describe('the set of controls does not depend on the size of the window', () => {
  // The complaint this file exists for: "到右边两个的时候会非常僵硬的多一个按钮".
  // The split toggle was rendered on the notes and todo tabs and nowhere else,
  // so arriving at either of them grew the header a button and slid the
  // collapse control 28 pixels left — under a pointer already travelling
  // towards where it used to be.
  it('is the same two controls on every tab', () => {
    const ids = PANEL_TABS.map(() => paneControls(0, true).map((c) => c.id).join(','))
    expect(new Set(ids), `the panel header varies by tab: ${JSON.stringify(ids)}`).toEqual(
      new Set(['menu,collapse']),
    )
  })

  it('is the same two controls at every width', () => {
    const orders = everyWidth().map((w) => panelFocusOrder(w, 'files').join(','))
    expect(new Set(orders).size, 'the panel header restructures itself as it is resized').toBe(1)
  })

  it('no longer offers the notes/todo preset', () => {
    // It built one arrangement -- notes and todo in two panes -- and that
    // arrangement is the notes tab now. A control that presses and changes
    // nothing teaches people the panel does not respond, so it was removed
    // rather than left returning the layout it was given.
    const ids = paneControls(0, true).map((c) => c.id)
    expect(ids, `the split preset is still in the header: ${ids.join(',')}`).not.toContain('split')
  })

  it('gives every pane below the first its menu and nothing else', () => {
    // The menu is the only way to split, merge or restore without a mouse, so
    // a pane without one is a pane a keyboard cannot rearrange.
    for (const i of [1, 2, 3, 4]) {
      expect(paneControls(i, false).map((c) => c.id), `pane ${i}`).toEqual(['menu'])
    }
  })

  it('gives every pane menu its own name in the DOM', () => {
    const ids = [0, 1, 2, 3, 4].map((i) => paneControls(i, false)[0].testid)
    expect(new Set(ids).size, `two panes share a testid: ${ids.join(', ')}`).toBe(ids.length)
  })

  it('puts the tabs before the controls, so focus crosses the strip once', () => {
    expect(panelFocusOrder(320, 'notes')).toEqual([
      'panel-tab-notes',
      'pane-menu-0',
      'panel-collapse',
    ])
  })

  it('keeps both terminal-strip controls at every tab count, empty included', () => {
    for (const count of [0, 1, 2, 8, 30]) {
      expect(
        bottomControls(count).map((c) => c.id),
        `the strip's controls change at ${count} terminals`,
      ).toEqual(['new', 'collapse'])
    }
  })
})

describe('what does change with width is presentation', () => {
  it('lays the figures out in two columns once there is room for two', () => {
    expect(panelDensity(PANEL_DENSE_WIDTH)).toBe('wide')
    expect(panelDensity(PANEL_DENSE_WIDTH - 1)).toBe('narrow')
  })

  it('is narrow at the width the panel opens at, and wide at its widest', () => {
    // The default is 280 (RIGHT_DEFAULT_WIDTH in App). A threshold below it
    // would mean the panel opens two-column and there is nothing left for
    // dragging it wider to buy.
    expect(panelDensity(280)).toBe('narrow')
    expect(panelDensity(PANEL_MIN_WIDTH)).toBe('narrow')
    expect(panelDensity(PANEL_MAX_WIDTH)).toBe('wide')
  })

  it('gives the extra width something to buy', () => {
    // The panel drags between 200 and 640. A threshold outside that range is
    // one nobody can cross, and the pixels the range allows would buy nothing
    // but whitespace -- which is the complaint this replaced the label
    // threshold to answer.
    expect(PANEL_DENSE_WIDTH).toBeGreaterThan(PANEL_MIN_WIDTH)
    expect(PANEL_DENSE_WIDTH).toBeLessThan(PANEL_MAX_WIDTH)
  })

  it('changes exactly once across the whole range', () => {
    // Two thresholds is a panel that reflows twice on one drag, which is the
    // same defect wearing a smaller hat.
    const flips = everyWidth().filter(
      (w, i, all) => i > 0 && panelDensity(w) !== panelDensity(all[i - 1]),
    )
    expect(flips).toEqual([PANEL_DENSE_WIDTH])
  })
})

// The ends of the strip rather than two tab names: wrapping is what these
// assert, and a sixth tab turned every one of them into a failure about
// `tokens` that had nothing to do with wrapping. Going from six tabs to four
// did it again, to the ones that had not been converted the first time.
//
// The literal order in "the tab list" below is deliberately still a literal --
// that one is the guard, and changing it is the moment to notice the order
// changed. Nothing else here spells a tab name.
const first = PANEL_TABS[0]
const last = PANEL_TABS[PANEL_TABS.length - 1]

describe('arrow keys inside the tab strip', () => {
  it('moves one tab at a time, in the direction of the arrow', () => {
    // Derived from the list rather than spelled out. Three test files had
    // hard-coded pairs and every one of them broke the day the list changed,
    // on an assertion that had nothing to do with what it was checking.
    const [a, b] = PANEL_TABS
    expect(tabFromKey('ArrowRight', a)).toBe(b)
    expect(tabFromKey('ArrowLeft', b)).toBe(a)
  })

  it('wraps rather than stopping', () => {
    expect(tabFromKey('ArrowRight', last)).toBe(first)
    expect(tabFromKey('ArrowLeft', first)).toBe(last)
  })

  it('has Home and End', () => {
    expect(tabFromKey('Home', last)).toBe(first)
    expect(tabFromKey('End', first)).toBe(last)
  })

  it('leaves every other key alone', () => {
    // The handler swallows the event only when this answers. Returning a tab
    // for Enter or a letter would take typing away from the panel below.
    for (const key of ['Enter', ' ', 'Tab', 'a', 'ArrowUp', 'Escape']) {
      expect(tabFromKey(key, 'notes'), key).toBeNull()
    }
  })
})

describe('the body enters from the side the strip moved towards', () => {
  it('follows the order of the tabs', () => {
    expect(swapDirection(first, last)).toBe('forward')
    expect(swapDirection(last, first)).toBe('back')
    // A tab to itself is forward, not back. Nothing moved, and a body that
    // slides in from the left because the strip did not move reads as a
    // glitch — which is what `>=` in swapDirection is for.
    expect(swapDirection(first, first)).toBe('forward')
  })

  it('agrees with the strip for every pair', () => {
    // Every ordered pair, because the arithmetic being backwards for exactly
    // one of them is what "reads as a glitch" is made of.
    for (const from of PANEL_TABS) {
      for (const to of PANEL_TABS) {
        const want = PANEL_TABS.indexOf(to) < PANEL_TABS.indexOf(from) ? 'back' : 'forward'
        expect(swapDirection(from, to), `${from} -> ${to}`).toBe(want)
      }
    }
  })
})

describe('the dividers resize from the keyboard', () => {
  it('steps towards growing the panel, which is away from it', () => {
    // Both dividers sit on the near edge of what they size, so the sign is the
    // drag: left widens the side panel, up heightens the terminal strip.
    expect(resizeStep('ArrowLeft', false)).toBe(RESIZE_STEP)
    expect(resizeStep('ArrowUp', false)).toBe(RESIZE_STEP)
    expect(resizeStep('ArrowRight', false)).toBe(-RESIZE_STEP)
    expect(resizeStep('ArrowDown', false)).toBe(-RESIZE_STEP)
  })

  it('takes a larger step with shift', () => {
    expect(resizeStep('ArrowLeft', true)).toBe(RESIZE_STEP_LARGE)
    expect(resizeStep('ArrowRight', true)).toBe(-RESIZE_STEP_LARGE)
    expect(RESIZE_STEP_LARGE).toBeGreaterThan(RESIZE_STEP)
  })

  it('leaves every other key alone', () => {
    for (const key of ['Enter', 'Tab', 'PageUp', 'x']) {
      expect(resizeStep(key, false), key).toBeNull()
    }
  })
})

describe('clamping', () => {
  it('keeps the panel between its two ends', () => {
    expect(clampPanelWidth(-500)).toBe(PANEL_MIN_WIDTH)
    expect(clampPanelWidth(99999)).toBe(PANEL_MAX_WIDTH)
    expect(clampPanelWidth(300)).toBe(300)
  })

  it('never lets the terminal strip take the whole window', () => {
    // 600 tall, so the strip may have 480 at most and the main terminal keeps
    // its 120. Dragging past that is the drag that empties the pane above.
    expect(clampBottomHeight(10_000, 600)).toBe(600 - BOTTOM_MIN_MAIN_HEIGHT)
    expect(clampBottomHeight(0, 600)).toBe(BOTTOM_MIN_HEIGHT)
    expect(clampBottomHeight(220, 600)).toBe(220)
  })

  it('resolves a window too short for both in favour of the strip existing', () => {
    // The floor and the ceiling cross below 200px of shared height. A strip of
    // negative height is a strip with no tab row, and the tab row is the only
    // way back out of it.
    expect(clampBottomHeight(220, 150)).toBe(BOTTOM_MIN_HEIGHT)
  })
})

describe('the tab list', () => {
  it('is the order the panel renders and navigates in', () => {
    // RightPanel maps over this rather than keeping its own array. Two lists
    // in two files that have to agree is how a left arrow ends up moving
    // right.
    //
    // The one literal that stays a literal. Everything else in this file and
    // in panes.test.ts derives from PANEL_TABS, so this is the single place
    // where changing the order is a thing somebody has to look at.
    expect([...PANEL_TABS]).toEqual(['files', 'notes'])
  })

  it('names every tab a browser check drives', () => {
    // render-check and panes-check both drive both by testid. A tab renamed
    // here and not there is a selector that matches nothing, which those
    // scripts report as a missing element rather than as the rename it is.
    const driven: PanelTab[] = ['files', 'notes']
    for (const tab of driven) expect(PANEL_TABS).toContain(tab)
  })

  it('does not still contain a tab it retired', () => {
    // The other direction, and the one that matters for a stored layout: a
    // name in RETIRED_TABS that is somehow still a tab means parseLayout's
    // repair is being tested against a string it will never see.
    for (const gone of RETIRED_TABS) {
      expect(
        PANEL_TABS as readonly string[],
        `${gone} is retired and still a tab`,
      ).not.toContain(gone)
    }
  })

  it('remembers every tab it has ever retired', () => {
    // The second literal in this file, and it is a literal for the same reason
    // the tab order is: it is the guard.
    //
    // RETIRED_TABS is append-only. It is not read at runtime — parseLayout
    // drops an unrecognised tab by not recognising it — so nothing anywhere
    // breaks if a name is dropped from it, and what is lost is the fixture:
    // panes.test.ts drives the stored-layout repair from this list, and a
    // shorter list is a repair tested against fewer of the strings that are
    // really in people's browsers. Mutation testing found exactly that, by
    // deleting two names and watching every check stay green.
    //
    // Six tabs became four became two. Each of these was a tab somebody had in
    // a pane of their own on the day it was removed.
    const ever = ['git', 'todos', 'vnc', 'monitor', 'tokens']
    for (const gone of ever) {
      expect(
        RETIRED_TABS as readonly string[],
        `${gone} was a tab and is not in RETIRED_TABS; the repair no longer tests it`,
      ).toContain(gone)
    }
  })
})

describe('the tabs that divide their own height', () => {
  it('are tabs', () => {
    for (const tab of STACKED_TABS) expect(PANEL_TABS).toContain(tab)
  })

  it('answers for every tab, and says yes to exactly those', () => {
    // The pane wraps a tab in a scroller unless this says not to, and a
    // scroller gives its child whatever height it asks for -- so a stacked tab
    // that answered `false` here would collapse to its two headers with
    // nothing between them.
    for (const tab of PANEL_TABS) {
      expect(tabOwnsHeight(tab), tab).toBe((STACKED_TABS as readonly string[]).includes(tab))
    }
  })

  it('is every tab there is, which is a coincidence and not a rule', () => {
    // Both tabs are stacks today. The list stays a list rather than becoming a
    // `true` so that the next tab that is one scrolling column is a name added
    // here, not a condition rebuilt at the call site.
    expect([...STACKED_TABS].sort()).toEqual([...PANEL_TABS].sort())
  })
})

describe('the blocks that open out of their compact form', () => {
  it('has every dock block in the openable list', () => {
    // The dock's header is the press target and PanelDetail is what it opens.
    // A block in one list and not the other is a control that presses and
    // draws nothing.
    for (const b of DOCK_BLOCKS) expect(DETAIL_BLOCKS).toContain(b)
  })

  it('has one that is not in the dock, and knows it', () => {
    // The repository. Its compact form is a line in the file tree's header,
    // because it is a fact about the directory above it rather than about the
    // machine — but the gesture that opens it is the same one.
    const extra = DETAIL_BLOCKS.filter((b) => !(DOCK_BLOCKS as readonly string[]).includes(b))
    expect(extra).toEqual(['repo'])
  })

  it('recognises exactly the blocks it lists', () => {
    for (const b of DETAIL_BLOCKS) expect(isDetailBlock(b)).toBe(true)
    // A block id out of a stored value or a data attribute is a string like
    // any other, and the panel must not open a detail for one it cannot draw.
    for (const bad of ['', 'todos', 'files', 'vnc', 'Repo']) {
      expect(isDetailBlock(bad), bad).toBe(false)
    }
  })
})
