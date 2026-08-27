import { t } from '../../i18n'

/**
 * The numbers a board puts on a wall, in the shapes a wall can read.
 *
 * Separate from the panel's own formatters because the constraint is different:
 * these are read at three metres, so a figure that needs a second glance to
 * parse has failed even when it is correct. 41,283,904 is correct and unusable;
 * 41.3M is what somebody standing up reads.
 */

/** A span of seconds, as "14m" or "3d 4h". */
export function duration(seconds: number): string {
  if (seconds < 60) return `${Math.max(0, Math.floor(seconds))}s`
  const d = Math.floor(seconds / 86400)
  const h = Math.floor((seconds % 86400) / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}m`
  return `${m}m`
}

/** How long ago, in words, for the line saying when the numbers were true. */
export function agoText(seconds: number): string {
  if (seconds < 2) return t('dash.agoNow')
  if (seconds < 60) return t('dash.agoSeconds', { n: Math.floor(seconds) })
  if (seconds < 3600) return t('dash.agoMinutes', { n: Math.floor(seconds / 60) })
  return t('dash.agoHours', { n: Math.floor(seconds / 3600) })
}

export function clockText(unixSeconds: number): string {
  return new Date(unixSeconds * 1000).toLocaleTimeString()
}

/**
 * A token count at wall size: 41.3M, 912K, 480.
 *
 * Deliberately not `toLocaleString`, which produces 41,283,904 — eight glyphs
 * and three separators, all of which have to be counted before the magnitude is
 * known. The exact figure is never lost: it is on the tile's own detail line,
 * for somebody who is close enough to want it.
 */
export function compact(n: number): string {
  const abs = Math.abs(n)
  if (abs < 1000) return String(Math.round(n))
  if (abs < 1_000_000) return `${trim(n / 1000)}K`
  if (abs < 1_000_000_000) return `${trim(n / 1_000_000)}M`
  return `${trim(n / 1_000_000_000)}B`
}

/** The same number in full, for the line under it. */
export function exact(n: number): string {
  return n.toLocaleString()
}

function trim(v: number): string {
  // One decimal below ten, none above: "9.4M" and "412M" are the same width to
  // read, and "412.3M" is a precision nobody asked for at this size.
  return v < 10 ? v.toFixed(1) : v.toFixed(0)
}

/**
 * A percentage for a meter, or null when nothing was measured.
 *
 * Null is not zero, and this is the third place in the codebase that has had to
 * say so. A CPU figure with no previous sample to difference against is not a
 * machine at rest.
 */
export function ratio(used: number, total: number): number | null {
  if (!Number.isFinite(used) || !Number.isFinite(total) || total <= 0) return null
  return Math.max(0, Math.min(100, (used / total) * 100))
}

/** A bucket label — "2026-08-23" or "2026-08" — in the reader's own format. */
export function bucketLabel(label: string): string {
  const parts = label.split('-')
  if (parts.length === 3) return `${Number(parts[1])}/${Number(parts[2])}`
  if (parts.length === 2) return `${parts[0].slice(2)}/${parts[1]}`
  return label
}
