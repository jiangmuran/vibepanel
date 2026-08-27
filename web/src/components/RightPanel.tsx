import { Fragment, useCallback, useEffect, useRef, useState } from 'react'
import {
  Activity,
  ArrowDown,
  ArrowUp,
  ChevronRight,
  Coins,
  EllipsisVertical,
  FolderTree,
  GitBranch,
  ListChecks,
  Merge,
  NotebookPen,
  RotateCcw,
  Rows2,
  Square,
} from 'lucide-react'

import type { Project, Session } from '../protocol/wire'
import type { PanelSocket } from '../protocol/socket'
import { FileTree } from './panels/FileTree'
import { GitPanel } from './panels/GitPanel'
import { SystemMonitor } from './panels/SystemMonitor'
import { Notes } from './panels/Notes'
import { Todos } from './panels/Todos'
import { ErrorBoundary } from './ErrorBoundary'
import { SystemStrip } from './panels/SystemStrip'
import { TokenUsage } from './panels/TokenUsage'
import {
  PANEL_MAX_WIDTH,
  PANEL_MIN_WIDTH,
  PANEL_TABS,
  clampPanelWidth,
  paneControls,
  paneLabelled,
  resizeStep,
  swapDirection,
  tabFromKey,
  type PanelTab,
} from './chrome'
import {
  DROP_KINDS,
  activate,
  dropKindAt,
  dropTargetFrom,
  fitTo,
  mergeGroup,
  moveTab,
  moveTowards,
  notesTodosSplit,
  paneKeyCommand,
  resetLayout,
  resizeAt,
  toggleNotesTodos,
  type DropKind,
  type DropTarget,
  type PaneGroup,
  type PaneLayout,
} from './panes'
import { t, useLang, type Key } from '../i18n'

export type { PanelTab }

// The label is a key, not a string: resolving it at render is what makes a
// language switch repaint the tabs instead of needing a reload.
//
// Keyed by tab and mapped over PANEL_TABS rather than being its own array, so
// the order on screen is the order chrome.ts navigates and animates in. Two
// lists in two files that have to agree is how a left arrow ends up moving
// right.
const TABS: Record<PanelTab, { icon: typeof Activity; key: Key }> = {
  files: { icon: FolderTree, key: 'panel.files' },
  git: { icon: GitBranch, key: 'panel.git' },
  monitor: { icon: Activity, key: 'panel.monitor' },
  notes: { icon: NotebookPen, key: 'panel.notes' },
  todos: { icon: ListChecks, key: 'panel.todos' },
  tokens: { icon: Coins, key: 'panel.tokens' },
}

const DROP_LABEL: Record<DropKind, Key> = {
  before: 'pane.dropBefore',
  join: 'pane.dropJoin',
  after: 'pane.dropAfter',
}

interface Props {
  project: Project | null
  /** Every session, so the monitor can name what it is measuring. */
  sessions: Session[]
  /** Needed by the notes and todo panels so they hear about other viewers. */
  socket: PanelSocket
  /** How the column is divided. Owned and persisted by App; see panes.ts. */
  layout: PaneLayout
  onLayout: (next: PaneLayout) => void
  /** Hand the keyboard back to the terminal after a pointer chose a tab. */
  onRefocus: () => void
  width: number
  onWidthChange: (px: number) => void
  onCollapse: () => void
  /** Opens the full-width token view. The panel is too narrow to hold it. */
  onOpenTokens: () => void
}

/** Pixels of movement before a press on a tab becomes a drag. */
const DRAG_THRESHOLD = 5

interface DragState {
  tab: PanelTab
  target: DropTarget | null
  /** Which pane and band the pointer is over, for drawing. */
  over: { group: number; kind: DropKind } | null
}

