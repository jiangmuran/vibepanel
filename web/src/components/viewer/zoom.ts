/**
 * The image viewer's zoom, as numbers: fitted to the window, or a scale.
 * Pure, so the steps and the bounds are tested.
 */

export type Zoom = 'fit' | number

/** From a twentieth to sixteen times: past either end is a blur or a speck. */
export const MIN_ZOOM = 0.05
export const MAX_ZOOM = 16

export function clampZoom(z: number): number {
  if (!Number.isFinite(z)) return 1
  return Math.min(MAX_ZOOM, Math.max(MIN_ZOOM, z))
}

/**
 * One step in or out from a scale. Steps multiply, so in and then out is
 * where it started; `notches` lets a fast wheel take several at once.
 */
export function zoomAbout(scale: number, dir: 1 | -1, notches = 1): number {
  return clampZoom(scale * Math.pow(1.25, dir * Math.max(1, notches)))
}

/** A scale for the footer: 100%, 25%, 1600%. */
export function percent(z: number): string {
  return `${Math.round(z * 100)}%`
}
