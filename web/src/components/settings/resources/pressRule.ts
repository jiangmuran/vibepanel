/**
 * Pressing twice to confirm, without a dialog.
 *
 * Ending a session is one press away from the list that shows it, and a modal
 * for each of a dozen rows is what made this page slow to use. The first press
 * arms the button and says so in words; the second, inside the window, does
 * it. A real double-click therefore confirms, which is what was asked for, and
 * anything else -- time, Escape, focus leaving, a press elsewhere -- puts it
 * back without having done anything.
 *
 * Plain state and a timer, not a hook, so the rules can be tested with fake
 * timers in a node test (vitest here runs without a DOM).
 */

/** How long the armed button waits, and how long its fill takes to drain. */
export const CONFIRM_WINDOW_MS = 3000

export type PressPhase = 'idle' | 'armed' | 'running'

export type PressEvent = 'press' | 'timeout' | 'cancel' | 'settled'

/**
 * The whole rule. `run` is true on the one transition that does the thing: the
 * second press.
 *
 * `running` ignores everything but its end: a third click while a request is
 * in flight must not send another, and Escape or a blur must not make the
 * button look as though the request had been called off.
 */
export function pressStep(phase: PressPhase, event: PressEvent): { phase: PressPhase; run: boolean } {
  switch (phase) {
    case 'idle':
      return { phase: event === 'press' ? 'armed' : 'idle', run: false }
    case 'armed':
      if (event === 'press') return { phase: 'running', run: true }
      if (event === 'timeout' || event === 'cancel') return { phase: 'idle', run: false }
      return { phase: 'armed', run: false }
    case 'running':
      return { phase: event === 'settled' ? 'idle' : 'running', run: false }
  }
}

export class ConfirmPress {
  phase: PressPhase = 'idle'
  private timer: ReturnType<typeof setTimeout> | undefined
  private disposed = false

  constructor(
    private readonly act: () => Promise<unknown>,
    private readonly changed: (phase: PressPhase) => void,
    private readonly windowMs = CONFIRM_WINDOW_MS,
  ) {}

  press(): void {
    this.step('press')
  }

  /** Escape, focus leaving, a press elsewhere. */
  cancel(): void {
    this.step('cancel')
  }

  dispose(): void {
    this.disposed = true
    clearTimeout(this.timer)
  }

  private step(event: PressEvent): void {
    if (this.disposed) return
    const next = pressStep(this.phase, event)
    const was = this.phase
    this.phase = next.phase
    if (was !== next.phase) this.changed(next.phase)
    clearTimeout(this.timer)
    if (next.phase === 'armed') this.timer = setTimeout(() => this.step('timeout'), this.windowMs)
    if (next.run) {
      // A rejection is the caller's to show: this only has to come back to
      // idle, so the button is usable again where the row says what failed.
      void this.act()
        .catch(() => undefined)
        .finally(() => this.step('settled'))
    }
  }
}
