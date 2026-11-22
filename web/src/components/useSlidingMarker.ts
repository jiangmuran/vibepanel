import { useLayoutEffect, useState } from 'react'
import type { RefObject } from 'react'

interface MarkerBox {
  left: number
  top: number
  width: number
  height: number
}

/**
 * Measures the active tab (`[aria-selected="true"]` or `[data-active="true"]`)
 * inside a horizontal segmented track or a vertical rail so a single pill
 * glides smoothly across either axis.
 *
 * Returns `null` when nothing is laid out yet (hidden ancestor or headless
 * probe), letting the stylesheet fall back to the button's own active fill.
 */
export function useSlidingMarker(
  trackRef: RefObject<HTMLElement | null>,
  depsKey: string,
): MarkerBox | null {
  const [box, setBox] = useState<MarkerBox | null>(null)

  useLayoutEffect(() => {
    const track = trackRef.current
    if (!track) return
    const find = () =>
      track.querySelector<HTMLElement>('[aria-selected="true"], [data-active="true"]')
    const measure = () => {
      const el = find()
      if (!el || el.offsetWidth <= 0 || el.offsetHeight <= 0) {
        setBox(null)
        return
      }
      setBox((prev) => {
        const left = el.offsetLeft
        const top = el.offsetTop
        const width = el.offsetWidth
        const height = el.offsetHeight
        if (
          prev &&
          prev.left === left &&
          prev.top === top &&
          prev.width === width &&
          prev.height === height
        ) {
          return prev
        }
        return { left, top, width, height }
      })
    }
    measure()
    const ro = new ResizeObserver(measure)
    ro.observe(track)
    const active = find()
    if (active) ro.observe(active)
    return () => ro.disconnect()
  }, [trackRef, depsKey])

  return box
}