export function RightPanel(props: Props) {
  const { project, layout, width } = props
  useLang()

  const dragFrom = useRef<{ x: number; width: number } | null>(null)
  const [widthDragging, setWidthDragging] = useState(false)
  const columnRef = useRef<HTMLDivElement | null>(null)
  const [menuOpen, setMenuOpen] = useState<number | null>(null)

  // The drag, twice: once as state so it can be drawn, once in a ref so the
  // release handler can read it. A pointerup can arrive before React has
  // flushed the pointermove just before it — a flick is exactly when those two
  // land together — and committing the target from one move ago drops the tab
  // in the wrong pane. Same reason useDragList keeps a `live` ref.
  const [drag, setDragState] = useState<DragState | null>(null)
  const dragLive = useRef<DragState | null>(null)
  const press = useRef<{ tab: PanelTab; x: number; y: number } | null>(null)
  const suppressClick = useRef(false)
  const setDrag = useCallback((next: DragState | null) => {
    dragLive.current = next
    setDragState(next)
  }, [])

  const focused = layout.groups[0].active
  const split = notesTodosSplit(layout)

  // A stored layout is not a promise about the screen it comes back on. Four
  // panes in a browser window dragged short is four tab strips and no content,
  // so panes are merged from the bottom until the rest have room. fitTo
  // returns the same object when nothing needs doing, which is what keeps this
  // from being a loop.
  const { onLayout } = props
  useEffect(() => {
    const el = columnRef.current
    if (!el) return
    const check = () => {
      const next = fitTo(layout, el.clientHeight)
      if (next !== layout) onLayout(next)
    }
    check()
    const ro = new ResizeObserver(check)
    ro.observe(el)
    return () => ro.disconnect()
  }, [layout, onLayout])

  // Escape cancels, from anywhere. A drag you can only get out of by finding a
  // neutral place to release is a drag people abandon by dropping the tab
  // somewhere they did not want it.
  useEffect(() => {
    if (!drag) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setDrag(null)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [drag, setDrag])

  useEffect(() => {
    if (menuOpen === null) return
    const close = () => setMenuOpen(null)
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') close()
    }
    // pointerdown rather than click: a menu that stays open until mouseup is a
    // menu that swallows the first press of whatever you meant to do next.
    window.addEventListener('pointerdown', close)
    window.addEventListener('keydown', onKey)
    return () => {
      window.removeEventListener('pointerdown', close)
      window.removeEventListener('keydown', onKey)
    }
  }, [menuOpen])

  /**
   * Which pane and band a point falls in.
   *
   * Read off the DOM at the moment of the move rather than out of a map the
   * panes keep up to date: the panes are the thing being rearranged, so a
   * registry of them is one more thing that can be stale exactly when it is
   * being used. `data-pane-index` is on the section that draws the pane, so
   * the rectangle and the index cannot disagree.
   *
   * Geometry rather than elementFromPoint, because the pointer is captured by
   * the tab being dragged and the bands drawn over each pane are deliberately
   * not hit-testable — they are there to be looked at.
   */
  const targetAt = useCallback((x: number, y: number) => {
    const column = columnRef.current
    if (!column) return null
    for (const el of column.querySelectorAll<HTMLElement>('[data-pane-index]')) {
      const r = el.getBoundingClientRect()
      if (r.height <= 0) continue
      if (x < r.left || x > r.right || y < r.top || y > r.bottom) continue
      const index = Number(el.dataset.paneIndex)
      // Against the body rather than the pane: the strip is above the body, so
      // an offset of zero or less is the strip, which dropKindAt reads as
      // "join these tabs".
      const body = el.querySelector<HTMLElement>('[data-pane-body]')?.getBoundingClientRect() ?? r
      const kind = dropKindAt(y - body.top, body.height)
      const target = dropTargetFrom(kind, el.dataset.paneIndex ?? null)
      return target ? { target, over: { group: index, kind } } : null
    }
    return null
  }, [])

  const onWidthStart = useCallback(
    (e: React.PointerEvent) => {
      e.preventDefault()
      e.currentTarget.setPointerCapture(e.pointerId)
      dragFrom.current = { x: e.clientX, width }
      setWidthDragging(true)
    },
    [width],
  )
  const onWidthMove = useCallback(
    (e: React.PointerEvent) => {
      const from = dragFrom.current
      if (!from) return
      // Dragging left widens the panel, so the delta is inverted.
      props.onWidthChange(clampPanelWidth(from.width + (from.x - e.clientX)))
    },
    [props],
  )
  const onWidthEnd = useCallback((e: React.PointerEvent) => {
    if (e.currentTarget.hasPointerCapture(e.pointerId)) {
      e.currentTarget.releasePointerCapture(e.pointerId)
    }
    dragFrom.current = null
    setWidthDragging(false)
  }, [])

  const tabDrag = {
    onPointerDown: (e: React.PointerEvent, tab: PanelTab) => {
      // Left button or touch. A right-click drag is not a rearrangement.
      if (e.button !== 0) return
      // Whatever the last gesture left behind. A drag released over something
      // that is not a tab produces no click for swallowClick() to eat, and a
      // flag left standing swallows the next real one instead.
      suppressClick.current = false
      e.currentTarget.setPointerCapture(e.pointerId)
      press.current = { tab, x: e.clientX, y: e.clientY }
    },
    onPointerMove: (e: React.PointerEvent) => {
      const from = press.current
      if (!from) return
      // A threshold, so a plain click on a tab is a plain click. Capturing on
      // pointerdown and calling that a drag paints every landing place on the
      // screen for the length of a mouse press.
      const moved = Math.abs(e.clientX - from.x) > DRAG_THRESHOLD ||
        Math.abs(e.clientY - from.y) > DRAG_THRESHOLD
      if (!moved && !dragLive.current) return
      const found = targetAt(e.clientX, e.clientY)
      setDrag({ tab: from.tab, target: found?.target ?? null, over: found?.over ?? null })
    },
    onPointerUp: (e: React.PointerEvent) => {
      if (e.currentTarget.hasPointerCapture(e.pointerId)) {
        e.currentTarget.releasePointerCapture(e.pointerId)
      }
      press.current = null
      const live = dragLive.current
      setDrag(null)
      if (!live) return
      // The click that a release still fires belongs to the drag, not to the
      // tab under it. Without this the click handler runs after the drop, with
      // the layout as it was before the move, and puts the tab straight back.
      suppressClick.current = true
      if (!live.target) return
      const next = moveTab(layout, live.tab, live.target)
      // moveTab returns the same object when the drop changes nothing, which
      // is what stops a drag that went nowhere from rewriting storage and
      // remounting every panel.
      if (next !== layout) props.onLayout(next)
    },
    onPointerCancel: () => {
      press.current = null
      setDrag(null)
    },
    /** True once for the click a completed drag is about to produce. */
    swallowClick: () => {
      if (!suppressClick.current) return false
      suppressClick.current = false
      return true
    },
  }

  const bodyFor = (tab: PanelTab) => {
    // Before the no-project guard, and the only tab that is. The others
    // are all *about* a project — its files, its notes, its sessions' load —
    // and have nothing to say without one. Token spend is a fact about the
    // machine: an agent that ran in a directory the panel has never been told
    // about still spent it, and hiding the whole tab until somebody adds a
    // project would hide exactly that case.
    if (tab === 'tokens') {
      return <TokenUsage projectId={project?.id ?? null} onOpen={props.onOpenTokens} />
    }
    if (!project) {
      return <p className="px-3 py-4 text-vp-base text-ink-2">{t('panel.noProject')}</p>
    }
    if (tab === 'files') return <FileTree key={project.id} projectId={project.id} />
    if (tab === 'git') {
      return <GitPanel key={project.id} projectId={project.id} sessions={props.sessions} />
    }
    if (tab === 'monitor') return <SystemMonitor sessions={props.sessions} />
    if (tab === 'notes') return <Notes key={project.id} projectId={project.id} socket={props.socket} />
    return <Todos key={project.id} projectId={project.id} socket={props.socket} />
  }

  return (
    <aside
      data-testid="right-panel"
      data-tab={focused}
      data-split={split}
      data-panes={layout.groups.length}
      data-dragging={drag ? drag.tab : undefined}
      // It arrives from the edge it lives on. Opening a panel is a reveal, and
      // the reveal is what makes the collapse control's chevron read as a
      // direction rather than a decoration — you saw where it came from, so
      // you know where it goes.
      className="vp-edge-in-right flex shrink-0 border-l border-hairline vp-blur"
      style={{ width }}
    >
      <div
        data-testid="panel-resize"
        role="separator"
        aria-orientation="vertical"
        aria-valuenow={width}
        aria-valuemin={PANEL_MIN_WIDTH}
        aria-valuemax={PANEL_MAX_WIDTH}
        tabIndex={0}
        data-dragging={widthDragging}
        onPointerDown={onWidthStart}
        onPointerMove={onWidthMove}
        onPointerUp={onWidthEnd}
        onPointerCancel={onWidthEnd}
        onKeyDown={(e) => {
          const step = resizeStep(e.key, e.shiftKey)
          if (step === null) return
          // Without this the arrow scrolls the panel behind the divider, and
          // the divider is inside a scroll container.
          e.preventDefault()
          props.onWidthChange(clampPanelWidth(width + step))
        }}
        title={t('panel.resize')}
        // `relative z-10` is what makes the grip hittable, not decoration.
        //
        // The negative margin pulls the panel's content four pixels left so
        // the eight-pixel grip straddles the border rather than sitting beside
        // it. The content is the later sibling, so without a stacking order it
        // paints over the half it overlaps — and that is the half on the
        // visible edge, which is where anyone aims. Measured with
        // elementFromPoint across the grip: offsets 0-3 hit it, 4-7 hit the
        // content. Half the target, and the wrong half.
        className="vp-grip z-10 -mr-1 w-2 cursor-col-resize"
      />

      <div className="flex min-w-0 flex-1 flex-col">
        {/* The panes, and only the panes. The monitor strip below is a sibling
            rather than the last child, because a divider's position is read as
            a share of this box — and a box that included the strip would put
            every boundary a strip's height out. */}
        <div ref={columnRef} className="flex min-h-0 flex-1 flex-col">
        {layout.groups.map((group, index) => (
          // Keyed by the tabs it holds, which is stable while the pane is
          // resized or its tab switched and changes only when the arrangement
          // does. Tab sets are disjoint by the invariant, so this is unique.
          <Fragment key={group.tabs.join('|')}>
            {index > 0 && (
              <PaneDivider
                index={index - 1}
                layout={layout}
                columnRef={columnRef}
                onLayout={props.onLayout}
              />
            )}
            <Pane
              index={index}
              group={group}
              layout={layout}
              width={width}
              drag={drag}
              tabDrag={tabDrag}
              menuOpen={menuOpen === index}
              onMenu={(open) => setMenuOpen(open ? index : null)}
              onLayout={props.onLayout}
              onRefocus={props.onRefocus}
              onCollapse={props.onCollapse}
              body={bodyFor}
            />
          </Fragment>
        ))}
        </div>

        {/* Always on, below whatever is chosen above. "Is the machine coping"
            is not a question you navigate to -- it is a thing you want in the
            corner of your eye while reading a terminal. As a tab it was three
            figures given a whole column, and invisible from the other three.

            Suppressed only when a pane is already showing the full monitor,
            which is the same rule as before, asked of every pane rather than
            of the one tab there used to be. */}
        {layout.groups.every((g) => g.active !== 'monitor') && (
          <ErrorBoundary label="The monitor strip">
            <SystemStrip />
          </ErrorBoundary>
        )}
      </div>
    </aside>
  )
}

