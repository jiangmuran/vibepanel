import { useCallback, useRef, useState } from 'react'
import { Activity, ChevronRight, Columns2, FolderTree, ListChecks, NotebookPen } from 'lucide-react'

import type { Project } from '../protocol/wire'
import type { PanelSocket } from '../protocol/socket'
import { FileTree } from './panels/FileTree'
import { SystemMonitor } from './panels/SystemMonitor'
import { Notes } from './panels/Notes'
import { Todos } from './panels/Todos'
import { ErrorBoundary } from './ErrorBoundary'
import { SystemStrip } from './panels/SystemStrip'
import { t, useLang, type Key } from '../i18n'

export type PanelTab = 'files' | 'monitor' | 'notes' | 'todos'

// The label is a key, not a string: resolving it at render is what makes a
// language switch repaint the tabs instead of needing a reload.
const TABS: { id: PanelTab; icon: typeof Activity; key: Key }[] = [
  { id: 'files', icon: FolderTree, key: 'panel.files' },
  { id: 'monitor', icon: Activity, key: 'panel.monitor' },
  { id: 'notes', icon: NotebookPen, key: 'panel.notes' },
  { id: 'todos', icon: ListChecks, key: 'panel.todos' },
]

interface Props {
  project: Project | null
  /** Needed by the notes and todo panels so they hear about other viewers. */
  socket: PanelSocket
  tab: PanelTab
  onTab: (t: PanelTab) => void
  width: number
  onWidthChange: (px: number) => void
  onCollapse: () => void
  /** Show notes and todos together, split vertically. */
  split: boolean
  onSplitChange: (split: boolean) => void
  splitRatio: number
  onSplitRatioChange: (ratio: number) => void
}

const MIN_WIDTH = 200
const MAX_WIDTH = 640

