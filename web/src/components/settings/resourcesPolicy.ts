import type { ResourcePolicy } from '../../protocol/wire'

/**
 * Whether a poll's answer may be drawn: only if nothing was saved since the
 * poll was sent. A poll already in flight when somebody chose a mode answered
 * with the mode from before, and drawing it put the old choice back under their
 * cursor for two seconds.
 */
export function pollStillCurrent(askedAt: number, current: number): boolean {
  return askedAt === current
}

/** A policy as the server takes one: no boost, which has its own route. */
export function policyOnly(p: ResourcePolicy): ResourcePolicy {
  return {
    mode: p.mode,
    poolPercent: p.poolPercent,
    askPercent: p.askPercent,
    autoAct: p.autoAct,
    graceSeconds: p.graceSeconds,
  }
}

/**
 * A whole number the person is still typing, or null. Kept as text while it is
 * being edited: a number input bound to a number turned an emptied field into
 * 0 and the next keystroke into "015".
 */
export function wholeNumber(text: string): number | null {
  return /^\d{1,4}$/.test(text.trim()) ? Number(text.trim()) : null
}

