import { useCallback, useSyncExternalStore } from 'react'

/**
 * Tracks a media query.
 *
 * Layout decisions in this panel are made on width, never on user agent. A
 * browser window dragged narrow on a desktop and a phone in portrait need the
 * same layout; asking "is this a phone" gets that wrong in both directions.
 *
 * useSyncExternalStore rather than useState plus an effect: matchMedia is an
 * external store, and the effect version reads it one render after mount, so
 * the first paint is against the wrong breakpoint.
 */
export function useMediaQuery(query: string): boolean {
  const subscribe = useCallback(
    (onChange: () => void) => {
      const mq = window.matchMedia(query)
      mq.addEventListener('change', onChange)
      return () => mq.removeEventListener('change', onChange)
    },
    [query],
  )
  const getSnapshot = useCallback(() => window.matchMedia(query).matches, [query])
  return useSyncExternalStore(subscribe, getSnapshot)
}

/** Below this width the sidebar becomes an overlay instead of taking a column. */
export const NARROW_QUERY = '(max-width: 767px)'
