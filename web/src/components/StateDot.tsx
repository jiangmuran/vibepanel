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
export function StateDot({
  state,
  size = 10,
  onToggle,
}: {
  state: SessionState
  size?: number
  /**
   * Makes the indicator a button that flips between "done" and "waiting".
   *
   * Two positions rather than a cycle through all three, because there are
   * only two things a person wants from this control: get something out of the
   * way, or keep something at the top until they come back to it. Nobody
   * clicks to declare that a session is working.
   */
  onToggle?: (next: SessionState) => void
}) {
  const title = onToggle
    ? `${LABEL[state]} — click to mark as ${state === 'done' ? 'waiting' : 'done'}`
    : LABEL[state]
  const glyph = renderGlyph(state, size, title)
  if (!onToggle) return glyph
  return (
    <button
      type="button"
      data-testid="state-dot"
      data-state={state}
      title={title}
      onClick={(e) => {
        e.stopPropagation()
        onToggle(state === 'done' ? 'waiting' : 'done')
      }}
      // A generous hit area around a 10px glyph; the visual size is the point,
      // not the target size, and on a phone a 10px target is unusable.
      className="-m-1 flex shrink-0 items-center justify-center rounded p-1 transition-colors duration-200 ease-vp hover:bg-surface-2"
    >
      {glyph}
    </button>
  )
}

function renderGlyph(state: SessionState, size: number, title: string) {
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
