import type { TokenUsage, UsageDay, UsageTotals } from '../../protocol/wire'
import { totalOf } from './tokens'

/**
 * The six figures the side panel leads with, worked out from one payload.
 *
 * 「几个数字 有布局（本周消耗、本项目消耗、今日消耗、分应用消耗、时间、字数）」.
 *
 * Kept out of the component for the reason tokens.ts is: none of it needs a
 * DOM, and the parts worth getting wrong are the date arithmetic and the "not
 * known is not zero" rule, neither of which a screenshot would show.
 *
 * One request feeds all six. The panel asks for the range with no project and
 * no tool filter, because the payload already carries the per-project and
 * per-tool splits — asking three times for three scopes would be three
 * transcript passes to answer one glance, and the three answers would be from
 * three different moments.
 *
 * Every figure is over the same window, and the window is stated on screen.
 * Mixing "today", "this week" and "this project, all time" in one block reads
 * as three facts about one thing and is three facts about three; a footer that
 * names one period is what makes them comparable.
 */

/** A date as YYYY-MM-DD in the local calendar. Same rule as tokens.ts. */
function isoDay(d: Date): string {
  const m = String(d.getMonth() + 1).padStart(2, '0')
  const day = String(d.getDate()).padStart(2, '0')
  return `${d.getFullYear()}-${m}-${day}`
}

/**
 * `today` minus `back` days, as YYYY-MM-DD.
 *
 * Parsed digit by digit and built as a local date. `new Date('2026-08-27')` is
 * parsed as UTC and lands on the previous day for anyone west of Greenwich,
 * which would silently drop a day off every window here. tokens.ts says the
 * same thing about the heatmap and for the same reason.
 *
 * An unparseable `today` returns the empty string, which every caller below
 * reads as "no window" rather than as a window starting in 1970.
 */
export function dayBefore(today: string, back: number): string {
  const [y, m, d] = today.split('-').map(Number)
  if (!Number.isFinite(y) || !Number.isFinite(m) || !Number.isFinite(d)) return ''
  const at = new Date(y, m - 1, d)
  at.setDate(at.getDate() - back)
  return isoDay(at)
}

/**
 * What this project spent over the range, as both readings, or null when
 * there is no answer.
 *
 * Two different absences, and neither is a zero. No project selected is null:
 * a figure with nothing scoping it, beside a project's name, is the panel
 * answering a question it was not asked. A project the payload's range never
 * saw is null as well: the row is absent because the window does not reach
 * whatever was spent in it, which an em dash says and a zero does not.
 */
export function projectFigures(
  data: TokenUsage,
  projectId: string | null,
): { total: number; output: number } | null {
  if (!projectId) return null
  const row = data.projects.find((p) => p.id === projectId)
  return row ? { total: totalOf(row), output: row.output } : null
}

/** One agent's share of the range. */
export interface ToolShare {
  tool: string
  total: number
  /** 0-1 of everything the tools spent between them. */
  share: number
  /** What the total is made of. See the note on `share` below. */
  output: number
  cacheRead: number
}

/**
 * Who spent it, largest first.
 *
 * Tools that spent nothing are dropped rather than drawn as a zero-width
 * segment: a legend entry reading 0% is a line that says nothing, and a
 * segment too small to see is a segment nobody can aim at. A tool that could
 * not be read at all never reaches `byTool`; the detail view is where that is
 * reported, because it is a reason and not a number.
 *
 * `share` is against the sum of the tools, not against the range total. They
 * are the same number in every honest payload and they diverge when one of
 * them is a lower bound — and a bar whose segments do not fill it, with
 * nothing explaining the gap, reads as a rendering fault rather than as
 * missing data.
 *
 * `output` and `cacheRead` come along because the share alone is read as
 * something it is not. Measured on the machine this was written on, over
 * 33.7B tokens for claude and 7.3B for codex: cache reads are 99.0% and 96.0%
 * of those totals. So a bar reading 82/18 is very largely a comparison of how
 * much context each agent re-read, and the same two agents are 73/27 by output
 * and 64/36 by requests. Every one of those is a true statement about tokens
 * and they are not the same statement, so the composition travels with the
 * share and the tooltip says which is which. The bar keeps billed tokens: it
 * is the one figure that is each vendor's own accounting rather than the
 * panel's choice of what counts as work.
 */
export function toolShares(data: TokenUsage): ToolShare[] {
  const rows = data.byTool
    .map((t) => ({ tool: t.tool, total: totalOf(t), output: t.output, cacheRead: t.cacheRead }))
    .filter((t) => t.total > 0)
    .sort((a, b) => b.total - a.total)
  const sum = rows.reduce((n, r) => n + r.total, 0)
  if (sum <= 0) return []
  return rows.map((r) => ({ ...r, share: r.total / sum }))
}

/**
 * The last `span` days as a series, oldest first, with gaps as zeros.
 *
 * Gaps matter: `byDay` only carries days that had something on them, so a
 * quiet Sunday is absent rather than zero. Drawing the array as it arrives
 * joins Saturday to Monday and hides the quiet day entirely — the shape of a
 * week with a day off is the shape somebody is looking at the chart for.
 */
