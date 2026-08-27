import type { UsageDay, UsageTotals } from '../../protocol/wire'

/**
 * The arithmetic and the layout behind the token panel, kept out of the
 * components so it can be tested without a DOM.
 *
 * Every function here has the same rule: a value that is not known is `null`,
 * never `0`. The whole feature exists because "I could not read the file" and
 * "nothing was spent" are different answers, and a number type that cannot tell
 * them apart is how they stop being different.
 */

/** Every token an entry was billed for, cache included. */
export function totalOf(t: UsageTotals): number {
  return t.input + t.output + t.cacheRead + t.cacheWrite
}

/**
 * A compact token count.
 *
 * Three significant figures and a suffix, because the column this sits in is
 * a hundred pixels wide and 812,004,112 is not a number anybody reads. The
 * exact figure is on the row's title, so nothing is lost — only folded.
 *
 * Not localised: k/M/B are the same in both languages the panel speaks, and a
 * separate Chinese scale (万/亿) would make two columns of the same table
 * incomparable at a glance.
 */
export function compact(n: number): string {
  const abs = Math.abs(n)
  if (abs < 1000) return String(n)
  const units: [number, string][] = [
    [1e9, 'B'],
    [1e6, 'M'],
    [1e3, 'k'],
  ]
  for (const [scale, suffix] of units) {
    if (abs >= scale) {
      const v = n / scale
      return `${v >= 100 ? v.toFixed(0) : v.toFixed(1)}${suffix}`
    }
  }
  return String(n)
}

/** A full count with thousands separators, for titles and wide cells. */
export function exact(n: number): string {
  return n.toLocaleString('en-US')
}

/** One square in the year grid. */
export interface HeatCell {
  /** YYYY-MM-DD. */
  day: string
  /** Total tokens, or null for a day outside the range the server sent. */
  total: number | null
  /** 0–4. Zero is "a day with nothing on it", which is a real reading. */
  level: number
}

/** A column of the grid: seven days, Sunday first, oldest column first. */
export interface HeatWeek {
  cells: (HeatCell | null)[]
}

/**
 * Bucket boundaries for the five shades, derived from the data rather than
 * fixed.
 *
 * Fixed thresholds cannot work here: a week of light use and a week of an
 * agent running overnight differ by three orders of magnitude, and any constant
 * either flattens one to a single shade or saturates the other. Quartiles of
 * the *non-empty* days keep the grid readable at both.
 *
 * Empty days are excluded from the quantiles on purpose. Including them, on a
 * year where most days are empty, puts every quartile at zero and paints every
 * working day the darkest shade.
 */
export function levels(days: UsageDay[]): number[] {
  const spent = days
    .map(totalOf)
    .filter((n) => n > 0)
    .sort((a, b) => a - b)
  if (spent.length === 0) return [0, 0, 0, 0]
  const at = (q: number) => spent[Math.min(spent.length - 1, Math.floor(spent.length * q))]
  return [at(0.25), at(0.5), at(0.75), at(0.9)]
}

/** Which of the five shades a total falls in. */
export function levelOf(total: number, cuts: number[]): number {
  if (total <= 0) return 0
  for (let i = 0; i < cuts.length; i++) {
    if (total <= cuts[i]) return i + 1
  }
  return 4
}

/** A date as YYYY-MM-DD in the *local* calendar. */
function isoDay(d: Date): string {
  const m = String(d.getMonth() + 1).padStart(2, '0')
  const day = String(d.getDate()).padStart(2, '0')
  return `${d.getFullYear()}-${m}-${day}`
}

/**
 * Lays the last `span` days out as GitHub does: one column per week, Sunday at
 * the top, the current week last.
 *
 * `today` comes from the server rather than from `new Date()`. The buckets are
 * the server's local days, and a phone in another timezone that decided for
 * itself would put the last column one square out — a grid that is subtly
 * wrong is worse than one that is obviously wrong.
 *
 * Days the server did not send are `null` totals, not zeros: the range starts
 * where the range starts, and the leading squares of the first column are days
 * that are simply not in it.
 */
export function weeks(days: UsageDay[], today: string, span: number): HeatWeek[] {
  const cuts = levels(days)
  const byDay = new Map(days.map((d) => [d.day, totalOf(d)]))

  // Parsed as a local date, digit by digit. `new Date('2026-08-27')` is parsed
  // as UTC and lands on the previous day for anyone west of Greenwich, which
  // shifts the entire grid by one square.
  const [y, m, d] = today.split('-').map(Number)
  const end = new Date(y, (m || 1) - 1, d || 1)

  const start = new Date(end)
  start.setDate(start.getDate() - (span - 1))
  // Back up to the Sunday on or before the start, so every column is a whole
  // week and the day-of-week rows line up all the way across.
  start.setDate(start.getDate() - start.getDay())

  const out: HeatWeek[] = []
  const cursor = new Date(start)
  while (cursor <= end) {
    const cells: (HeatCell | null)[] = []
    for (let i = 0; i < 7; i++) {
      if (cursor > end) {
        // The current week, past today. Rendered as a gap rather than as an
        // empty day: tomorrow has not happened, and drawing it as "nothing was
        // spent" is a claim about the future.
        cells.push(null)
      } else {
        const day = isoDay(cursor)
        const total = byDay.has(day) ? (byDay.get(day) as number) : null
        cells.push({ day, total, level: total === null ? 0 : levelOf(total, cuts) })
      }
      cursor.setDate(cursor.getDate() + 1)
    }
    out.push({ cells })
  }
  return out
}

/**
 * Where each month label goes: the index of the first column whose first day
 * falls in a new month.
 *
 * Skips a month whose first column is the one already labelled, which is what
 * stops two labels overlapping at the left edge of a 53-week grid.
 */
export function monthLabels(grid: HeatWeek[]): { index: number; month: number }[] {
  const out: { index: number; month: number }[] = []
  let last = -1
  grid.forEach((week, index) => {
    const first = week.cells.find((c) => c !== null)
    if (!first) return
    const month = Number(first.day.slice(5, 7))
    if (month === last) return
    if (out.length > 0 && index - out[out.length - 1].index < 3) return
    out.push({ index, month })
    last = month
  })
  return out
}

/** Sums a range of days. */
export function sum(days: UsageDay[]): UsageTotals {
  return days.reduce(
    (acc, d) => ({
      input: acc.input + d.input,
      output: acc.output + d.output,
      cacheRead: acc.cacheRead + d.cacheRead,
      cacheWrite: acc.cacheWrite + d.cacheWrite,
      requests: acc.requests + d.requests,
    }),
    { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, requests: 0 },
  )
}
