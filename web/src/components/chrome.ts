

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

// Four, and each one is a question rather than a data source.
//
// It was six. `git` and `todos` were tabs of their own, and both were the
// bottom half of a question somebody was already asking on another tab: what
// is in this directory *and* what has changed in it; what am I thinking *and*
// what is left. A tab you have to leave to answer the second half of your own
// question is a tab that costs you the first half. So they moved inside
// `files` and `notes`, below a divider you can drag — see STACKED_TABS.
//
// Their names did not disappear with them: `panel.git` and `panel.todos` still
// name the lower half of the tab that absorbed them. What disappeared is the
// tab strip's claim that they were separate places.
export const PANEL_TABS = ['files', 'monitor', 'tokens', 'notes'] as const

export type PanelTab = (typeof PANEL_TABS)[number]

/**
 * Tab ids that a build before this one could have written into localStorage.
 *
 * Not used by anything at runtime — parseLayout drops an unknown tab by not
 * recognising it, which is the same path a corrupted key takes and needs no
 * list. This is here so the test that pins that repair names the strings that
 * were actually in people's browsers rather than a plausible-looking
 * invention, and so the next tab that is retired is added here rather than
 * quietly relied upon to behave the same way.
 */
export const RETIRED_TABS = ['git', 'todos', 'vnc'] as const

/**
 * The tabs whose body divides the pane's height itself.
 *
 * A stacked tab is two panels and a divider, so its height is the pane's
 * height and not the height of its content. The pane gives it `h-full` rather
 * than the `min-h-full` every other tab gets — a box whose height is its
 * content's leaves the flex column inside it nothing to divide, and it
 * collapses to its two headers with the divider between them.
 *
 * Named here rather than checked with `tab === 'files' || tab === 'notes'` at
 * the one call site, because the second call site is the one that forgets.
 */
export const STACKED_TABS = ['files', 'notes'] as const

export function tabOwnsHeight(tab: PanelTab): boolean {
  return (STACKED_TABS as readonly string[]).includes(tab)
}

/** Narrower than this and the panels are unusable rather than merely tight. */
export const PANEL_MIN_WIDTH = 200

/** Wider than this and it is a second terminal, not a side panel. */
export const PANEL_MAX_WIDTH = 640

/**
 * The width above which a panel lays its figures out in two columns.
 *
 * This replaced PANEL_LABEL_WIDTH, and the replacement is the point. That
 * threshold decided whether the selected tab wore its name; the strip is icons
 * now, so the only thing width still buys is *more figures on screen at once*,
 * which is what a wider column was dragged out for in the first place. A panel
 * that only stretches as it widens is a panel where the extra 360 pixels the
 * range allows buy nothing but whitespace.
 *
 * 380 is where two label/value pairs and their gutter stop having to truncate:
 * the widest pair in either language is the memory meter's "内存 / 12.4 GiB of
 * 31.1 GiB", which needs about 175px, and two of those plus 16px of gutter and
 * 24px of padding is 390. Below it the same figures stack, which is the same
 * information and not a different layout.
 *
 * A column count is not a control, which is why this is allowed to depend on
 * width at all. paneControls() below does not.
 */
export const PANEL_DENSE_WIDTH = 380

export type PanelDensity = 'narrow' | 'wide'

/**
 * How much a panel body may put on one row.
 *
 * Two values and one threshold, deliberately. Three would mean a panel that
 * reflows twice on one drag, which is the same defect the label fold had,
 * wearing a smaller hat.
 */
export function panelDensity(width: number): PanelDensity {
  return width >= PANEL_DENSE_WIDTH ? 'wide' : 'narrow'
}

export type PaneControlId = 'menu' | 'collapse'

export interface PaneControl {
  id: PaneControlId
  testid: string
}

/**
 * The controls after a pane's tabs, in focus order.
 *
 * Every pane has its menu — which is where splitting, merging and restoring
 * live for anyone not using a mouse. The first pane additionally carries the
 * one control that belongs to the *panel* rather than to a pane: the collapse
 * chevron. That is a structural rule, not a size-dependent or tab-dependent
 * one: the first pane is always the first pane, and the panel's own control has
 * to be somewhere.
 *
 * There were two. The other was a toggle that put notes and todo in two panes
 * at once, and it was removed rather than left as furniture: notes and todo are
 * one tab now, stacked with a divider between them, so the arrangement the
 * toggle built is the arrangement you get. A control that presses and changes
 * nothing is worse than no control, because it teaches people the panel does
 * not respond.
 *
 * Written as a function of the pane rather than a constant so the test has
 * something to hold. A constant array would let the next conditional be written
 * at the call site instead, where nothing is watching.
 */
export function paneControls(index: number, panelHeader: boolean): PaneControl[] {
  const menu: PaneControl = { id: 'menu', testid: `pane-menu-${index}` }
  if (!panelHeader) return [menu]
  return [menu, { id: 'collapse', testid: 'panel-collapse' }]
}

/**
 * Everything in a pane's strip a keyboard reaches, in the order it reaches it:
 * the tabs left to right, then the controls.
 *
 * Only the selected tab is in the tab order — that is what a tablist is, and
 * arrow keys move within it. See tabFromKey().
 */
export function panelFocusOrder(_width: number, tab: PanelTab): string[] {
  return [`panel-tab-${tab}`, ...paneControls(0, true).map((c) => c.testid)]
}

/**
 * Arrow-key navigation within the tab strip.
 *
 * Wrapping, because a row of tabs with a stop at each end is a control
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