export function daySeries(days: UsageDay[], today: string, span: number): number[] {
  const by = new Map(days.map((d) => [d.day, totalOf(d)]))
  const out: number[] = []
  for (let i = span - 1; i >= 0; i--) {
    const day = dayBefore(today, i)
    out.push(day === '' ? 0 : (by.get(day) ?? 0))
  }
  return out
}

/**
 * Which reading of a row a figure is.
 *
 * Two, and they are shown side by side rather than one standing in for the
 * other. `total` is what the agents were billed for and is almost entirely
 * cache reads -- 97% on the machine this was written on -- so on its own it
 * moves with how much context was re-read, not with how much work was done.
 * `output` is what the models produced. Neither is the right one to drop.
 */
export type Metric = 'total' | 'output'

/** One row read as `metric`. */
function valueOf(t: UsageTotals, metric: Metric): number {
  return metric === 'output' ? t.output : totalOf(t)
}

/** One day's value, or 0 for a day with no row. */
export function dayValue(days: UsageDay[], day: string, metric: Metric): number {
  const found = days.find((d) => d.day === day)
  return found ? valueOf(found, metric) : 0
}

/**
 * One reading over the `span` days ending at `today`, inclusive.
 *
 * String comparison on YYYY-MM-DD is a date comparison, which is the one thing
 * that format is for. The window is closed at both ends: a payload whose range
 * is longer than the window must not have its older days counted, and a clock
 * ahead of the server's must not pull in a day the server calls tomorrow.
 *
 * `output` is the panel's reading of 「字数」, and a reading rather than the
 * thing: a token is not a character in any language, and turning one into the
 * other would need a per-model tokeniser the panel does not have. It is the one
 * column of the four that is unambiguously production, and it is labelled
 * output.
 */
export function windowValue(days: UsageDay[], today: string, span: number, metric: Metric): number {
  const from = dayBefore(today, span - 1)
  if (from === '') return 0
  let sum = 0
  for (const d of days) {
    if (d.day >= from && d.day <= today) sum += valueOf(d, metric)
  }
  return sum
}

/** A day on the chart: its label and its value, gaps filled with zero. */
export interface DayPoint {
  day: string
  value: number
}

/**
 * The last `span` days as points, oldest first. See daySeries for why gaps are
 * zeros rather than absences.
 */
export function dayPoints(days: UsageDay[], today: string, span: number, metric: Metric): DayPoint[] {
  const by = new Map(days.map((d) => [d.day, valueOf(d, metric)]))
  const out: DayPoint[] = []
  for (let i = span - 1; i >= 0; i--) {
    const day = dayBefore(today, i)
    if (day !== '') out.push({ day, value: by.get(day) ?? 0 })
  }
  return out
}

/**
 * The average of the finished days that have anything on them, and today
 * against it.
 *
 * Today is left out of the average: it is half a day, and folding it in pulls
 * the baseline down every morning and makes every afternoon look unusual.
 * Empty days are left out as well, so a week off does not halve the average
 * of the weeks worked. Null when there is nothing to compare against.
 */
export function pace(points: DayPoint[], today: string): { average: number; ratio: number } | null {
  const past = points.filter((p) => p.day !== today && p.value > 0)
  if (past.length === 0) return null
  const average = past.reduce((n, p) => n + p.value, 0) / past.length
  const now = points.find((p) => p.day === today)?.value ?? 0
  return { average, ratio: average > 0 ? now / average : 0 }
}

/**
 * The points from the first one with a value, keeping at least `min` at the
 * end. What comes before the first reading is not a quiet stretch; it is
 * before there was anything to read.
 */
export function sinceFirst(points: DayPoint[], min: number): DayPoint[] {
  const first = points.findIndex((p) => p.value > 0)
  if (first < 0) return points
  return points.slice(Math.min(first, Math.max(0, points.length - min)))
}

/** `2026-09-14` as `9/14`, for an axis. */
export function shortDay(day: string): string {
  const [, m, d] = day.split('-').map(Number)
  if (!Number.isFinite(m) || !Number.isFinite(d)) return day
  return `${m}/${d}`
}

/**
 * Which points get a date under them: the first, the last, and evenly between,
 * at most `count` in all. Indices, oldest first.
 */
export function axisTicks(length: number, count: number): number[] {
  if (length <= 0) return []
  if (length === 1 || count <= 1) return [length - 1]
  const n = Math.min(count, length)
  const out = new Set<number>()
  for (let i = 0; i < n; i++) out.add(Math.round((i * (length - 1)) / (n - 1)))
  return [...out]
}

/** A ranked row, with its share of the rows shown. */
export interface Ranked {
  key: string
  label: string
  hint: string
  total: number
  output: number
  share: number
}

/**
 * Rows largest first by `metric`, empty ones dropped, each with its share of
 * the whole list -- not of the rows kept after a cut, so "top six" still reads
 * as a fraction of everything.
 */
export function rank(
  rows: { key: string; label: string; hint: string; t: UsageTotals }[],
  metric: Metric,
): Ranked[] {
  const kept = rows
    .map((r) => ({ key: r.key, label: r.label, hint: r.hint, total: totalOf(r.t), output: r.t.output, v: valueOf(r.t, metric) }))
    .filter((r) => r.v > 0)
    .sort((a, b) => b.v - a.v)
  const sum = kept.reduce((n, r) => n + r.v, 0)
  return kept.map(({ v, ...r }) => ({ ...r, share: sum > 0 ? v / sum : 0 }))
}
