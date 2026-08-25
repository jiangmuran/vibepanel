/**
 * What a meter reads when its value is not known yet.
 *
 * Null is not zero. The CPU figure is a difference between two samples, so the
 * first one has nothing to compare against — and it was rendered as "0%", next
 * to a detail line reading "sampling…". A number nobody had measured was being
 * shown as a measurement, and the measurement it claimed was "this machine is
 * idle". The comment above that line even said it must not do that; the code
 * three characters to the right did it anyway.
 */
export function meterText(value: number | null): string {
  return value === null ? '—' : `${clamp(value).toFixed(0)}%`
}

/**
 * How wide the bar is drawn.
 *
 * An unknown value draws nothing. A zero-width bar is not the same claim as a
 * bar at zero percent, because the number beside it reads "—" rather than "0%".
 */
export function meterWidth(value: number | null): number {
  return value === null ? 0 : clamp(value)
}

function clamp(value: number): number {
  return Math.max(0, Math.min(100, value))
}
