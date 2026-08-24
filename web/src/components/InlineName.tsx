import { useEffect, useRef, useState } from 'react'

interface Props {
  value: string
  onCommit: (next: string) => void
  className?: string
  title?: string
}

/**
 * A label that becomes an input on double click.
 *
 * Renaming is the single most requested thing this panel does — the whole
 * reason it exists is that tabs called "bash" are useless — so it has to be
 * two clicks away, not behind a dialog.
 */
export function InlineName({ value, onCommit, className, title }: Props) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(value)
  const inputRef = useRef<HTMLInputElement | null>(null)

  // The draft is seeded when editing starts rather than synced from the prop.
  // Syncing would also mean that a rename arriving from another viewer, or the
  // automatic namer picking a new title, overwrites whatever this person is
  // halfway through typing.
  const startEditing = () => {
    setDraft(value)
    setEditing(true)
  }

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
        className={`min-w-0 rounded border border-accent bg-surface px-1 outline-none ${className ?? ''}`}
      />
    )
  }

  return (
    <span
      className={`min-w-0 truncate ${className ?? ''}`}
      title={title ?? 'Double click to rename'}
      onDoubleClick={(e) => {
        e.stopPropagation()
        startEditing()
      }}
    >
      {value}
    </span>
  )
}
