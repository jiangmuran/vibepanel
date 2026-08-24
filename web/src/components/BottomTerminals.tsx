import { useCallback, useRef, useState } from 'react'
import { ChevronDown, Plus, X } from 'lucide-react'

import type { PanelSocket } from '../protocol/socket'
import type { Session } from '../protocol/wire'
import { TerminalView } from './Terminal'
import { InlineName } from './InlineName'

interface Props {
  socket: PanelSocket
  /** The main session these terminals belong to. */
  parent: Session
  terminals: Session[]
  themeKey: string
  height: number
  onHeightChange: (px: number) => void
  onCollapse: () => void
  onNew: () => void
  onClose: (s: Session) => void
  onRename: (s: Session, title: string) => void
}

const MIN_HEIGHT = 80
/** Leave at least this much of the main terminal visible. */
const MIN_MAIN_HEIGHT = 120

function label(s: Session, index: number): string {
  // No fallback to the command name. The server leaves a scratch terminal's
  // title empty precisely when it has no useful automatic name — every shell
  // is called "bash", and a strip of tabs all reading "bash" tells you
  // nothing. Trust that judgement rather than re-deriving it here, where the
  // two would drift.
  return s.title || `term ${index + 1}`
}

/**
 * Scratch terminals belonging to the session above them.
 *
 * They follow the main session rather than being a global set: a terminal
 * opened while working on one project should not still be sitting there,
 * pointing at the wrong directory, when you switch to another. New ones start
 * in whatever directory the session above is currently in.
 */
export function BottomTerminals(props: Props) {
  const { socket, parent, terminals, themeKey, height } = props
  const [activeId, setActiveId] = useState<string | null>(null)
  const wrapRef = useRef<HTMLDivElement | null>(null)
  const dragFrom = useRef<{ y: number; height: number } | null>(null)

  // The active tab is derived rather than stored, so a terminal closing or the
  // parent changing cannot leave a selection pointing at nothing.
  const active = terminals.find((t) => t.id === activeId) ?? terminals[0] ?? null

  const onResizeStart = useCallback(
    (e: React.PointerEvent) => {
      e.preventDefault()
      e.currentTarget.setPointerCapture(e.pointerId)
      dragFrom.current = { y: e.clientY, height }
    },
    [height],
  )

  const onResizeMove = useCallback(
    (e: React.PointerEvent) => {
      const from = dragFrom.current
      if (!from) return
      const available = wrapRef.current?.parentElement?.clientHeight ?? 600
      // Dragging up makes the panel taller, so the delta is inverted.
      const next = from.height + (from.y - e.clientY)
      props.onHeightChange(Math.max(MIN_HEIGHT, Math.min(next, available - MIN_MAIN_HEIGHT)))
    },
    [props],
  )

  const onResizeEnd = useCallback((e: React.PointerEvent) => {
    if (e.currentTarget.hasPointerCapture(e.pointerId)) {
      e.currentTarget.releasePointerCapture(e.pointerId)
    }
    dragFrom.current = null
  }, [])

  return (
    <div
      ref={wrapRef}
      data-testid="bottom-terminals"
      className="flex shrink-0 flex-col border-t border-hairline"
      style={{ height }}
    >
      <div
        onPointerDown={onResizeStart}
        onPointerMove={onResizeMove}
        onPointerUp={onResizeEnd}
        onPointerCancel={onResizeEnd}
        title="Drag to resize"
        // Without touch-action the browser scrolls instead of reporting the
        // drag, and on touch the gesture never arrives at all.
        style={{ touchAction: 'none' }}
        className="-mt-1 h-2 shrink-0 cursor-row-resize"
      />

      <div className="flex h-8 shrink-0 items-center gap-1 px-2 vp-blur">
        {terminals.map((t, i) => (
          <div
            key={t.id}
            data-testid="bottom-tab"
            data-active={active?.id === t.id}
            onClick={() => setActiveId(t.id)}
            className={`group flex max-w-44 cursor-pointer items-center gap-1 rounded-vp px-2 py-1 text-[12px] transition-colors duration-200 ease-vp ${
              active?.id === t.id ? 'bg-surface-2 text-ink' : 'text-ink-2 hover:bg-surface-2'
            }`}
          >
            <InlineName
              value={label(t, i)}
              onCommit={(next) => props.onRename(t, next)}
              className="max-w-32"
            />
            <button
              type="button"
              onClick={(e) => {
                e.stopPropagation()
                props.onClose(t)
              }}
              title="Close terminal"
              className="rounded p-0.5 opacity-0 transition-opacity duration-200 ease-vp group-hover:opacity-100 hover:text-ink focus-visible:opacity-100"
            >
              <X size={11} />
            </button>
          </div>
        ))}
        <button
          type="button"
          data-testid="bottom-new"
          onClick={props.onNew}
          title={`New terminal in ${parent.cwd || 'this project'}`}
          className="rounded-md p-1 text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
        >
          <Plus size={14} />
        </button>
        <button
          type="button"
          onClick={props.onCollapse}
          title="Hide terminals"
          className="ml-auto rounded-md p-1 text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
        >
          <ChevronDown size={14} />
        </button>
      </div>

      <div className="min-h-0 flex-1" style={{ background: 'var(--vp-terminal-bg)' }}>
        {active ? (
          <TerminalView
            key={active.id}
            socket={socket}
            sessionId={active.id}
            themeKey={themeKey}
            className="h-full w-full px-2 py-1"
          />
        ) : (
          <div className="flex h-full items-center justify-center text-[12px] text-ink-2">
            No terminals here yet
          </div>
        )}
      </div>
    </div>
  )
}
