import { useCallback, useRef, useState } from 'react'
import { Activity, ChevronRight, Columns2, FolderTree, ListChecks, NotebookPen } from 'lucide-react'

import type { Project } from '../protocol/wire'
import type { PanelSocket } from '../protocol/socket'
import { FileTree } from './panels/FileTree'
import { SystemMonitor } from './panels/SystemMonitor'
import { Notes } from './panels/Notes'
import { Todos } from './panels/Todos'

export type PanelTab = 'files' | 'monitor' | 'notes' | 'todos'

const TABS: { id: PanelTab; icon: typeof Activity; label: string }[] = [
  { id: 'files', icon: FolderTree, label: 'Files' },
  { id: 'monitor', icon: Activity, label: 'System' },
  { id: 'notes', icon: NotebookPen, label: 'Notes' },
  { id: 'todos', icon: ListChecks, label: 'Todo' },
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
  const showLabel = (id: PanelTab) => id === tab && width >= 230
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
        onPointerDown={onWidthStart}
        onPointerMove={onWidthMove}
        onPointerUp={onWidthEnd}
        onPointerCancel={onWidthEnd}
        style={{ touchAction: 'none' }}
        title="Drag to resize"
        className="-mr-1 w-2 shrink-0 cursor-col-resize"
      />

      <div className="flex min-w-0 flex-1 flex-col">
        <header
          data-testid="panel-header"
          className="flex h-8 shrink-0 items-center gap-0.5 overflow-hidden border-b border-hairline px-1"
        >
          {TABS.map(({ id, icon: Icon, label }) => (
            <button
              key={id}
              type="button"
              data-testid={`panel-tab-${id}`}
              onClick={() => props.onTab(id)}
              title={label}
              className={`flex items-center gap-1 rounded-md px-1.5 py-1.5 text-[11px] transition-colors duration-200 ease-vp ${
                tab === id ? 'bg-surface-2 text-ink' : 'text-ink-2 hover:bg-surface-2 hover:text-ink'
              }`}
            >
              <Icon size={13} className="shrink-0" />
              {showLabel(id) && <span>{label}</span>}
            </button>
          ))}
          {splittable && (
            <button
              type="button"
              data-testid="panel-split"
              onClick={() => props.onSplitChange(!split)}
              title={split ? 'Show one at a time' : 'Show notes and todo together'}
              className={`ml-auto rounded-md p-1.5 transition-colors duration-200 ease-vp ${
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
            title="Hide panel"
            className={`shrink-0 rounded-md p-1.5 text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink ${
              splittable ? '' : 'ml-auto'
            }`}
          >
            <ChevronRight size={14} />
          </button>
        </header>

        <div className="min-h-0 flex-1 overflow-y-auto">{body()}</div>
      </div>
    </aside>
  )
}
