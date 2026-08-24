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
      setState({ draggingId: id, overIndex: null })
    },
    [],
  )

  const onPointerMove = useCallback(
    (e: React.PointerEvent) => {
      if (!armed.current || !state.draggingId) return
      if (Math.abs(e.clientY - startY.current) < THRESHOLD && state.overIndex === null) return
      setState((s) => ({ ...s, overIndex: indexForY(e.clientY) }))
    },
    [state.draggingId, state.overIndex, indexForY],
  )

  const finish = useCallback(
    (e: React.PointerEvent) => {
      if (e.currentTarget.hasPointerCapture(e.pointerId)) {
        e.currentTarget.releasePointerCapture(e.pointerId)
      }
      armed.current = false
      const { draggingId, overIndex } = state
      setState({ draggingId: null, overIndex: null })
      if (!draggingId || overIndex === null) return

      const from = ids.indexOf(draggingId)
      if (from < 0) return
      // The insertion index counts positions in the original list, so removing
      // the dragged row first shifts everything below it up by one.
      const to = overIndex > from ? overIndex - 1 : overIndex
      if (to === from) return

      const next = ids.slice()
      next.splice(from, 1)
      next.splice(to, 0, draggingId)
      onCommit(next)
    },
    [ids, state, onCommit],
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
