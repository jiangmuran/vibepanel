import { getLang } from '../../../i18n'
import { formatAgo } from '../../panels/ago'

/**
 * "3d ago", "2h ago": short enough for a row, from unix seconds. Empty when
 * unknown. `now` is the server's clock from the same reading, so a browser
 * whose clock is wrong does not show a session as used in the future.
 */
export function shortAgo(unix: number, now: number): string {
  return formatAgo(unix, now, 'narrow')
}

/** The exact time, for a tooltip. */
export function exactTime(unix: number): string {
  if (!unix) return ''
  return new Date(unix * 1000).toLocaleString(getLang() === 'zh' ? 'zh-CN' : 'en-GB', {
    dateStyle: 'medium',
    timeStyle: 'short',
  })
}

/** Whether the person asked for no motion; checked when something would animate. */
export function reducedMotion(): boolean {
  return typeof window !== 'undefined' && window.matchMedia?.('(prefers-reduced-motion: reduce)').matches === true
}

/** How long a row takes to collapse; matches .vp-collapse. */
export const COLLAPSE_MS = 180

/** Wait for a row to finish leaving, or not at all without motion. */
export function afterCollapse(): Promise<void> {
  return new Promise((resolve) => window.setTimeout(resolve, reducedMotion() ? 0 : COLLAPSE_MS + 20))
}