/** The boundary between two panes. Same grip as the panel's own edge. */
function PaneDivider({
  index,
  layout,
  columnRef,
  onLayout,
}: {
  index: number
  layout: PaneLayout
  columnRef: React.RefObject<HTMLDivElement | null>
  onLayout: (next: PaneLayout) => void
}) {
  const [dragging, setDragging] = useState(false)
  const ratioAt = (clientY: number) => {
    const box = columnRef.current?.getBoundingClientRect()
    if (!box || box.height === 0) return null
    return (clientY - box.top) / box.height
  }
  return (
    <div
      data-testid={`pane-resize-${index}`}
      role="separator"
      aria-orientation="horizontal"
      tabIndex={0}
      data-dragging={dragging}
      onPointerDown={(e) => {
        e.preventDefault()
        e.currentTarget.setPointerCapture(e.pointerId)
        setDragging(true)
      }}
      onPointerMove={(e) => {
        if (!dragging) return
        const ratio = ratioAt(e.clientY)
        if (ratio === null) return
        onLayout(resizeAt(layout, index, ratio))
      }}
      onPointerUp={(e) => {
        if (e.currentTarget.hasPointerCapture(e.pointerId)) {
          e.currentTarget.releasePointerCapture(e.pointerId)
        }
        setDragging(false)
      }}
      onKeyDown={(e) => {
        const step = resizeStep(e.key, e.shiftKey)
        if (step === null) return
        const box = columnRef.current?.getBoundingClientRect()
        if (!box || box.height === 0) return
        e.preventDefault()
        const before = layout.groups.slice(0, index + 1).reduce((s, g) => s + g.size, 0)
        onLayout(resizeAt(layout, index, before - step / box.height))
      }}
      title={t('panel.resize')}
      className="vp-grip h-2 cursor-row-resize border-y border-hairline"
    />
  )
}

