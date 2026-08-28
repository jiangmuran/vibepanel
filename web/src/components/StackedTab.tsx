import { useCallback, useEffect, useRef, useState } from 'react'

import { resizeStep } from './chrome'
import {
  clampStackRatio,
  readStackRatio,
  stackRatioAt,
  stackStorageKey,
} from './stack'
import { t, useLang } from '../i18n'

/**
 * Two panels in one tab, with a divider between them you can drag.
 *
 * 「可以上下拖动」. The top half is what the tab is for — the files, or the
 * note. The bottom half is the dock, which is the same on both tabs and names
 * its own blocks, so nothing here labels it: a heading above a thing that
 * already has two headings is furniture.
 *
 * The divider position is per tab even though the bottom half is shared, and
 * that is deliberate rather than an oversight. Somebody reading a long file
 * list wants the dock small; somebody writing a note wants the same dock in the
 * same place at whatever size they left it there. One shared ratio would make
 * every tab switch move a boundary nobody touched.
 *
 * The same gesture as the divider between two panes: same `.vp-grip`, same
 * `role="separator"`, same arrow keys, same shift for a larger step. Three
 * places in this layout drag a boundary and all three feel identical, which is
 * most of what stops the panel reading as parts that arrived separately.
 */
export function StackedTab({
  id,
  top,
  bottom,
  swapDir,
}: {
  /** Which tab this is, which is also where its divider position lives. */
  id: string
  top: React.ReactNode
  bottom: React.ReactNode
  /**
   * Which way the tab strip moved, or undefined for no movement.
   *
   * On the top half and nowhere else. The dock below is the same content on
   * both tabs, and sliding something that did not change says a change
   * happened -- 「下半部分既然内容不变那么也不要有切换动画了」. It also used
   * to be *remounted*: the animation wrapper carried `key={tab}`, so every tab
   * switch tore down the monitor and rebuilt it, which is a two-second gap in
   * the one block that is supposed to be always on. The comment above the dock
   * already said the two tabs are handed one element; the key made that untrue
   * one component further out.
   */
  swapDir?: 'forward' | 'back'
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

  const pct = Math.round(ratio * 100)

  return (
    <div ref={boxRef} data-testid={`stack-${id}`} className="flex h-full min-h-0 flex-col">
      {/* flex-basis rather than height, so the two halves and the divider
          divide the box exactly. A percentage height ignores the eight pixels
          the divider takes and the pair overflows by exactly that much. */}
      <div
        data-testid={`stack-${id}-top`}
        key={id}
        data-dir={swapDir}
        // Not `vp-stack-half` while dragging: a transition on the size of the
        // thing under the pointer means the divider arrives where the pointer
        // was two frames ago, which reads as a lag in the panel rather than as
        // a movement. Off for the drag, on for the keyboard — where the step
        // is discrete and the ease is what makes it read as one boundary
        // moving rather than two panels resizing.
        className={`vp-swap min-h-0 overflow-y-auto ${dragging ? '' : 'vp-stack-half'}`}
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
        // they have — and there are three of them in this column.
        aria-label={t('panel.dockDivider')}
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
        data-testid={`stack-${id}-bottom`}
        className={`flex min-h-0 flex-col ${dragging ? '' : 'vp-stack-half'}`}
        style={{ flexGrow: 1 - ratio, flexShrink: 1, flexBasis: 0 }}
      >
        {bottom}
      </div>
    </div>
  )
}
