

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

// Two, and they are the only two things the side panel is *for*: the files in
// front of you, and what you are thinking about them.
//
// It was six, then four, and four was 「特别迷惑」. The mistake in both was
// treating a tab as a place to put a data source. `monitor` and `tokens` are
// not places you go — they are things you want in the corner of your eye while
// reading a terminal, which is the argument the monitor strip was already
// built on and which was then contradicted by giving the monitor a tab as
// well. So they are not tabs: they are the bottom half of both tabs, the same
// two blocks in the same place whichever one you are on, and pressing either
// opens it (see PanelDock).
//
// The strip is therefore two icons and a movement between them, which is a
// thing you can learn in one glance rather than four.
export const PANEL_TABS = ['files', 'notes'] as const

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
 *
 * `vnc` stays on this list although the feature behind it is gone entirely --
 * the proxy, the flag, the settings page and the table, not only the tab. That
 * is the reason it stays rather than a reason to take it off: what this list
 * names is a string sitting in somebody's browser today, and a saved layout
 * naming a tab that will never come back is exactly the layout the repair path
 * has to handle. Nothing is ever removed from here; it only grows.
 */
export const RETIRED_TABS = ['git', 'todos', 'vnc', 'monitor', 'tokens'] as const

/**
 * The tabs whose body divides the pane's height itself.
 *
 * Every tab, now — which is why this is still a list rather than a `true`. A
 * stacked tab is two panels and a divider, so its height is the pane's height
 * and not the height of its content: the pane gives it `h-full` rather than
 * `min-h-full`, because a box whose height is its content's leaves the flex
 * column inside it nothing to divide and it collapses to its two headers.
 *
 * Kept as a list because "all of them" is a coincidence of there being two.
 * The next tab that is not a stack would otherwise be a `true` somebody has to
 * turn back into a condition, at the call site, where nothing is watching.
 */
export const STACKED_TABS = ['files', 'notes'] as const

export function tabOwnsHeight(tab: PanelTab): boolean {
  return (STACKED_TABS as readonly string[]).includes(tab)
}

/**
 * What sits in the bottom half of every tab, in order.
 *
 * The same two blocks on the files tab and on the notes tab, in the same
 * place, at the same size. That is the whole idea: "is the machine coping" and
 * "what is this costing" are questions you have *while* reading something
 * else, so they are never somewhere you navigate to. Token spend above the
 * monitor because it is the one that was asked for
 * (「在现有的监控面板上面加一个token用量」) and because the monitor already has
 * a permanent home in the strip below the panel.
 */
export const DOCK_BLOCKS = ['tokens', 'monitor'] as const

export type DockBlock = (typeof DOCK_BLOCKS)[number]

/**
 * Everything that can be opened out of its compact form, in one list.
 *
 * The two dock blocks plus the repository, which is not in the dock: its
 * compact form is a line in the file tree's own header, because a repository
 * is a fact about the directory above it rather than about the machine. What
 * it shares with the other two is the *gesture* — press the compact form, get
 * the whole side panel; press again, get the window — and that gesture is one
 * component (PanelDetail) reading this list rather than three components that
 * each grew their own.
 *
 * Three states and no more. A block that is expanded is expanded *instead of*
 * the stack, not beside it: two of these open at once is the four-tab panel's
 * mistake in a smaller box.
 */
export const DETAIL_BLOCKS = ['repo', 'tokens', 'monitor'] as const

export type DetailBlock = (typeof DETAIL_BLOCKS)[number]

/** Every block in the dock is openable; not every openable block is in the dock. */
export function isDetailBlock(v: string): v is DetailBlock {
  return (DETAIL_BLOCKS as readonly string[]).includes(v)
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
