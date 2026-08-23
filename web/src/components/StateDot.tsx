import type { SessionState } from '../protocol/wire'

const LABEL: Record<SessionState, string> = {
  waiting: 'Waiting for you',
  working: 'Working',
  done: 'Done',
}

/**
 * The session state indicator.
 *
 * Shape carries the meaning as much as colour does: a triangle for waiting, a
 * breathing circle for working, a check for done. Colour alone fails for
 * colour-blind users, and this panel is read at a glance in a dark room — which
 * is exactly when hue discrimination is worst.
 */
export function StateDot({ state, size = 10 }: { state: SessionState; size?: number }) {
  const title = LABEL[state]
  if (state === 'waiting') {
    return (
      <svg width={size} height={size} viewBox="0 0 10 10" role="img" aria-label={title}>
        <title>{title}</title>
        <path d="M5 0.5 L9.5 9 L0.5 9 Z" fill="var(--vp-state-waiting)" />
      </svg>
    )
  }
  if (state === 'done') {
    return (
      <svg width={size} height={size} viewBox="0 0 10 10" role="img" aria-label={title}>
        <title>{title}</title>
        <path
          d="M1.5 5.2 L4 7.7 L8.5 2.5"
          fill="none"
          stroke="var(--vp-state-done)"
          strokeWidth="1.8"
          strokeLinecap="round"
          strokeLinejoin="round"
        />
      </svg>
    )
  }
  return (
    <svg width={size} height={size} viewBox="0 0 10 10" role="img" aria-label={title} className="vp-breathe">
      <title>{title}</title>
      <circle cx="5" cy="5" r="4" fill="var(--vp-state-working)" />
    </svg>
  )
}
