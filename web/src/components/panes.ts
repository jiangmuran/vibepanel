import { PANEL_TABS, type PanelTab } from './chrome'

/**
 * How the side panel is divided into panes, and where that division lives.
 *
 * The panel used to be one tab at a time plus a single boolean called `split`
 * that meant "notes and todo, stacked". This is that idea taken seriously: a
 * vertical stack of groups, each with its own tab strip, each holding one or
 * more of them.
 *
 * Three decisions are load-bearing and worth stating before the code.
 *
 * **Every tab lives in exactly one group, and every tab lives somewhere.** The
 * union of the groups is always PANEL_TABS — no more, no fewer. That single
 * invariant is what makes the whole thing safe: a tab cannot be dragged into
 * oblivion, a stored layout missing one cannot hide it, and "which pane holds
 * the file tree" always has an answer. It also settles a question that would
 * otherwise need a much larger model: there is no way to have two file trees,
 * because a tab is in one place. Each pane therefore has its own state — its
 * own scroll position, its own directory, its own filter — for free, by being
 * a different tab rather than a second copy of one.
 *
 * **This is not tmux.** Nothing here reaches the server or the tmux server;
 * these are boxes in a browser. Red line 2 is about processes, and no process
 * anywhere depends on any of this.
 *
 * **The layout is the viewer's, and it does not travel.** It goes to
 * localStorage under a key that includes a viewport-size band, so a layout
 * built on a 4K monitor is not the one a laptop opens, and a phone inherits
 * nothing. Nothing about it is sent to the server, so two devices signed into
 * the same panel keep their own arrangements — which is the point.
 *
 * (The brief for this work said the panel width and split ratio were already
 * stored per viewport band and to follow that precedent. They were not: both
 * were plain unbucketed keys and the split ratio was not persisted at all.
 * This is where the bucketing starts.)
 */

export const PANE_LAYOUT_VERSION = 1

/** One group per tab is as far as it can go, because a group needs a tab. */
export const MAX_PANES = PANEL_TABS.length

/**
 * A pane's floor, in pixels: the 40px strip plus a body worth looking at.
 *
 * Used to decide whether a stored layout fits the window it is being opened
 * in. Four panes in a 300px-tall browser window is four tab strips and no
 * content, which is not a layout, it is a stack of headings.
 */
export const PANE_MIN_HEIGHT = 132

/** No pane smaller than this share of the column, however hard you drag. */
export const PANE_MIN_RATIO = 0.1

export interface PaneGroup {
  /** At least one, unique across the whole layout. */
  tabs: PanelTab[]
  /** One of `tabs`. */
  active: PanelTab
  /** Share of the column. The sizes of a layout sum to 1. */
  size: number
}

export interface PaneLayout {
  version: typeof PANE_LAYOUT_VERSION
  groups: PaneGroup[]
}

/** Where a dragged tab would land. */
export type DropTarget =
  /** Into this group's tab strip. */
  | { kind: 'join'; group: number }
  /** Into a new group of its own, inserted at this index. */
  | { kind: 'new'; at: number }

/** The three bands a pane offers while something is being dragged over it. */
export const DROP_KINDS = ['before', 'join', 'after'] as const
export type DropKind = (typeof DROP_KINDS)[number]

/**
 * What a drop zone in the DOM means, as a target.
 *
 * The zones carry `data-drop` and `data-group` and are found with
 * elementFromPoint, so both values arrive as strings out of the document —
 * which is to say from the same place a stored layout arrives from, and
 * deserving the same suspicion. A band this build does not know, or a group
 * index that is not one, is not a drop.
 */
export function dropTargetFrom(kind: string | null, group: string | null): DropTarget | null {
  // Explicitly, before Number() gets hold of it: `Number(null)` and `Number('')`
  // are both 0, so a missing attribute would arrive as a confident instruction
  // to drop into the first pane.
  if (group === null || group.trim() === '') return null
  const at = Number(group)
  if (!Number.isInteger(at) || at < 0) return null
  if (kind === 'join') return { kind: 'join', group: at }
  if (kind === 'before') return { kind: 'new', at }
  if (kind === 'after') return { kind: 'new', at: at + 1 }
  return null
}

/**
 * Which of the three bands a point falls in.
 *
 * Measured against the pane's *body*, not the pane. The tab strip sits at the
 * top of a pane, so bands over the whole thing put the strip inside the "new
 * pane above" third — and a fourteen-pixel sideways wiggle on a tab, which is
 * a clumsy click, split the panel in two. Found by driving it.
 *
 * A third at each end, because the middle is the common case — most drags are
 * "put this with those" — and the edges have to be reachable without aiming.
 */
