import { useCallback, useRef, useState } from 'react'
import { ChevronDown, Plus, X } from 'lucide-react'

import type { PanelSocket } from '../protocol/socket'
import type { Session } from '../protocol/wire'
import { terminalLabel } from './label'
import { TerminalView } from './Terminal'
import { InlineName } from './InlineName'
import { StateDot } from './StateDot'

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
        className="-mt-1 h-2 shrink-0 cursor-row-resize transition-colors duration-200 ease-vp hover:bg-accent"
      />

      <div className="flex h-8 shrink-0 items-center gap-1 px-2 vp-blur">
        {/* The tabs scroll; the controls after them do not.
            
            This row had no overflow handling at all. Eight terminals in an
            820px window put four of them past the right edge — and `overflow:
            visible` means they were not clipped but *drawn over the panel to
            the right*, with no way to scroll to them. The same shape as the
            key bar: a row that can outgrow its box and hides whatever does not
            fit.
            
            New-terminal and collapse are outside the scroller deliberately. Put
            them inside and they scroll away exactly when there are enough tabs
            to need them. */}
        <div className="flex min-w-0 flex-1 items-center gap-1 overflow-x-auto">
        {terminals.map((t, i) => (
          <div
            key={t.id}
            data-testid="bottom-tab"
            data-session-id={t.id}
            data-active={active?.id === t.id}
            onClick={() => setActiveId(t.id)}
            className={`group flex max-w-44 shrink-0 cursor-pointer items-center gap-1 rounded-vp px-2 py-1 text-[12px] transition-colors duration-200 ease-vp ${
              active?.id === t.id ? 'bg-surface-2 text-ink' : 'text-ink-2 hover:bg-surface-2'
            }`}
          >
            {/* Only when it has exited.
                A bottom terminal that is running needs no decoration — the
                strip would be a row of identical dots — but one whose process
                is gone gave no sign at all, so a build that died down here
                looked exactly like a build still going. The glyph carries the
                difference between a clean exit and a crash by shape, as
                everywhere else. */}
            {t.exited && (
              <StateDot state={t.state} exited exitStatus={t.exitStatus} size={8} />
            )}
            <InlineName
              value={terminalLabel(t, i)}
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
              className="vp-tap rounded p-0.5 vp-reveal hover:text-ink"
            >
              <X size={11} />
            </button>
          </div>
        ))}
        </div>
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
          className="rounded-md p-1 text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
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
