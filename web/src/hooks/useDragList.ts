import { useCallback, useRef, useState } from 'react'

export interface DragState {
  /** Id of the item being dragged, or null. */
  draggingId: string | null
  /** Index the dragged item would land at if released now. */
  overIndex: number | null
}

/**
 * Reorder-by-drag for a vertical list.
 *
 * Pointer Events rather than HTML5 drag-and-drop: the HTML5 API does not fire
 * on touch at all, so a drag built on it simply does not exist on a phone.
 * Pointer Events cover mouse, touch and pen through the same handlers.
 *
 * The caller owns the list and the commit; this only tracks where the pointer
 * is and what that would mean.
 */
export function useDragList(ids: string[], onCommit: (ordered: string[]) => void) {
  const [state, setState] = useState<DragState>({ draggingId: null, overIndex: null })
  const rowsRef = useRef(new Map<string, HTMLElement>())
  const startY = useRef(0)
  const armed = useRef(false)

  // The same drag state, kept in a ref because the handlers cannot wait for a
  // render to read it. A pointerup can arrive before React has flushed the
  // update from the pointermove just before it, and a flick is exactly when
  // those two land together — so reading `state` in the release handler
  // commits the position from one move ago, or nothing at all if the drag was
  // quick enough that no render had happened yet. State is for drawing; this
  // is for deciding.
  const live = useRef<DragState>({ draggingId: null, overIndex: null })
  const setDrag = useCallback((next: DragState) => {
    live.current = next
    setState(next)
  }, [])

  const register = useCallback((id: string, el: HTMLElement | null) => {
    if (el) rowsRef.current.set(id, el)
    else rowsRef.current.delete(id)
  }, [])

  /** Pixels of movement before a press becomes a drag. */
  const THRESHOLD = 4

  const indexForY = useCallback(
    (y: number) => {
      // Compare against the vertical midpoint of each row: crossing halfway is
      // where a human expects the gap to move, and using the top edge makes the
      // list feel like it lags a row behind the pointer.
      let index = ids.length
      for (let i = 0; i < ids.length; i++) {
        const el = rowsRef.current.get(ids[i])
        if (!el) continue
        const rect = el.getBoundingClientRect()
        if (y < rect.top + rect.height / 2) {
          index = i
          break
        }
      }
      return index
    },
    [ids],
  )

  const onPointerDown = useCallback(
    (id: string) => (e: React.PointerEvent) => {
      // Left button or touch only; a right-click drag is not a reorder.
      if (e.button !== 0) return
      e.preventDefault()
      e.currentTarget.setPointerCapture(e.pointerId)
      startY.current = e.clientY
      armed.current = true
      setDrag({ draggingId: id, overIndex: null })
    },
    [setDrag],
  )

  const onPointerMove = useCallback(
    (e: React.PointerEvent) => {
      const { draggingId, overIndex } = live.current
      if (!armed.current || !draggingId) return
      if (Math.abs(e.clientY - startY.current) < THRESHOLD && overIndex === null) return
      setDrag({ draggingId, overIndex: indexForY(e.clientY) })
    },
    [indexForY, setDrag],
  )

  const finish = useCallback(
    (e: React.PointerEvent) => {
      if (e.currentTarget.hasPointerCapture(e.pointerId)) {
        e.currentTarget.releasePointerCapture(e.pointerId)
      }
      // pointerup and pointercancel can both arrive for one gesture, so
      // without this the same reorder is committed twice.
      if (!armed.current) return
      armed.current = false
      const { draggingId, overIndex } = live.current
      setDrag({ draggingId: null, overIndex: null })
      if (!draggingId || overIndex === null) return

      const from = ids.indexOf(draggingId)
      if (from < 0) return
      // The insertion index counts positions in the original list, so removing
      // the dragged row first shifts everything below it up by one.
      //
      // KNOWN GAP: nothing exercises this branch. `overIndex > from` is a
      // downward drag; render-check drags the *second* project above the
      // first, which is upward and takes the `else`. And web/src/hooks has no
      // test file at all — every other pure-logic module here has one
      // (touchSelect, keys, label, meter, text, theme, styles, deps, harness),
      // which makes this the only piece of arithmetic in the frontend that
      // needed a comment to explain it and has nothing checking it.
      //
      // The line looks right and is not a reported bug. What it is, is the
      // untested half of a classic off-by-one, in a gesture people use
      // constantly, whose failure is silent: a project dropped one position
      // from where it was aimed reads as having aimed badly.
      //
      // A unit test is the cheap half — this is a pure function of
      // (ids, from, overIndex). A downward drag in render-check is the other.
      const to = overIndex > from ? overIndex - 1 : overIndex
      if (to === from) return

      const next = ids.slice()
      next.splice(from, 1)
      next.splice(to, 0, draggingId)
      onCommit(next)
    },
    // No dependency on the rendered state any more, so this handler survives a
    // whole gesture instead of being rebuilt on every pointermove.
    [ids, onCommit, setDrag],
  )

  return {
    ...state,
    register,
    handleProps: (id: string) => ({
      onPointerDown: onPointerDown(id),
      onPointerMove,
      onPointerUp: finish,
      onPointerCancel: finish,
      // Without this the browser scrolls the list instead of reporting the
      // drag, and on touch the gesture never reaches us at all.
      style: { touchAction: 'none' as const },
    }),
  }
}
