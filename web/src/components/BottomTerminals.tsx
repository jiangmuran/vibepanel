import { useCallback, useRef, useState } from 'react'
import { ChevronDown, Plus, X } from 'lucide-react'

import type { PanelSocket } from '../protocol/socket'
import type { Session } from '../protocol/wire'
import { terminalLabel } from './label'
import { TerminalView } from './Terminal'
import { focusTerminal } from './focus'
import { InlineName } from './InlineName'
import { StateDot } from './StateDot'
import { bottomControls, clampBottomHeight, resizeStep } from './chrome'
import { safeText } from './text'
import { t as tr, useLang } from '../i18n'

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

/**
 * Scratch terminals belonging to the session above them.
 *
 * They follow the main session rather than being a global set: a terminal
 * opened while working on one project should not still be sitting there,
 * pointing at the wrong directory, when you switch to another. New ones start
 * in whatever directory the session above is currently in.
 */
export function BottomTerminals(props: Props) {
  useLang()
  const { socket, parent, terminals, themeKey, height } = props
  const [activeId, setActiveId] = useState<string | null>(null)
  const wrapRef = useRef<HTMLDivElement | null>(null)
  const dragFrom = useRef<{ y: number; height: number } | null>(null)
  const [dragging, setDragging] = useState(false)

  // The active tab is derived rather than stored, so a terminal closing or the
  // parent changing cannot leave a selection pointing at nothing.
  const active = terminals.find((t) => t.id === activeId) ?? terminals[0] ?? null

  /** The height the strip and the main terminal are dividing between them. */
  const available = () => wrapRef.current?.parentElement?.clientHeight ?? 600

  const onResizeStart = useCallback(
    (e: React.PointerEvent) => {
      e.preventDefault()
      e.currentTarget.setPointerCapture(e.pointerId)
      dragFrom.current = { y: e.clientY, height }
      setDragging(true)
    },
    [height],
  )

  const onResizeMove = useCallback(
    (e: React.PointerEvent) => {
      const from = dragFrom.current
      if (!from) return
      // Dragging up makes the panel taller, so the delta is inverted.
      props.onHeightChange(clampBottomHeight(from.height + (from.y - e.clientY), available()))
    },
    [props],
  )

  const onResizeEnd = useCallback((e: React.PointerEvent) => {
    if (e.currentTarget.hasPointerCapture(e.pointerId)) {
      e.currentTarget.releasePointerCapture(e.pointerId)
    }
    dragFrom.current = null
    setDragging(false)
  }, [])

  return (
    <div
      ref={wrapRef}
      data-testid="bottom-terminals"
      // From the edge it lives on, like the side panel from its own. The two
      // strips are the same object seen twice; they open the same way.
      className="vp-edge-in-bottom flex shrink-0 flex-col border-t border-hairline"
      style={{ height }}
    >
      <div
        role="separator"
        aria-orientation="horizontal"
        aria-valuenow={Math.round(height)}
        tabIndex={0}
        data-dragging={dragging}
        onPointerDown={onResizeStart}
        onPointerMove={onResizeMove}
        onPointerUp={onResizeEnd}
        onPointerCancel={onResizeEnd}
        onKeyDown={(e) => {
          const step = resizeStep(e.key, e.shiftKey)
          if (step === null) return
          e.preventDefault()
          props.onHeightChange(clampBottomHeight(height + step, available()))
        }}
        title={tr('bottom.resize')}
        // The same grip as the side panel's and the notes/todo split's: an
        // eight-pixel hit area with a small pill in the middle of it. It was
        // three different affordances in three places, which is most of why
        // this strip read as assembled rather than designed. touch-action
        // comes with .vp-grip — without it the browser scrolls instead of
        // reporting the drag, and on touch the gesture never arrives at all.
        className="vp-grip -mt-1 h-2 cursor-row-resize"
      />

      <div className="vp-chrome gap-1 px-2 vp-blur">
        {/* The tabs scroll; the controls after them do not.

            This row had no overflow handling at all. Eight terminals in an
            820px window put four of them past the right edge — and `overflow:
            visible` means they were not clipped but *drawn over the panel to
            the right*, with no way to scroll to them. The same shape as the
            key bar: a row that can outgrow its box and hides whatever does not
            fit.

            New-terminal and collapse are outside the scroller deliberately.
            Put them inside and they scroll away exactly when there are enough
            tabs to need them. */}
        <div className="flex min-w-0 flex-1 items-center gap-0.5 overflow-x-auto">
          {terminals.map((term, i) => {
            const choose = () => {
              setActiveId(term.id)
              // The tab you just chose is the one you want to type into. The
              // main terminal above usually has the keyboard at this point,
              // and its hidden textarea is why focusTerminal has to know an
              // xterm from a text field.
              focusTerminal(term.id)
            }
            return (
            <div
              key={term.id}
              data-testid="bottom-tab"
              data-session-id={term.id}
              data-active={active?.id === term.id}
              // role and tabIndex, because the strip was unreachable from the
              // keyboard: tab past the terminal and there was nothing there at
              // all — no way to choose a tab, rename one or close one.
              //
              // A div carrying the role rather than a real <button>, because
              // renaming replaces the label with an <input>, and interactive
              // content inside a <button> is not something the parser will
              // build: it closes the button first and the tab stops being one.
              role="button"
              tabIndex={0}
              onClick={choose}
              onKeyDown={(e) => {
                if (e.key !== 'Enter' && e.key !== ' ') return
                e.preventDefault()
                choose()
              }}
              className="vp-tab group max-w-44 shrink-0 cursor-pointer text-vp-base"
            >
              {/* Only when it has exited.
                  A bottom terminal that is running needs no decoration — the
                  strip would be a row of identical dots — but one whose process
                  is gone gave no sign at all, so a build that died down here
                  looked exactly like a build still going. The glyph carries the
                  difference between a clean exit and a crash by shape, as
                  everywhere else. */}
              {term.exited && (
                <StateDot state={term.state} exited exitStatus={term.exitStatus} size={8} />
              )}
              <InlineName
                value={terminalLabel(term, i)}
                onCommit={(next) => props.onRename(term, next)}
                className="max-w-32"
              />
              <button
                type="button"
                onClick={(e) => {
                  e.stopPropagation()
                  props.onClose(term)
                }}
                title={tr('bottom.close')}
                className="vp-press vp-tap rounded-md p-0.5 vp-reveal hover:text-ink"
              >
                <X size={11} />
              </button>
            </div>
            )
          })}
        </div>

        <span className="vp-divider" aria-hidden="true" />

        {/* From the list, not written out: the two of them are here at every
            tab count, including none. Hiding "close the strip" when the strip
            is empty is exactly the moment somebody wants it gone. */}
        {bottomControls(terminals.length).map((control) =>
          control.id === 'new' ? (
            <button
              key={control.id}
              type="button"
              data-testid={control.testid}
              onClick={props.onNew}
              // safeText, because cwd is whatever directory the session is
              // sitting in and a directory name is arbitrary bytes — one
              // carrying U+202E reverses the tooltip around it. This was a
              // template literal in English with the raw cwd in the middle,
              // so it was both untranslated and unsanitised.
              title={
                parent.cwd ? tr('bottom.newIn', { dir: safeText(parent.cwd) }) : tr('bottom.new')
              }
              className="vp-control"
            >
              <Plus size={14} />
            </button>
          ) : (
            <button
              key={control.id}
              type="button"
              data-testid={control.testid}
              onClick={props.onCollapse}
              title={tr('bottom.hide')}
              className="vp-control"
            >
              <ChevronDown size={14} />
            </button>
          ),
        )}
      </div>

      {/* Set into the chrome, like the main terminal above it. Two terminals
          treated differently in one window is the detail that makes a layout
          look assembled rather than designed. */}
      <div
        className="mx-2 mb-2 min-h-0 flex-1 overflow-hidden rounded-vp border border-hairline"
        style={{ background: 'var(--vp-terminal-bg)' }}
      >
        {active ? (
          <TerminalView
            key={active.id}
            socket={socket}
            sessionId={active.id}
            themeKey={themeKey}
            className="h-full w-full px-2 py-1"
          />
        ) : (
          <div className="flex h-full items-center justify-center text-vp-base text-ink-2">
            {tr('bottom.empty')}
          </div>
        )}
      </div>
    </div>
  )
}
