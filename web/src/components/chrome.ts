import type { Key } from '../i18n'

/**
 * What the chrome around the terminal contains, and how it presents itself.
 *
 * Split out of the components because the interesting half of it is a
 * decision, not a render: *which controls exist* has to be the same answer at
 * every width and on every tab, and the only way to keep that true is to have
 * one function say so and a test that sweeps it.
 *
 * The rule this file exists to hold is narrow and worth stating plainly. A
 * control that is present at one size and absent at another is a layout that
 * rearranges itself while somebody is aiming at it — the side panel used to
 * grow a split toggle the moment you reached the notes tab, so the collapse
 * button moved 28 pixels left under a pointer already travelling towards it.
 * Presentation may change with size; the set of controls may not.
 */

export const PANEL_TABS = ['files', 'monitor', 'notes', 'todos', 'tokens'] as const

export type PanelTab = (typeof PANEL_TABS)[number]

/** Narrower than this and the panels are unusable rather than merely tight. */
export const PANEL_MIN_WIDTH = 200

/** Wider than this and it is a second terminal, not a side panel. */
export const PANEL_MAX_WIDTH = 640

/**
 * The width at which the selected tab is named in words rather than drawn as
 * an icon alone.
 *
 * Below it the segmented track has under 170 pixels to divide five ways, and a
 * name in the selected fifth of that is two letters and an ellipsis — which
 * tells you less than the icon it displaced. The label does not blink out at
 * this width; it folds shut. See the `max-width` transition in RightPanel.
 *
 * A label is not a control, which is why this is allowed to depend on width at
 * all. panelControls() below does not.
 */
export const PANEL_LABEL_WIDTH = 250

export interface PanelChrome {
  /** The selected tab shows its name; the other four are icons either way. */
  labelled: boolean
}

export function panelChrome(width: number): PanelChrome {
  return { labelled: width >= PANEL_LABEL_WIDTH }
}

export type PanelControlId = 'split' | 'collapse'

export interface PanelControl {
  id: PanelControlId
  testid: string
}

/**
 * The controls after the tabs, in focus order — the same two, always.
 *
 * The split toggle used to be rendered only on the notes and todo tabs, on the
 * reasoning that it means nothing on the other three. It means something on
 * all five now: see splitTarget(), which reads it as "show me notes and todo
 * together" and goes there. That is a smaller change than it sounds and it is
 * the whole fix — a control whose *meaning* is constant does not have to
 * appear and disappear to stay honest.
 *
 * The argument is taken and ignored on purpose. A function of the tab that
 * does not vary with the tab is what the test can hold on to; a constant array
 * would let a future conditional be written at the call site instead, where
 * nothing is watching.
 */
export function panelControls(_tab: PanelTab): PanelControl[] {
  return [
    { id: 'split', testid: 'panel-split' },
    { id: 'collapse', testid: 'panel-collapse' },
  ]
}

/**
 * Everything in the panel header a keyboard reaches, in the order it reaches
 * it: the tabs left to right, then the controls.
 *
 * Only the selected tab is in the tab order — that is what a tablist is, and
 * arrow keys move within it. See tabFromKey().
 */
export function panelFocusOrder(_width: number, tab: PanelTab): string[] {
  return [`panel-tab-${tab}`, ...panelControls(tab).map((c) => c.testid)]
}

/** Notes and todo are the pair worth seeing at once: what you are thinking and
 *  what you have left. Files and the monitor are lookups, not companions. */
export function splittable(tab: PanelTab): boolean {
  return tab === 'notes' || tab === 'todos'
}

/**
 * Where the split control takes you.
 *
 * From notes or todo with the split on, it turns the split off and leaves you
 * where you are. From anywhere else it turns the split on and moves to notes,
 * because that is what the control has always promised in words — "show notes
 * and todo together" — and doing it from the files tab is not a surprise, it
 * is the sentence.
 */
export function splitTarget(tab: PanelTab, split: boolean): { tab: PanelTab; split: boolean } {
  if (split && splittable(tab)) return { tab, split: false }
  return { tab: splittable(tab) ? tab : 'notes', split: true }
}