interface PaneProps {
  index: number
  group: PaneGroup
  layout: PaneLayout
  width: number
  drag: DragState | null
  tabDrag: {
    onPointerDown: (e: React.PointerEvent, tab: PanelTab) => void
    onPointerMove: (e: React.PointerEvent) => void
    onPointerUp: (e: React.PointerEvent) => void
    onPointerCancel: () => void
    swallowClick: () => boolean
  }
  menuOpen: boolean
  onMenu: (open: boolean) => void
  onLayout: (next: PaneLayout) => void
  onRefocus: () => void
  onCollapse: () => void
  body: (tab: PanelTab) => React.ReactNode
}

function Pane(props: PaneProps) {
  const { index, group, layout, width, drag } = props
  const lang = useLang()
  const tab = group.active
  const solo = group.tabs.length === 1
  const labelled = paneLabelled(width, group.tabs.length)
  const panelHeader = index === 0

  // Where the marker sits, measured rather than computed.
  //
  // The tabs are not equal widths — the selected one grows to hold its name —
  // so there is no arithmetic that gets this right, and a marker a few pixels
  // out is worse than none. offsetLeft is relative to the track, which is the
  // marker's containing block.
  //
  // A plain effect, and it was written here as useLayoutEffect first with a
  // confident comment about a property that changes without an intervening
  // paint not transitioning. Measured both ways: they animate identically,
  // because the marker's previous position was painted several frames ago and
  // that is what the transition starts from. The claim was wrong, and a
  // comment nobody can reproduce is worse than none. This is a plain effect
  // because geometry read after paint is the cheaper of two equal options.
  const trackRef = useRef<HTMLDivElement | null>(null)
  const [marker, setMarker] = useState<{ left: number; width: number } | null>(null)
  useEffect(() => {
    const track = trackRef.current
    const el = track?.querySelector<HTMLElement>('[aria-selected="true"]')
    if (!track || !el || solo) {
      setMarker(null)
      return
    }
    const measure = () => {
      // A zero-width box is a pane nothing has laid out yet — an ancestor
      // still display:none, a headless probe. Leaving the marker unset is what
      // hands the selection back to the tab's own background.
      if (el.offsetWidth <= 0) return
      setMarker({ left: el.offsetLeft, width: el.offsetWidth })
    }
    measure()
    // The label folds open over 260ms and the tab grows with it, so the final
    // geometry is not available at the moment the tab changes. Observing both
    // boxes is what makes the marker travel with the tab instead of to where
    // the tab used to be going.
    const ro = new ResizeObserver(measure)
    ro.observe(track)
    ro.observe(el)
    return () => ro.disconnect()
  }, [tab, width, lang, solo])

  // Which way the body enters. Remembered rather than recomputed each render,
  // because `data-dir` changing on a live element restarts its animation: with
  // the direction derived on the fly, every unrelated re-render after a
  // backwards switch replayed the slide — a socket message, a note saving.
  //
  // Adjusted during render, which is React's own answer to "state that depends
  // on a prop that changed"; the discarded pass never reaches the DOM.
  const [swap, setSwap] = useState<{ tab: PanelTab; dir: 'forward' | 'back' }>({
    tab,
    dir: 'forward',
  })
  if (swap.tab !== tab) setSwap({ tab, dir: swapDirection(swap.tab, tab) })

  const over = drag && drag.over?.group === index ? drag.over.kind : null

  return (
    <section
      data-testid={`pane-${index}`}
      data-pane-index={index}
      data-pane-tabs={group.tabs.join(' ')}
      className="relative flex min-h-0 flex-col"
      style={{ flexGrow: group.size, flexShrink: 1, flexBasis: 0 }}
    >
      <header
        data-testid={panelHeader ? 'panel-header' : `pane-header-${index}`}
        className="vp-chrome gap-1 overflow-hidden px-2"
      >
        {/* The tabs in a track, not as loose buttons. A ragged row of one
            label and four icons with the pane's controls floated off to the
            right reads as parts that happened to land near each other; a track
            reads as one thing you are choosing within, which is what it is.

            A pane holding a single tab drops the track and keeps the tab: a
            lone pill in a groove is a segmented control with one segment,
            which is a heading wearing the wrong clothes. */}
        <div
          ref={trackRef}
          role="tablist"
          aria-label={t('panel.tablist')}
          aria-orientation="horizontal"
          data-labelled={labelled}
          data-solo={solo}
          data-marker={marker ? 'on' : 'off'}
          className="vp-segmented flex-1"
        >
          {marker && (
            <span
              aria-hidden="true"
              className="vp-marker"
              style={{ width: marker.width, transform: `translateX(${marker.left}px)` }}
            />
          )}
          {PANEL_TABS.filter((id) => group.tabs.includes(id)).map((id) => {
            const { icon: Icon, key } = TABS[id]
            const label = t(key)
            return (
              <button
                key={id}
                type="button"
                role="tab"
                id={`panel-tab-${id}`}
                data-testid={`panel-tab-${id}`}
                aria-selected={tab === id}
                aria-controls={`pane-body-${index}`}
                // Roving: the strip is one stop in the page's tab order and
                // the arrows move inside it, which is what a tablist is. Five
                // stops on the way to the terminal is not navigation.
                tabIndex={tab === id ? 0 : -1}
                data-drag-source={drag?.tab === id}
                onClick={() => {
                  if (props.tabDrag.swallowClick()) return
                  props.onLayout(activate(layout, id))
                  props.onRefocus()
                }}
                onKeyDown={(e) => {
                  // Alt first: the bare arrows move between tabs and Alt moves
                  // the tab between panes, which is the whole non-pointer path
                  // into the layout.
                  const move = paneKeyCommand(e.key, e.altKey)
                  if (move) {
                    e.preventDefault()
                    props.onLayout(moveTowards(layout, id, move))
                    return
                  }
                  const next = tabFromKey(e.key, id)
                  if (!next || !group.tabs.includes(next)) return
                  e.preventDefault()
                  props.onLayout(activate(layout, next))
                  document.getElementById(`panel-tab-${next}`)?.focus()
                }}
                onPointerDown={(e) => props.tabDrag.onPointerDown(e, id)}
                onPointerMove={props.tabDrag.onPointerMove}
                onPointerUp={props.tabDrag.onPointerUp}
                onPointerCancel={props.tabDrag.onPointerCancel}
                title={label}
                className="vp-tab vp-tab-drag text-vp-sm"
              >
                <Icon size={13} className="shrink-0" />
                <span className="vp-tab-label">{label}</span>
              </button>
            )
          })}
        </div>

        <span className="vp-divider" aria-hidden="true" />

        {/* Rendered from the list rather than written out, so "which controls
            are in this strip" is one answer in one place a test can sweep. The
            menu is on every pane; the panel's own two are on the first, which
            is a structural rule rather than one that changes under a pointer.
            See paneControls(). */}
        {paneControls(index, panelHeader).map((control) => {
          if (control.id === 'menu') {
            return (
              <button
                key={control.id}
                type="button"
                data-testid={control.testid}
                aria-expanded={props.menuOpen}
                aria-haspopup="menu"
                onPointerDown={(e) => e.stopPropagation()}
                onClick={() => props.onMenu(!props.menuOpen)}
                title={t('pane.menu')}
                className="vp-control"
              >
                <EllipsisVertical size={14} />
              </button>
            )
          }
          if (control.id === 'split') {
            return (
              <button
                key={control.id}
                type="button"
                data-testid={control.testid}
                onClick={() => props.onLayout(toggleNotesTodos(layout))}
                aria-pressed={notesTodosSplit(layout)}
                title={t(notesTodosSplit(layout) ? 'panel.splitOff' : 'panel.splitOn')}
                className="vp-control"
              >
                {/* The current arrangement, by shape: one pane or two rows.
                    The accent tint says the same thing a second way, which is
                    the point — a pressed state carried by hue alone is
                    unreadable in a dark room at 2am. */}
                {notesTodosSplit(layout) ? <Rows2 size={14} /> : <Square size={14} />}
              </button>
            )
          }
          return (
            <button
              key={control.id}
              type="button"
              data-testid={control.testid}
              onClick={props.onCollapse}
              title={t('app.hidePanel')}
              className="vp-control"
            >
              <ChevronRight size={14} />
            </button>
          )
        })}
      </header>

      {props.menuOpen && (
        <PaneMenu
          index={index}
          layout={layout}
          tab={tab}
          onLayout={(next) => {
            props.onMenu(false)
            props.onLayout(next)
          }}
        />
      )}

      {/* Per-pane, so the panel's own chrome — the tabs, the width, the
          collapse control — survives whatever a tab does, and switching away
          from a broken one is still possible. Keyed by tab so the boundary
          resets when you move to another.

          overflow-x-clip because the swap slides ten pixels sideways as it
          arrives, and a scroll container whose child briefly overflows grows a
          horizontal scrollbar for the length of the animation. */}
      {/* The body in its own positioned box, and the drop bands measured
          against that box rather than against the pane. The strip is part of
          the pane and sits above the body, so bands over the whole pane put
          the tabs inside the "new pane above" third. */}
      <div data-pane-body className="relative min-h-0 flex-1">
        <div
          id={`pane-body-${index}`}
          role="tabpanel"
          aria-labelledby={`panel-tab-${tab}`}
          className="h-full overflow-x-clip overflow-y-auto border-t border-hairline"
        >
          <div key={tab} data-dir={swap.dir} className="vp-swap min-h-full">
            <ErrorBoundary label={`The ${tab} panel`}>
              {props.body(tab)}
            </ErrorBoundary>
          </div>
        </div>

        {/* Every landing place, drawn at once and only while something is in
            the air. A drop target that appears under the pointer and nowhere
            else is a guessing game: you find out where a tab would have gone
            by putting it there. */}
        {drag && (
          <div
            data-testid={`pane-drops-${index}`}
            aria-hidden="true"
            className="pointer-events-none absolute inset-0 z-20 flex flex-col"
          >
            {DROP_KINDS.map((kind) => (
              <div
                key={kind}
                data-drop={kind}
                data-over={over === kind}
                className="vp-drop flex items-center justify-center"
                style={{ flexGrow: kind === 'join' ? 4 : 3, flexBasis: 0 }}
              >
                {over === kind && (
                  <span className="rounded-full px-2 py-0.5 text-vp-sm vp-solid">
                    {t(DROP_LABEL[kind])}
                  </span>
                )}
              </div>
            ))}
          </div>
        )}
      </div>
    </section>
  )
}