export function RightPanel(props: Props) {
  const { project, tab, width, split } = props

  // Only the selected tab is labelled. Four unlabelled icons are a guessing
  // game, but four labelled ones plus the two panel controls overflow a 280px
  // column and push the collapse button off the edge. Naming where you are —
  // and leaving tooltips for the rest — fits and answers the more useful
  // question.
  //
  // The four sit in a segmented control rather than in a row of loose buttons.
  // A ragged row of one label and three icons, left-aligned with the panel
  // controls floated off to the right, reads as parts that happened to land
  // near each other. Equal widths in a track read as one thing you are choosing
  // within — which is what it is — and the width the label needs is then taken
  // from the group instead of from the buttons beside it.
  const showLabel = (id: PanelTab) => id === tab && width >= 230
  useLang()
  const dragFrom = useRef<{ x: number; width: number } | null>(null)
  const splitRef = useRef<HTMLDivElement | null>(null)
  const [splitDragging, setSplitDragging] = useState(false)

  const onWidthStart = useCallback(
    (e: React.PointerEvent) => {
      e.preventDefault()
      e.currentTarget.setPointerCapture(e.pointerId)
      dragFrom.current = { x: e.clientX, width }
    },
    [width],
  )
  const onWidthMove = useCallback(
    (e: React.PointerEvent) => {
      const from = dragFrom.current
      if (!from) return
      // Dragging left widens the panel, so the delta is inverted.
      const next = from.width + (from.x - e.clientX)
      props.onWidthChange(Math.max(MIN_WIDTH, Math.min(next, MAX_WIDTH)))
    },
    [props],
  )
  const onWidthEnd = useCallback((e: React.PointerEvent) => {
    if (e.currentTarget.hasPointerCapture(e.pointerId)) {
      e.currentTarget.releasePointerCapture(e.pointerId)
    }
    dragFrom.current = null
  }, [])

  const onSplitMove = useCallback(
    (e: React.PointerEvent) => {
      if (!splitDragging) return
      const box = splitRef.current?.getBoundingClientRect()
      if (!box || box.height === 0) return
      const ratio = (e.clientY - box.top) / box.height
      props.onSplitRatioChange(Math.max(0.15, Math.min(0.85, ratio)))
    },
    [splitDragging, props],
  )

  // Notes and todos are the pair worth seeing together — what you are thinking
  // and what you have left. Files and the monitor are lookups, not companions.
  const splittable = tab === 'notes' || tab === 'todos'
  const showSplit = split && splittable

  const body = () => {
    if (!project) {
      return <p className="px-3 py-4 text-[12px] text-ink-2">No project selected.</p>
    }
    if (tab === 'files') return <FileTree key={project.id} projectId={project.id} />
    if (tab === 'monitor') return <SystemMonitor />
    if (showSplit) {
      return (
        <div ref={splitRef} className="flex h-full min-h-0 flex-col">
          <div className="min-h-0 overflow-hidden" style={{ flexBasis: `${props.splitRatio * 100}%` }}>
            <Notes key={project.id} projectId={project.id} socket={props.socket} />
          </div>
          <div
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
            style={{ touchAction: 'none' }}
            title="Drag to resize"
            className="h-1.5 shrink-0 cursor-row-resize border-y border-hairline transition-colors duration-200 ease-vp hover:bg-accent"
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
      className="flex shrink-0 border-l border-hairline vp-blur"
      style={{ width }}
    >
      <div
        data-testid="panel-resize"
        onPointerDown={onWidthStart}
        onPointerMove={onWidthMove}
        onPointerUp={onWidthEnd}
        onPointerCancel={onWidthEnd}
        style={{ touchAction: 'none' }}
        title="Drag to resize"
        // `relative z-10` is what makes the grip hittable, not decoration.
        //
        // The negative margin pulls the panel's content four pixels left so
        // the eight-pixel grip straddles the border rather than sitting beside
        // it. The content is the later sibling, so without a stacking order it
        // paints over the half it overlaps — and that is the half on the
        // visible edge, which is where anyone aims. Measured with
        // elementFromPoint across the grip: offsets 0-3 hit it, 4-7 hit the
        // content. Half the target, and the wrong half.
        className="relative z-10 -mr-1 w-2 shrink-0 cursor-col-resize"
      />

      <div className="flex min-w-0 flex-1 flex-col">
        <header
          data-testid="panel-header"
          className="flex h-10 shrink-0 items-center gap-1 overflow-hidden border-b border-hairline px-2"
        >
          <div className="flex min-w-0 flex-1 items-center gap-0.5 rounded-lg bg-surface-2 p-0.5">
            {TABS.map(({ id, icon: Icon, key }) => {
              const label = t(key)
              return (
              <button
                key={id}
                type="button"
                data-testid={`panel-tab-${id}`}
                onClick={() => props.onTab(id)}
                title={label}
                aria-pressed={tab === id}
                className={`flex min-w-0 flex-1 items-center justify-center gap-1 rounded-[7px] py-1 text-[11px] transition-colors duration-200 ease-vp ${
                  tab === id
                    ? 'bg-surface text-ink shadow-[0_1px_2px_rgb(0_0_0/0.12)]'
                    : 'text-ink-2 hover:text-ink'
                }`}
              >
                <Icon size={13} className="shrink-0" />
                {showLabel(id) && <span className="truncate">{label}</span>}
                </button>
              )
            })}
          </div>
          {splittable && (
            <button
              type="button"
              data-testid="panel-split"
              onClick={() => props.onSplitChange(!split)}
              title={split ? t('panel.splitOff') : t('panel.splitOn')}
              className={`shrink-0 rounded-md p-1.5 transition-colors duration-200 ease-vp ${
                split ? 'text-accent' : 'text-ink-2 hover:bg-surface-2 hover:text-ink'
              }`}
            >
              <Columns2 size={14} className="rotate-90" />
            </button>
          )}
          <button
            type="button"
            onClick={props.onCollapse}
            data-testid="panel-collapse"
            title={t('app.hidePanel')}
            className="shrink-0 rounded-md p-1.5 text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
          >
            <ChevronRight size={14} />
          </button>
        </header>

        {/* Per-tab, so the panel's own chrome — the tabs, the width, the
            collapse control — survives whatever the tab does, and switching
            away from a broken one is still possible. Keyed by tab so the
            boundary resets when you move to another. */}
        <div className="min-h-0 flex-1 overflow-y-auto">
          <ErrorBoundary key={tab} label={`The ${tab} panel`}>
            {body()}
          </ErrorBoundary>
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
