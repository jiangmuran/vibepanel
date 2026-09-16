import { useState } from 'react'

/**
 * What the chat page's forms share: the two button shapes the rest of the
 * product uses, the field classes, and the one hook every editor needs.
 *
 * The buttons are the sharing page's, not `vp-control`: that class is the
 * header's borderless icon button, and a row of them under a form reads as
 * a row of labels. A secondary action has a border; the one action that
 * commits is filled with the accent.
 */

const FIELD =
  'min-w-0 rounded-vp border border-hairline bg-surface-2 px-2 py-1.5 text-vp-md text-ink outline-none focus:border-accent'

export const INPUT = `w-full ${FIELD}`
export const INPUT_SHORT = FIELD
export const SELECT = FIELD

export function Secondary({
  children,
  ...rest
}: React.ButtonHTMLAttributes<HTMLButtonElement> & { children: React.ReactNode }) {
  return (
    <button
      type="button"
      {...rest}
      className={`vp-press flex items-center gap-1.5 rounded-vp border border-hairline px-2 py-1 text-vp-sm text-ink hover:border-accent disabled:opacity-40 ${rest.className ?? ''}`}
    >
      {children}
    </button>
  )
}

export function Primary({
  children,
  ...rest
}: React.ButtonHTMLAttributes<HTMLButtonElement> & { children: React.ReactNode }) {
  return (
    <button
      type="button"
      {...rest}
      className={`vp-press flex items-center gap-1.5 rounded-vp px-3 py-1.5 text-vp-base disabled:opacity-40 ${rest.className ?? ''}`}
      style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
    >
      {children}
    </button>
  )
}

/**
 * A local copy of something the server keeps sending.
 *
 * The page polls, and every poll brings the server's copy of what a form is
 * editing. The copy replaces the form only while nothing has been typed: a
 * poll landing mid-edit must not put a field back. Done during render, as
 * React's "adjusting state when a prop changes" pattern, rather than in an
 * effect that would render twice and that the lint rule refuses.
 */
export function useServerCopy<T>(server: T, dirty: boolean): [T, (next: T | ((prev: T) => T)) => void] {
  const [local, setLocal] = useState(server)
  const [seen, setSeen] = useState(server)
  if (seen !== server) {
    setSeen(server)
    if (!dirty) setLocal(server)
  }
  return [local, setLocal]
}

export function errText(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}