/** Which of the two things the split control is about to do, for its tooltip. */
export function splitTitleKey(tab: PanelTab, split: boolean): Key {
  return splitTarget(tab, split).split ? 'panel.splitOn' : 'panel.splitOff'
}

/**
 * Arrow-key navigation within the tab strip.
 *
 * Wrapping, because five tabs in a row with a stop at each end is a control
 * that punishes you for holding a key down. Home and End are in the ARIA
 * tablist pattern and cost one line each.
 *
 * Returns null for every other key, which is what tells the handler to leave
 * the event alone — the panel below the strip has its own keys.
 */
export function tabFromKey(key: string, current: PanelTab): PanelTab | null {
  const at = PANEL_TABS.indexOf(current)
  if (at < 0) return null
  const n = PANEL_TABS.length
  if (key === 'ArrowRight') return PANEL_TABS[(at + 1) % n]
  if (key === 'ArrowLeft') return PANEL_TABS[(at - 1 + n) % n]
  if (key === 'Home') return PANEL_TABS[0]
  if (key === 'End') return PANEL_TABS[n - 1]
  return null
}

/**
 * Which way the body should enter when the tab changes.
 *
 * The strip is a row, so moving right means the new panel comes from the right.
 * Anything that reads as a movement has to agree with the movement that caused
 * it, or it reads as a glitch instead.
 */
export function swapDirection(from: PanelTab, to: PanelTab): 'forward' | 'back' {
  return PANEL_TABS.indexOf(to) >= PANEL_TABS.indexOf(from) ? 'forward' : 'back'
}

/** One arrow key is a nudge; with shift it is a shove. */
export const RESIZE_STEP = 16
export const RESIZE_STEP_LARGE = 64

/**
 * How far an arrow key moves a divider, signed the way the divider moves.
 *
 * Both dividers sit on the near edge of the thing they size, so growing the
 * panel means dragging *away* from it: left for the side panel, up for the
 * terminal strip. The signs here are the drag, not the axis, which is why
 * ArrowLeft is positive.
 *
 * Returns null for anything else, so the handler knows not to swallow the key.
 */
export function resizeStep(key: string, shift: boolean): number | null {
  const step = shift ? RESIZE_STEP_LARGE : RESIZE_STEP
  if (key === 'ArrowLeft' || key === 'ArrowUp') return step
  if (key === 'ArrowRight' || key === 'ArrowDown') return -step
  return null
}

export function clampPanelWidth(px: number): number {
  return Math.max(PANEL_MIN_WIDTH, Math.min(px, PANEL_MAX_WIDTH))
}

/** Notes over todo, or the reverse; never one of them squeezed to a caption. */
export function clampSplitRatio(ratio: number): number {
  return Math.max(0.15, Math.min(0.85, ratio))
}

/** Below this the strip is a row of tabs with no terminal under it. */
export const BOTTOM_MIN_HEIGHT = 80

/** Leave at least this much of the main terminal visible. */
export const BOTTOM_MIN_MAIN_HEIGHT = 120

/**
 * The strip may not eat the terminal it belongs to.
 *
 * `available` is the height the two share. A window short enough that the
 * floor and the ceiling cross resolves to the floor, because a strip that is
 * too short is a nuisance and a main terminal with no rows is a bug report.
 */
export function clampBottomHeight(px: number, available: number): number {
  return Math.max(BOTTOM_MIN_HEIGHT, Math.min(px, available - BOTTOM_MIN_MAIN_HEIGHT))
}

export type BottomControlId = 'new' | 'collapse'

export interface BottomControl {
  id: BottomControlId
  testid: string
}

/**
 * The terminal strip's controls, in focus order — the same two at every tab
 * count, including none.
 *
 * The count is taken and ignored for the reason panelControls() ignores the
 * tab. The failure being guarded against is the obvious one: hiding "close the
 * strip" when the strip is empty, which is exactly the moment somebody wants
 * it gone.
 */
export function bottomControls(_count: number): BottomControl[] {
  return [
    { id: 'new', testid: 'bottom-new' },
    { id: 'collapse', testid: 'bottom-collapse' },
  ]
}
