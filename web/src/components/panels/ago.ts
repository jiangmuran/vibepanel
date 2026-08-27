import { getLang } from '../../i18n'

/**
 * How long ago something was, in the reader's language.
 *
 * Lived in git.ts, which was fine while the repository tab was the only thing
 * with timestamps on it. Three panels have them now — a commit, a todo you
 * ticked, the last time the token reader finished a pass — and the third one
 * importing "./git" for a date is the point at which a helper has outgrown the
 * file it happened to be written in.
 */

/**
 * How long ago, as a value and a unit for Intl.RelativeTimeFormat.
 *
 * Through Intl rather than a table of suffixes in the dictionary: the browser
 * already knows that Chinese says 3 天前 and English says 3 days ago, including
 * the cases where one of them has a special word for it. A hand-written table
 * is two more dictionary lines per unit and gets "1 days ago" wrong.
 *
 * Always negative or zero, because everything here already happened. A commit
 * timestamped in the future — which happens, clocks differ across machines that
 * share a repository — is clamped to "now" rather than rendered as "in 3 hours",
 * which reads as the panel being broken.
 */
export function agoParts(when: number, now: number): { value: number; unit: Intl.RelativeTimeFormatUnit } {
  const secs = Math.min(0, when - now)
  const abs = Math.abs(secs)
  if (abs < 60) return { value: Math.round(secs), unit: 'second' }
  if (abs < 3600) return { value: Math.round(secs / 60), unit: 'minute' }
  if (abs < 86400) return { value: Math.round(secs / 3600), unit: 'hour' }
  if (abs < 2592000) return { value: Math.round(secs / 86400), unit: 'day' }
  if (abs < 31536000) return { value: Math.round(secs / 2592000), unit: 'month' }
  return { value: Math.round(secs / 31536000), unit: 'year' }
}

/**
 * The same, as a string.
 *
 * `numeric: 'auto'` is what gets "yesterday" rather than "1 day ago" in both
 * languages. A zero timestamp is "never happened" everywhere this is used —
 * a todo with no doneAt, a note the server has not written yet — and returns
 * the empty string rather than "56 years ago".
 */
export function formatAgo(when: number, now: number): string {
  if (!Number.isFinite(when) || when <= 0) return ''
  const { value, unit } = agoParts(when, now)
  const fmt = new Intl.RelativeTimeFormat(getLang() === 'zh' ? 'zh-CN' : 'en', { numeric: 'auto' })
  return fmt.format(value, unit)
}