/**
 * The pane's own menu: everything the drag does, without a pointer.
 *
 * Dragging is a mouse gesture. This is the same set of moves for a keyboard
 * and for a finger, and it is also where "put it back" lives — somebody who
 * has made a mess of the layout must not have to undo it by hand.
 */
function PaneMenu({
  index,
  layout,
  tab,
  onLayout,
}: {
  index: number
  layout: PaneLayout
  tab: PanelTab
  onLayout: (next: PaneLayout) => void
}) {
  const items: { key: Key; icon: typeof ArrowUp; next: PaneLayout }[] = [
    { key: 'pane.moveUp', icon: ArrowUp, next: moveTowards(layout, tab, 'up') },
    { key: 'pane.moveDown', icon: ArrowDown, next: moveTowards(layout, tab, 'down') },
    { key: 'pane.mergeUp', icon: Merge, next: mergeGroup(layout, index, 'up') },
    { key: 'pane.mergeDown', icon: Merge, next: mergeGroup(layout, index, 'down') },
    { key: 'pane.reset', icon: RotateCcw, next: resetLayout() },
  ]
  return (
    <div
      role="menu"
      data-testid={`pane-menu-open-${index}`}
      onPointerDown={(e) => e.stopPropagation()}
      className="vp-panel-in absolute top-10 right-2 z-30 min-w-44 rounded-vp border border-hairline p-1 vp-solid"
    >
      {items.map(({ key, icon: Icon, next }) => {
        // An operation that would change nothing is offered and refused rather
        // than hidden: a menu whose items come and go is the same complaint as
        // a strip whose buttons do, one layer in.
        const enabled = next !== layout
        return (
          <button
            key={key}
            type="button"
            role="menuitem"
            disabled={!enabled}
            onClick={() => onLayout(next)}
            className="vp-press flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-vp-base text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink disabled:opacity-40 disabled:hover:bg-transparent"
          >
            <Icon size={13} className="shrink-0" />
            <span className="truncate">{t(key)}</span>
          </button>
        )
      })}
    </div>
  )
}
