import type { SessionState } from '../protocol/wire'
import { EXIT_VANISHED } from '../protocol/wire'

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
  exited,
  exitStatus = 0,
  onToggle,
}: {
  state: SessionState
  size?: number
  /**
   * The process is gone. Overrides the state glyph, because "what the task
   * was doing" stops being the useful thing to show once nothing is running.
   */
  exited?: boolean
  exitStatus?: number
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
  if (exited) {
    // Not a button. Marking a session with no process "waiting for you" says
    // something untrue about a thing that cannot change until it is restarted.
    return renderExited(size, exitStatus)
  }
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

/**
 * Two more shapes, for the two ways a process can be gone.
 *
 * A cross for a non-zero status and a hollow square for a clean one — both
 * unmistakable against the triangle, circle and check at 10px, which is what
 * red line 4 is about. The status itself goes in the label, because a shape
 * cannot carry the number and the number is what tells you whether to worry.
 */
function renderExited(size: number, status: number) {
  if (status === EXIT_VANISHED) {
    // Same shape family as a clean exit — a square reads as "not running" —
    // but dashed, so the two are told apart without relying on colour. The
    // distinction is real: a clean exit was watched happening, this one was
    // noticed afterwards, and the session may have been killed from a shell
    // while doing something important.
    const title = 'Gone — the tmux session no longer exists'
    return (
      <svg width={size} height={size} viewBox="0 0 10 10" role="img" aria-label={title}>
        <title>{title}</title>
        <rect
          x="1.4"
          y="1.4"
          width="7.2"
          height="7.2"
          rx="1.2"
          fill="none"
          stroke="var(--vp-state-dead)"
          strokeWidth="1.5"
          strokeDasharray="2 1.6"
        />
      </svg>
    )
  }
  const title = status === 0 ? 'Exited' : `Exited with status ${status}`
  const colour = status === 0 ? 'var(--vp-state-dead)' : 'var(--vp-state-crashed)'
  if (status === 0) {
    return (
      <svg width={size} height={size} viewBox="0 0 10 10" role="img" aria-label={title}>
        <title>{title}</title>
        <rect x="1.4" y="1.4" width="7.2" height="7.2" rx="1.2" fill="none" stroke={colour} strokeWidth="1.5" />
      </svg>
    )
  }
  return (
    <svg width={size} height={size} viewBox="0 0 10 10" role="img" aria-label={title}>
      <title>{title}</title>
      <path
        d="M1.8 1.8 L8.2 8.2 M8.2 1.8 L1.8 8.2"
        fill="none"
        stroke={colour}
        strokeWidth="1.8"
        strokeLinecap="round"
      />
    </svg>
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
  if (state === 'working') {
    return (
      <svg width={size} height={size} viewBox="0 0 10 10" role="img" aria-label={title} className="vp-breathe">
        <title>{title}</title>
        <circle cx="5" cy="5" r="4" fill="var(--vp-state-working)" />
      </svg>
    )
  }
  return unknownGlyph(state, size, title)
}

/**
 * A state this build does not know how to draw.
 *
 * The parameter is `never`, so the call above is the exhaustiveness check:
 * adding a member to SessionState without a branch for it stops the build.
 * This used to be an unconditional return of the breathing circle, which
 * type-checked forever — TypeScript had narrowed the union to 'working' by
 * then — and would have drawn a fourth state as the third one, silently.
 *
 * Deliberately not that circle. Red line 4 is that colour is never the only
 * carrier of meaning, so each state has a shape of its own; falling through to
 * working was one state wearing another's shape, which is the failure that
 * rule exists to prevent rather than a smaller version of it.
 *
 * It still draws something rather than returning null, because the value can
 * also arrive at runtime — a row written by a newer build, or an older one —
 * and a missing glyph is a hole in the sidebar that reads as nothing at all. A
 * hollow dashed ring is in the vocabulary of neither of the three.
 */
function unknownGlyph(state: never, size: number, title: string) {
  return (
    <svg width={size} height={size} viewBox="0 0 10 10" role="img" aria-label={title}>
      <title>{`${title} (unknown state: ${String(state)})`}</title>
      <circle
        cx="5"
        cy="5"
        r="4"
        fill="none"
        stroke="var(--vp-state-dead)"
        strokeWidth="1.5"
        strokeDasharray="2 1.5"
      />
    </svg>
  )
}
