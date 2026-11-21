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
  'min-w-0 rounded-md border border-hairline-strong bg-bg px-2.5 py-1.5 text-vp-base text-ink outline-none transition-colors duration-100 ease-vp focus:border-accent'

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
      className={`vp-outline text-vp-sm ${rest.className ?? ''}`}
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
      className={`vp-primary text-vp-sm ${rest.className ?? ''}`}
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
