import { useCallback, useEffect, useRef, useState } from 'react'
import { Activity, ChevronRight, Coins, FolderTree, ListChecks, NotebookPen, Rows2, Square } from 'lucide-react'

import type { Project, Session } from '../protocol/wire'
import type { PanelSocket } from '../protocol/socket'
import { FileTree } from './panels/FileTree'
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
  clampSplitRatio,
  panelChrome,
  panelControls,
  resizeStep,
  splitTarget,
  splitTitleKey,
  splittable,
  swapDirection,
  tabFromKey,
  type PanelTab,
} from './chrome'
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
  monitor: { icon: Activity, key: 'panel.monitor' },
  notes: { icon: NotebookPen, key: 'panel.notes' },
  todos: { icon: ListChecks, key: 'panel.todos' },
  tokens: { icon: Coins, key: 'panel.tokens' },
}

interface Props {
  project: Project | null
  /** Every session, so the monitor can name what it is measuring. */
  sessions: Session[]
  /** Needed by the notes and todo panels so they hear about other viewers. */
  socket: PanelSocket
  tab: PanelTab
  onTab: (t: PanelTab) => void
  width: number
  onWidthChange: (px: number) => void
  onCollapse: () => void
  /** Opens the full-width token view. The panel is too narrow to hold it. */
  onOpenTokens: () => void
  /** Show notes and todos together, split vertically. */
  split: boolean
  onSplitChange: (split: boolean) => void
  splitRatio: number
  onSplitRatioChange: (ratio: number) => void
}