export function dropKindAt(offsetY: number, height: number): DropKind {
  if (!Number.isFinite(height) || height <= 0) return 'join'
  // Above the body is the strip, which is the tabs: dropping there joins them.
  if (offsetY <= 0) return 'join'
  const share = offsetY / height
  if (share < 0.3) return 'before'
  if (share > 0.7) return 'after'
  return 'join'
}

export function defaultLayout(): PaneLayout {
  return {
    version: PANE_LAYOUT_VERSION,
    groups: [{ tabs: [...PANEL_TABS], active: PANEL_TABS[0], size: 1 }],
  }
}

function isTab(v: unknown): v is PanelTab {
  return typeof v === 'string' && (PANEL_TABS as readonly string[]).includes(v)
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

/**
 * Sizes that sum to one, with nothing squeezed out of existence.
 *
 * Any nonsense at all — a zero, a NaN, a negative, a share below the floor —
 * and the whole set is equalised rather than patched. Patching one bad value
 * against five good ones produces a layout nobody chose and nobody can explain;
 * even panes are at least a layout somebody can recognise as the fallback.
 */
function withSizes(groups: PaneGroup[]): PaneGroup[] {
  const even = 1 / groups.length
  const total = groups.reduce((sum, g) => sum + g.size, 0)
  const usable =
    Number.isFinite(total) &&
    total > 0 &&
    groups.every((g) => Number.isFinite(g.size) && g.size / total >= PANE_MIN_RATIO - 1e-9)
  return groups.map((g) => ({ ...g, size: usable ? g.size / total : even }))
}

/**
 * A layout out of whatever came back from localStorage.
 *
 * Total: every path returns a usable layout and none of them throws. What is
 * in that key is a string a person can edit, a build from six months ago can
 * have written, or a different build can have written under the same name — so
 * "it parsed" proves nothing about it.
 *
 * The repairs, in order, and each one is a real failure mode:
 *   - not an object, or a version this build does not know  -> the default
 *   - a tab name this build does not have                   -> dropped
 *   - the same tab in two groups                            -> kept in the first
 *   - a group with nothing left in it                       -> dropped
 *   - an `active` that is not in the group                  -> the first tab
 *   - more groups than there are tabs                       -> the tail dropped
 *   - a tab this build has and the layout does not          -> appended
 *   - sizes that are not shares of anything                 -> equalised
 *
 * The seventh is the one that matters most and is the least obvious: a tab
 * missing from the stored layout is a tab with no strip anywhere, and therefore
 * a panel with no way to reach the file tree at all.
 */
export function parseLayout(raw: unknown): PaneLayout {
  if (!isRecord(raw)) return defaultLayout()
  if (raw.version !== PANE_LAYOUT_VERSION) return defaultLayout()
  if (!Array.isArray(raw.groups)) return defaultLayout()

  const seen = new Set<PanelTab>()
  const groups: PaneGroup[] = []
  for (const g of raw.groups) {
    if (groups.length >= MAX_PANES) break
    if (!isRecord(g)) continue
    const tabs: PanelTab[] = []
    if (Array.isArray(g.tabs)) {
      for (const t of g.tabs) {
        if (!isTab(t) || seen.has(t)) continue
        seen.add(t)
        tabs.push(t)
      }
    }
    if (tabs.length === 0) continue
    const active = isTab(g.active) && tabs.includes(g.active) ? g.active : tabs[0]
    const size = typeof g.size === 'number' ? g.size : Number.NaN
    groups.push({ tabs, active, size })
  }
  if (groups.length === 0) return defaultLayout()

  const missing = PANEL_TABS.filter((t) => !seen.has(t))
  if (missing.length > 0) groups[groups.length - 1].tabs.push(...missing)

  return { version: PANE_LAYOUT_VERSION, groups: withSizes(groups) }
}

/** The same, from the string localStorage actually hands back. */
export function readLayout(json: string | null): PaneLayout {
  if (!json) return defaultLayout()
  try {
    return parseLayout(JSON.parse(json))
  } catch {
    // Truncated, or somebody's editor put a BOM in it. Not a crash.
    return defaultLayout()
  }
}

export function serialiseLayout(layout: PaneLayout): string {
  return JSON.stringify(layout)
}

/**
 * Viewport bands, coarse on purpose.
 *
 * The key has to change when the *screen* changes and not when the window is
 * nudged, so these are wide: a laptop stays a laptop while it is resized, and
 * moving the window to a 4K display crosses a band. Exact pixels as a key would
 * make every layout single-use.
 *
 * Height as well as width. A window on a 1440-wide monitor is a different
 * proposition full-screen and half-height, and panes are stacked vertically —
 * height is the dimension this feature spends.
 */
const WIDTH_BANDS = [0, 640, 900, 1200, 1440, 1800, 2400, 3200]
const HEIGHT_BANDS = [0, 600, 800, 1000, 1300, 1700, 2200]

function band(value: number, bands: number[]): number {
  if (!Number.isFinite(value) || value <= 0) return bands[0]
  let found = bands[0]
  for (const b of bands) if (value >= b) found = b
  return found
}

/**
 * Where this viewer's layout for this screen lives.
 *
 * localStorage, never the server: "I changed monitor and my layout followed me"
 * is the failure being avoided, and so is "I opened it on my phone and got the
 * desktop's three panes".
 */
export function layoutStorageKey(width: number, height: number): string {
  return `vibepanel.panes.${band(width, WIDTH_BANDS)}x${band(height, HEIGHT_BANDS)}`
}

export function groupOf(layout: PaneLayout, tab: PanelTab): number {
  return layout.groups.findIndex((g) => g.tabs.includes(tab))
}

/** The tab a group is showing, or the panel's own idea of "where I am". */
export function activeTab(layout: PaneLayout, group: number): PanelTab {
  return layout.groups[group]?.active ?? layout.groups[0].active
}

/** Bring a tab to the front of whichever pane holds it. */
export function activate(layout: PaneLayout, tab: PanelTab): PaneLayout {
  const at = groupOf(layout, tab)
  if (at < 0 || layout.groups[at].active === tab) return layout
  return {
    ...layout,
    groups: layout.groups.map((g, i) => (i === at ? { ...g, active: tab } : g)),
  }
}

/**
 * Move a tab to where it was dropped.
 *
 * Returns the same object when the drop is a no-op, so a released drag that
 * went nowhere does not rewrite storage or remount four panels.
 */
export function moveTab(layout: PaneLayout, tab: PanelTab, target: DropTarget): PaneLayout {
  const from = groupOf(layout, tab)
  if (from < 0) return layout

  const alone = layout.groups[from].tabs.length === 1
  if (target.kind === 'join' && target.group === from) return layout
  // Already a pane of its own, in the position being asked for. `at` counts
  // insertion points in the list as it stands, so both the slot before this
  // group and the slot after it name where it already is.
  if (target.kind === 'new' && alone && (target.at === from || target.at === from + 1)) {
    return layout
  }

  const groups = layout.groups.map((g) => ({ ...g, tabs: g.tabs.filter((t) => t !== tab) }))
  let removed = -1
  if (alone) {
    groups.splice(from, 1)
    removed = from
  } else if (groups[from].active === tab) {
    // The pane it left has to be showing something.
    groups[from].active = groups[from].tabs[0]
  }

  if (target.kind === 'join') {
    // Every index after the removed group has moved up one.
    const g = removed >= 0 && target.group > removed ? target.group - 1 : target.group
    const into = groups[g]
    if (!into) return layout
    into.tabs = [...into.tabs, tab]
    into.active = tab
  } else {
    const at = removed >= 0 && target.at > removed ? target.at - 1 : target.at
    const share = 1 / (groups.length + 1)
    for (const g of groups) g.size *= 1 - share
    groups.splice(Math.max(0, Math.min(at, groups.length)), 0, {
      tabs: [tab],
      active: tab,
      size: share,
    })
  }

  return { version: PANE_LAYOUT_VERSION, groups: withSizes(groups) }
}

/**
 * The same move, without a pointer.
 *
 * Dragging is a mouse gesture, and the panel has to be usable without one —
 * from the keyboard, and from the pane menu, which is what a touch screen gets.
 * One tab towards the next pane in that direction, or into a new pane of its
 * own if there is not one.
 */
export function moveTowards(layout: PaneLayout, tab: PanelTab, dir: 'up' | 'down'): PaneLayout {
  const from = groupOf(layout, tab)
  if (from < 0) return layout
  const alone = layout.groups[from].tabs.length === 1
  const next = dir === 'up' ? from - 1 : from + 1
  if (next >= 0 && next < layout.groups.length) {
    return moveTab(layout, tab, { kind: 'join', group: next })
  }
  // Off the end. A tab that is already a pane on its own at the end has
  // nowhere further to go, and moveTab says so by returning the same layout.
  if (alone) return layout
  return moveTab(layout, tab, { kind: 'new', at: dir === 'up' ? from : from + 1 })
}

/** Fold a pane's tabs into its neighbour. The one control that undoes a split. */
export function mergeGroup(layout: PaneLayout, index: number, dir: 'up' | 'down'): PaneLayout {
  const into = dir === 'up' ? index - 1 : index + 1
  if (index < 0 || index >= layout.groups.length) return layout
  if (into < 0 || into >= layout.groups.length) return layout
  const from = layout.groups[index]
  const groups = layout.groups.map((g) => ({ ...g, tabs: [...g.tabs] }))
  groups[into].tabs.push(...from.tabs)
  groups[into].active = from.active
  groups[into].size += from.size
  groups.splice(index, 1)
  return { version: PANE_LAYOUT_VERSION, groups: withSizes(groups) }
}

/**
 * Drag the boundary between pane `index` and the one below it.
 *
 * `ratio` is where the boundary sits as a share of the whole column, so the
 * two panes on either side of it take what is left of their combined share and
 * nothing else moves. Dragging one divider must not shuffle the panes three
 * rows away, which is what renormalising everything would do.
 */
export function resizeAt(layout: PaneLayout, index: number, ratio: number): PaneLayout {
  const below = index + 1
  if (index < 0 || below >= layout.groups.length) return layout
  const before = layout.groups.slice(0, index).reduce((s, g) => s + g.size, 0)
  const pair = layout.groups[index].size + layout.groups[below].size
  if (pair <= 0) return layout
  const top = Math.max(
    PANE_MIN_RATIO,
    Math.min(ratio - before, pair - PANE_MIN_RATIO),
  )
  const groups = layout.groups.map((g, i) => {
    if (i === index) return { ...g, size: top }
    if (i === below) return { ...g, size: pair - top }
    return g
  })
  return { version: PANE_LAYOUT_VERSION, groups }
}

export function resetLayout(): PaneLayout {
  return defaultLayout()
}

/** Notes and todo, in two panes, both on screen. What `split` used to mean. */
export function notesTodosSplit(layout: PaneLayout): boolean {
  const notes = groupOf(layout, 'notes')
  const todos = groupOf(layout, 'todos')
  if (notes < 0 || todos < 0 || notes === todos) return false
  return (
    layout.groups[notes].active === 'notes' && layout.groups[todos].active === 'todos'
  )
}

/**
 * The one-press version of the arrangement people actually want.
 *
 * Kept as its own control because it is the answer to "what you are thinking
 * and what you have left, at the same time", and asking somebody to build that
 * by dragging twice is asking them to do the interface's job.
 */
export function toggleNotesTodos(layout: PaneLayout): PaneLayout {
  if (notesTodosSplit(layout)) {
    const notes = groupOf(layout, 'notes')
    const todos = groupOf(layout, 'todos')
    return mergeGroup(layout, todos, todos > notes ? 'up' : 'down')
  }
  let next = activate(layout, 'notes')
  const notesAt = groupOf(next, 'notes')
  if (next.groups[notesAt].tabs.length > 1) {
    next = moveTab(next, 'notes', { kind: 'new', at: notesAt + 1 })
  }
  const at = groupOf(next, 'notes')
  next = moveTab(next, 'todos', { kind: 'new', at: at + 1 })
  return activate(activate(next, 'notes'), 'todos')
}

/**
 * A layout that fits the window it is being opened in.
 *
 * A stored layout is not a promise about the screen it comes back on: a browser
 * window dragged shorter, a laptop lid on a different machine, a band boundary
 * crossed by one pixel. Panes are merged from the bottom until the rest have
 * room, because the top of the column is where the panel opens.
 *
 * `available <= 0` means nothing has been laid out yet — a panel that has not
 * been measured, a test environment with no layout at all — and returning the
 * layout untouched is the only safe answer. Treating "unmeasured" as "no room"
 * would collapse everybody's layout on the frame before the first paint.
 */
export function fitTo(layout: PaneLayout, available: number): PaneLayout {
  if (!Number.isFinite(available) || available <= 0) return layout
  let next = layout
  while (next.groups.length > 1 && next.groups.length * PANE_MIN_HEIGHT > available) {
    next = mergeGroup(next, next.groups.length - 1, 'up')
  }
  return next
}

/**
 * Alt plus an arrow moves the focused tab between panes.
 *
 * Alt because the bare arrows already move between tabs (tabFromKey) and the
 * panel below the strip has its own keys. Returns null for everything else, so
 * the handler knows not to swallow the event.
 */
export function paneKeyCommand(key: string, alt: boolean): 'up' | 'down' | null {
  if (!alt) return null
  if (key === 'ArrowUp') return 'up'
  if (key === 'ArrowDown') return 'down'
  return null
}
