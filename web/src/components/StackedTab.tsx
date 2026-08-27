import { useCallback, useEffect, useRef, useState } from 'react'

import { resizeStep } from './chrome'
import {
  clampStackRatio,
  readStackRatio,
  stackRatioAt,
  stackStorageKey,
} from './stack'
import { t, useLang, type Key } from '../i18n'

/**
 * Two panels in one tab, with a divider between them you can drag.
 *
 * 「放在文件tab的下半段 可以上下拖动」. The file tree and the repository are
 * two halves of one question — what is in this directory, and what has changed
 * in it — and so are a note and a checklist. Each pair used to be two tabs,
 * which meant answering the second half cost you sight of the first.
 *
 * The same gesture as the divider between two panes, deliberately: same
 * `.vp-grip`, same `role="separator"`, same arrow keys, same shift for a larger
 * step. There are now three places in this layout where a boundary is dragged
 * and all three feel identical, which is most of what stops the panel reading
 * as parts that arrived separately.
 *
 * The lower half is named, the upper one is not. The upper half's name is the
 * tab you are on — its icon is lit in the strip above — and repeating it would
 * be a heading that says what the selection already says. The lower half has
 * nothing else to identify it, and an unlabelled second panel below a line is
 * a panel people ask about rather than read.
 */
export function StackedTab({
  id,
  label,
  icon: Icon,
  top,
  bottom,
}: {
  /** Which tab this is, which is also where its divider position lives. */
  id: string
  /** Names the *lower* half. The upper half is named by the tab strip. */
  label: Key
  icon: React.ComponentType<{ size?: number; className?: string }>
  top: React.ReactNode
  bottom: React.ReactNode
}) {
  useLang()
  const boxRef = useRef<HTMLDivElement | null>(null)
  const key = stackStorageKey(id)
  const [ratio, setRatioState] = useState(() => {
    try {
      return readStackRatio(localStorage.getItem(key))
    } catch {
      // Private mode. The divider still drags; it just does not persist.
      return readStackRatio(null)
    }
  })
  const [dragging, setDragging] = useState(false)

  const setRatio = useCallback(
    (next: number) => {
      setRatioState(next)
      try {
        localStorage.setItem(key, String(next))
      } catch {
        /* private mode: the position simply does not persist */
      }
    },
    [key],
  )

  // The tab is keyed by id at the call site, so switching tabs remounts this
  // and re-reads the key. Nothing to reset on a change of `id`.
  useEffect(() => {
    if (!dragging) return
    // Released outside the window — over the terminal, off the edge of the
    // screen — still ends the drag. Without this the divider follows the
    // pointer after the button is up, which is a panel that has grabbed the
    // mouse.
    const stop = () => setDragging(false)
    window.addEventListener('pointerup', stop)
    window.addEventListener('pointercancel', stop)
    return () => {
      window.removeEventListener('pointerup', stop)
      window.removeEventListener('pointercancel', stop)
    }
  }, [dragging])

  const name = t(label)
  const pct = Math.round(ratio * 100)

  return (
    <div ref={boxRef} data-testid={`stack-${id}`} className="flex h-full min-h-0 flex-col">
      {/* flex-basis rather than height, so the two halves and the divider
          divide the box exactly. A percentage height ignores the eight pixels
          the divider takes and the pair overflows by exactly that much. */}
      <div
        data-testid={`stack-${id}-top`}
        // Not `vp-stack-half` while dragging: a transition on the size of the
        // thing under the pointer means the divider arrives where the pointer
        // was two frames ago, which reads as a lag in the panel rather than as
        // a movement. Off for the drag, on for the keyboard — where the step
        // is discrete and the ease is what makes it read as one boundary
        // moving rather than two panels resizing.
        className={`min-h-0 overflow-y-auto ${dragging ? '' : 'vp-stack-half'}`}
        style={{ flexGrow: ratio, flexShrink: 1, flexBasis: 0 }}
      >
        {top}
      </div>

      <div
        data-testid={`stack-${id}-divider`}
        role="separator"
        aria-orientation="horizontal"
        // What it divides, not what it is. "Separator" announced on its own
        // tells somebody holding the arrow keys nothing about which boundary
        // they have.
        aria-label={name}
        aria-valuenow={pct}
        aria-valuemin={0}
        aria-valuemax={100}
        tabIndex={0}
        data-dragging={dragging}
        onPointerDown={(e) => {
          e.preventDefault()
          e.currentTarget.setPointerCapture(e.pointerId)
          setDragging(true)
        }}
        onPointerMove={(e) => {
          if (!dragging) return
          const box = boxRef.current?.getBoundingClientRect()
          if (!box) return
          const next = stackRatioAt(e.clientY, box.top, box.height)
          if (next !== null) setRatio(next)
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
          const box = boxRef.current?.getBoundingClientRect()
          if (!box || box.height === 0) return
          // Without this the arrow scrolls the half above the divider, and the
          // divider is between two scroll containers.
          e.preventDefault()
          // The same sign as the divider between two panes: ArrowUp moves the
          // boundary up, so the half above shrinks and the half below grows.
          // `ratio` is where the boundary sits, which is why a positive step
          // subtracts — resizeStep is signed the way the boundary moves, not
          // the way either half changes.
          setRatio(clampStackRatio(ratio - step / box.height))
        }}
        title={t('panel.resize')}
        className="vp-grip h-2 cursor-row-resize border-y border-hairline"
      />

      <div
        className={`flex min-h-0 flex-col ${dragging ? '' : 'vp-stack-half'}`}
        style={{ flexGrow: 1 - ratio, flexShrink: 1, flexBasis: 0 }}
      >
        {/* Compact on purpose: this is a label, not a chrome row. `.vp-chrome`
            is 40px tall and a second one of those inside a tab is a quarter of
            a short pane spent saying a word. */}
        <div
          data-testid={`stack-${id}-label`}
          className="flex shrink-0 items-center gap-1.5 px-2 py-1 text-vp-xs text-ink-2"
        >
          <Icon size={11} className="shrink-0" />
          <span className="truncate">{name}</span>
        </div>
        <div data-testid={`stack-${id}-bottom`} className="min-h-0 flex-1 overflow-y-auto">
          {bottom}
        </div>
      </div>
    </div>
  )
}
