import { describe, expect, it } from 'vitest'

import {
  BOTTOM_MIN_HEIGHT,
  BOTTOM_MIN_MAIN_HEIGHT,
  PANEL_LABEL_WIDTH,
  PANEL_MAX_WIDTH,
  PANEL_MIN_WIDTH,
  PANEL_TABS,
  RESIZE_STEP,
  RESIZE_STEP_LARGE,
  bottomControls,
  clampBottomHeight,
  clampPanelWidth,
  clampSplitRatio,
  panelChrome,
  panelControls,
  panelFocusOrder,
  resizeStep,
  splitTarget,
  splitTitleKey,
  splittable,
  swapDirection,
  tabFromKey,
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
    const ids = PANEL_TABS.map((tab) => panelControls(tab).map((c) => c.id).join(','))
    expect(new Set(ids), `panelControls varies by tab: ${JSON.stringify(ids)}`).toEqual(
      new Set(['split,collapse']),
    )
  })

  it('is the same two controls at every width', () => {
    const orders = everyWidth().map((w) => panelFocusOrder(w, 'files').join(','))
    expect(new Set(orders).size, 'the panel header restructures itself as it is resized').toBe(1)
  })

  it('puts the tabs before the controls, so focus crosses the header once', () => {
    expect(panelFocusOrder(320, 'notes')).toEqual([
      'panel-tab-notes',
      'panel-split',
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
  it('names the selected tab once there is room for the name', () => {
    expect(panelChrome(PANEL_LABEL_WIDTH).labelled).toBe(true)
    expect(panelChrome(PANEL_LABEL_WIDTH - 1).labelled).toBe(false)
  })

  it('is labelled at the width the panel opens at, and unlabelled at its narrowest', () => {
    // The default is 280 (RIGHT_DEFAULT_WIDTH in App). A threshold above it
    // would mean nobody ever sees a label without dragging for it.
    expect(panelChrome(280).labelled).toBe(true)
    expect(panelChrome(PANEL_MIN_WIDTH).labelled).toBe(false)
  })

  it('changes exactly once across the whole range', () => {
    // Two thresholds is a strip that reflows twice on one drag, which is the
    // same defect wearing a smaller hat.
    const flips = everyWidth().filter(
      (w, i, all) => i > 0 && panelChrome(w).labelled !== panelChrome(all[i - 1]).labelled,
    )
    expect(flips).toEqual([PANEL_LABEL_WIDTH])
  })
})

describe('the split control means something on all five tabs', () => {
  it('turns the split on and goes to notes from a tab that has none', () => {
    for (const tab of ['files', 'monitor', 'tokens'] as const) {
      expect(splitTarget(tab, false)).toEqual({ tab: 'notes', split: true })
      // Already on elsewhere: pressing it still takes you to the pair, which
      // is what the label promises.
      expect(splitTarget(tab, true)).toEqual({ tab: 'notes', split: true })
    }
  })

  it('toggles in place on the two tabs that are the split', () => {
    for (const tab of ['notes', 'todos'] as const) {
      expect(splittable(tab)).toBe(true)
      expect(splitTarget(tab, false)).toEqual({ tab, split: true })
      expect(splitTarget(tab, true)).toEqual({ tab, split: false })
    }
  })

  it('says which of the two it is about to do', () => {
    expect(splitTitleKey('files', false)).toBe('panel.splitOn')
    expect(splitTitleKey('files', true)).toBe('panel.splitOn')
    expect(splitTitleKey('notes', false)).toBe('panel.splitOn')
    expect(splitTitleKey('notes', true)).toBe('panel.splitOff')
  })
})

describe('arrow keys inside the tab strip', () => {
  it('moves one tab at a time, in the direction of the arrow', () => {
    expect(tabFromKey('ArrowRight', 'files')).toBe('monitor')
    expect(tabFromKey('ArrowLeft', 'monitor')).toBe('files')
  })

  it('wraps rather than stopping', () => {
    expect(tabFromKey('ArrowRight', 'tokens')).toBe('files')
    expect(tabFromKey('ArrowLeft', 'files')).toBe('tokens')
  })

  it('has Home and End', () => {
    expect(tabFromKey('Home', 'todos')).toBe('files')
    expect(tabFromKey('End', 'files')).toBe('tokens')
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
    expect(swapDirection('files', 'tokens')).toBe('forward')
    expect(swapDirection('tokens', 'files')).toBe('back')
    expect(swapDirection('notes', 'todos')).toBe('forward')
    expect(swapDirection('todos', 'notes')).toBe('back')
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

  it('never squeezes notes or todo to a caption', () => {
    expect(clampSplitRatio(0)).toBe(0.15)
    expect(clampSplitRatio(1)).toBe(0.85)
    expect(clampSplitRatio(0.5)).toBe(0.5)
  })
})

describe('the tab list', () => {
  it('is the order the panel renders and navigates in', () => {
    // RightPanel maps over this rather than keeping its own array. Two lists
    // in two files that have to agree is how a left arrow ends up moving
    // right.
    expect([...PANEL_TABS]).toEqual(['files', 'monitor', 'notes', 'todos', 'tokens'])
  })

  it('names every tab the render check drives', () => {
    const driven: PanelTab[] = ['files', 'monitor', 'notes', 'todos', 'tokens']
    for (const tab of driven) expect(PANEL_TABS).toContain(tab)
  })
})
