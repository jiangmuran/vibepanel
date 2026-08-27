import { useEffect, useRef, useState } from 'react'

interface Props {
  value: string
  onCommit: (next: string) => void
  className?: string
  title?: string
}

/** How long a finger has to stay put before it means "rename". */
const LONG_PRESS_MS = 500

/** How far it may drift first, in css pixels, before it means "scroll". */
const LONG_PRESS_SLOP = 10

/**
 * A label that becomes an input on double click, or on a long press.
 *
 * Renaming is the single most requested thing this panel does — the whole
 * reason it exists is that tabs called "bash" are useless — so it has to be
 * two clicks away, not behind a dialog.
 *
 * The long press is not a nicety. On a narrow screen the list is an overlay,
 * and choosing a session closes it, so the *first* tap of a double tap
 * dismisses the thing being tapped: a second tap never arrives, and renaming
 * from a phone was impossible rather than merely awkward. Nothing else in a
 * session row uses a press-and-hold, so the gesture was free.
 */
export function InlineName({ value, onCommit, className, title }: Props) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(value)
  const inputRef = useRef<HTMLInputElement | null>(null)
  const pressRef = useRef<{ timer: number; x: number; y: number } | null>(null)

  // The draft is seeded when editing starts rather than synced from the prop.
  // Syncing would also mean that a rename arriving from another viewer, or the
  // automatic namer picking a new title, overwrites whatever this person is
  // halfway through typing.
  const startEditing = () => {
    setDraft(value)
    setEditing(true)
  }

  const cancelPress = () => {
    if (pressRef.current) {
      clearTimeout(pressRef.current.timer)
      pressRef.current = null
    }
  }

  /**
   * Swallow the click that the press is about to produce.
   *
   * Releasing after a long press still fires a click, and it lands on the row
   * rather than on this label because the element under the finger changed
   * between down and up. The row's handler selects the session, which on a
   * phone closes the drawer and takes the input with it — so the rename would
   * open and vanish in the same gesture. Capture phase, so it runs before the
   * row's own listener, and a timeout in case no click ever arrives.
   */
  const swallowNextClick = () => {
    const handler = (e: MouseEvent) => {
      e.stopPropagation()
      e.preventDefault()
      window.removeEventListener('click', handler, true)
    }
    window.addEventListener('click', handler, true)
    window.setTimeout(() => window.removeEventListener('click', handler, true), 700)
  }

  useEffect(() => cancelPress, [])

  useEffect(() => {
    if (editing && inputRef.current) {
      inputRef.current.focus()
      inputRef.current.select()
    }
  }, [editing])

  const commit = () => {
    setEditing(false)
    const next = draft.trim()
    // An empty name is a mistake, not an instruction to clear it: committing it
    // would leave a row the user cannot identify or click back into.
    if (next && next !== value) onCommit(next)
  }

  if (editing) {
    return (
      <input
        ref={inputRef}
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onBlur={commit}
        onKeyDown={(e) => {
          e.stopPropagation()
          if (e.key === 'Enter') commit()
          if (e.key === 'Escape') setEditing(false)
        }}
        onClick={(e) => e.stopPropagation()}
        onDoubleClick={(e) => e.stopPropagation()}
        className={`min-w-0 rounded-md border border-accent bg-surface px-1 outline-none ${className ?? ''}`}
      />
    )
  }

  return (
    <span
      data-testid="inline-name"
      className={`min-w-0 truncate ${className ?? ''}`}
      title={title ?? 'Double click, or press and hold, to rename'}
      onDoubleClick={(e) => {
        e.stopPropagation()
        startEditing()
      }}
      onPointerDown={(e) => {
        // Only a primary press. A right click has its own meaning and a
        // secondary touch point is part of some other gesture.
        if (e.button !== 0) return
        cancelPress()
        const timer = window.setTimeout(() => {
          pressRef.current = null
          swallowNextClick()
          startEditing()
        }, LONG_PRESS_MS)
        pressRef.current = { timer, x: e.clientX, y: e.clientY }
      }}
      onPointerMove={(e) => {
        const p = pressRef.current
        if (!p) return
        // A finger that travels is scrolling the list, not holding still.
        if (Math.abs(e.clientX - p.x) > LONG_PRESS_SLOP ||
            Math.abs(e.clientY - p.y) > LONG_PRESS_SLOP) {
          cancelPress()
        }
      }}
      onPointerUp={cancelPress}
      onPointerCancel={cancelPress}
      onPointerLeave={cancelPress}
    >
      {value}
    </span>
  )
}