export function RightPanel(props: Props) {
  const { project, tab, width, split } = props

  const lang = useLang()
  const { labelled } = panelChrome(width)
  const dragFrom = useRef<{ x: number; width: number } | null>(null)
  const [widthDragging, setWidthDragging] = useState(false)
  const splitRef = useRef<HTMLDivElement | null>(null)
  const [splitDragging, setSplitDragging] = useState(false)

  // Where the marker sits, measured rather than computed.
  //
  // The tabs are not equal widths — the selected one grows to hold its name —
  // so there is no arithmetic that gets this right, and a marker that is a few
  // pixels out is worse than none. offsetLeft is relative to the track, which
  // is the marker's containing block.
  //
  // Deliberately a plain effect and not useLayoutEffect. A layout effect would
  // move the marker in the same commit that selected the new tab, and a
  // property that changes without an intervening paint does not transition —
  // the marker would teleport, which is the thing this whole mechanism exists
  // to stop.
  const trackRef = useRef<HTMLDivElement | null>(null)
  const [marker, setMarker] = useState<{ left: number; width: number } | null>(null)
  useEffect(() => {
    const track = trackRef.current
    const el = track?.querySelector<HTMLElement>('[aria-selected="true"]')
    if (!track || !el) return
    const measure = () => {
      // A zero-width box is a panel nothing has laid out yet — an ancestor
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
  }, [tab, width, lang])

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

  const onSplitMove = useCallback(
    (e: React.PointerEvent) => {
      if (!splitDragging) return
      const box = splitRef.current?.getBoundingClientRect()
      if (!box || box.height === 0) return
      props.onSplitRatioChange(clampSplitRatio((e.clientY - box.top) / box.height))
    },
    [splitDragging, props],
  )

  const showSplit = split && splittable(tab)

  const body = () => {
    // Before the no-project guard, and the only tab that is. The other four
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
    if (tab === 'monitor') return <SystemMonitor sessions={props.sessions} />
    if (showSplit) {
      return (
        <div ref={splitRef} className="flex h-full min-h-0 flex-col">
          <div className="min-h-0 overflow-hidden" style={{ flexBasis: `${props.splitRatio * 100}%` }}>
            <Notes key={project.id} projectId={project.id} socket={props.socket} />
          </div>
          <div
            role="separator"
            aria-orientation="horizontal"
            tabIndex={0}
            data-dragging={splitDragging}
            onPointerDown={(e) => {
              e.preventDefault()
              e.currentTarget.setPointerCapture(e.pointerId)
              setSplitDragging(true)
            }}
            onPointerMove={onSplitMove}
            onPointerUp={(e) => {
              if (e.currentTarget.hasPointerCapture(e.pointerId)) {
                e.currentTarget.releasePointerCapture(e.pointerId)
              }
              setSplitDragging(false)
            }}
            onKeyDown={(e) => {
              const step = resizeStep(e.key, e.shiftKey)
              if (step === null) return
              const box = splitRef.current?.getBoundingClientRect()
              if (!box || box.height === 0) return
              e.preventDefault()
              props.onSplitRatioChange(clampSplitRatio(props.splitRatio - step / box.height))
            }}
            title={t('panel.resize')}
            className="vp-grip h-2 cursor-row-resize border-y border-hairline"
          />
          <div className="min-h-0 flex-1 overflow-hidden">
            <Todos key={project.id} projectId={project.id} socket={props.socket} />
          </div>
        </div>
      )
    }
    if (tab === 'notes') return <Notes key={project.id} projectId={project.id} socket={props.socket} />
    return <Todos key={project.id} projectId={project.id} socket={props.socket} />
  }

  return (
    <aside
      data-testid="right-panel"
      data-tab={tab}
      data-split={showSplit}
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
        <header
          data-testid="panel-header"
          className="vp-chrome gap-1 overflow-hidden border-b border-hairline px-2"
        >
          {/* Five destinations in a track, not five loose buttons. A ragged
              row of one label and four icons with the panel controls floated
              off to the right reads as parts that happened to land near each
              other; a track reads as one thing you are choosing within, which
              is what it is. */}
          <div
            ref={trackRef}
            role="tablist"
            aria-label={t('panel.tablist')}
            aria-orientation="horizontal"
            data-labelled={labelled}
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
            {PANEL_TABS.map((id) => {
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
                  aria-controls="panel-body"
                  // Roving: the strip is one stop in the page's tab order and
                  // the arrows move inside it, which is what a tablist is.
                  // Five stops on the way to the terminal is not navigation.
                  tabIndex={tab === id ? 0 : -1}
                  onClick={() => props.onTab(id)}
                  onKeyDown={(e) => {
                    const next = tabFromKey(e.key, tab)
                    if (!next) return
                    e.preventDefault()
                    props.onTab(next)
                    document.getElementById(`panel-tab-${next}`)?.focus()
                  }}
                  title={label}
                  className="vp-tab text-vp-sm"
                >
                  <Icon size={13} className="shrink-0" />
                  <span className="vp-tab-label">{label}</span>
                </button>
              )
            })}
          </div>

          <span className="vp-divider" aria-hidden="true" />

          {/* Rendered from the list rather than written out, so "which
              controls are in this header" is one answer in one place that a
              test can sweep. Both of these are here at every width and on
              every tab — see panelControls(). */}
          {panelControls(tab).map((control) => {
            if (control.id === 'split') {
              const target = splitTarget(tab, split)
              return (
                <button
                  key={control.id}
                  type="button"
                  data-testid={control.testid}
                  onClick={() => {
                    if (target.tab !== tab) props.onTab(target.tab)
                    props.onSplitChange(target.split)
                  }}
                  aria-pressed={showSplit}
                  title={t(splitTitleKey(tab, split))}
                  className="vp-control"
                >
                  {/* The current layout, by shape: one pane or two rows. The
                      accent tint says the same thing a second way, which is
                      the point — pressed state carried by hue alone is
                      unreadable in a dark room at 2am. */}
                  {showSplit ? <Rows2 size={14} /> : <Square size={14} />}
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

        {/* Per-tab, so the panel's own chrome — the tabs, the width, the
            collapse control — survives whatever the tab does, and switching
            away from a broken one is still possible. Keyed by tab so the
            boundary resets when you move to another.

            overflow-x-clip because the swap slides ten pixels sideways as it
            arrives, and a scroll container whose child briefly overflows grows
            a horizontal scrollbar for the length of the animation. */}
        <div
          id="panel-body"
          role="tabpanel"
          aria-labelledby={`panel-tab-${tab}`}
          className="min-h-0 flex-1 overflow-x-clip overflow-y-auto"
        >
          <div key={tab} data-dir={swap.dir} className="vp-swap min-h-full">
            <ErrorBoundary label={`The ${tab} panel`}>{body()}</ErrorBoundary>
          </div>
        </div>

        {/* Always on, below whatever is chosen above. "Is the machine coping"
            is not a question you navigate to -- it is a thing you want in the
            corner of your eye while reading a terminal. As a tab it was three
            figures given a whole column, and invisible from the other three. */}
        {tab !== 'monitor' && (
          <ErrorBoundary label="The monitor strip">
            <SystemStrip />
          </ErrorBoundary>
        )}
      </div>
    </aside>
  )
}
