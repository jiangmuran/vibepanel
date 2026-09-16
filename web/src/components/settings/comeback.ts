/**
 * Waiting for the panel to come back after it was told to go.
 *
 * Shared by the restart button and the updater, which used to have two copies
 * of the same loop -- and the updater's was the one with the bug, because it
 * was written second and left out the delay at the start.
 *
 * Polled rather than timed. How long a restart takes is the supervisor's
 * business -- systemd's RestartSec is three seconds and a slow machine adds
 * more -- and a fixed wait is either a lie or a delay.
 *
 * Two ways to know it is back, and the second is the one an update needs. The
 * old process answers /api/health right up until it stops, so a poll that
 * starts now succeeds against the panel being replaced and reloads into a
 * socket that is about to close; hence the pause before the first try and the
 * requirement that health has been *unreachable* at least once. But a restart
 * can be quick enough to fall between two polls, so a health answer whose
 * build differs from the one that was running counts as back on its own.
 */
export interface Comeback {
  /** The build that was running, as `version@commit`, or null to not compare. */
  was: string | null
  /** Called once the new process answers. */
  onBack: () => void
  /** Called when it has not answered within the budget. */
  onGaveUp: () => void
  /** Overridable for tests. */
  fetchHealth?: () => Promise<{ version: string; commit: string } | null>
  setTimeout?: (fn: () => void, ms: number) => void
}

export const COMEBACK_FIRST_MS = 1500
export const COMEBACK_EVERY_MS = 500
/** Ninety seconds: a slow box under memory pressure, not a stuck one. */
export const COMEBACK_TRIES = 180

export function waitForItToComeBack(opts: Comeback): void {
  const later = opts.setTimeout ?? ((fn, ms) => void window.setTimeout(fn, ms))
  const health = opts.fetchHealth ?? defaultHealth
  let tries = 0
  let wasDown = false
  const tick = () => {
    tries++
    void health().then((h) => {
      if (h === null) {
        wasDown = true
      } else {
        const build = `${h.version}@${h.commit}`
        if (wasDown || (opts.was !== null && build !== opts.was)) {
          opts.onBack()
          return
        }
      }
      if (tries < COMEBACK_TRIES) later(tick, COMEBACK_EVERY_MS)
      else opts.onGaveUp()
    })
  }
  later(tick, COMEBACK_FIRST_MS)
}

async function defaultHealth(): Promise<{ version: string; commit: string } | null> {
  try {
    const r = await fetch('/api/health', { cache: 'no-store' })
    if (!r.ok) return null
    return (await r.json()) as { version: string; commit: string }
  } catch {
    return null
  }
}
