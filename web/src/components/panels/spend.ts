import type { TokenUsage, UsageDay } from '../../protocol/wire'
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
 * Everything spent in the `span` days ending at `today`, inclusive.
 *
 * String comparison on YYYY-MM-DD is a date comparison, which is the one thing
 * that format is for. The window is closed at both ends: a payload whose range
 * is longer than the window must not have its older days counted, and a clock
 * ahead of the server's must not pull in a day the server calls tomorrow.
 */
export function windowTotal(days: UsageDay[], today: string, span: number): number {
  const from = dayBefore(today, span - 1)
  if (from === '') return 0
  let sum = 0
  for (const d of days) {
    if (d.day >= from && d.day <= today) sum += totalOf(d)
  }
  return sum
}

/** Everything one day cost, or 0 for a day with no row. */
export function dayTotal(days: UsageDay[], day: string): number {
  const found = days.find((d) => d.day === day)
  return found ? totalOf(found) : 0
}

/**
 * What this project cost over the range, or null when there is no answer.
 *
 * Two different absences, and neither is a zero. No project selected is null —
 * a total with nothing scoping it, under a heading saying "this project", is
 * the panel answering a question it was not asked. A project the payload's
 * range never saw is also null rather than 0: the row is absent because the
 * window does not reach whatever was spent in it, which an em dash says and a
 * zero does not.
 */
export function projectTotal(data: TokenUsage, projectId: string | null): number | null {
  if (!projectId) return null
  const row = data.projects.find((p) => p.id === projectId)
  return row ? totalOf(row) : null
}

/** One agent's share of the range. */
export interface ToolShare {
  tool: string
  total: number
  /** 0-1 of everything the tools spent between them. */
  share: number
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
 */
export function toolShares(data: TokenUsage): ToolShare[] {
  const rows = data.byTool
    .map((t) => ({ tool: t.tool, total: totalOf(t) }))
    .filter((t) => t.total > 0)
    .sort((a, b) => b.total - a.total)
  const sum = rows.reduce((n, r) => n + r.total, 0)
  if (sum <= 0) return []
  return rows.map((r) => ({ ...r, share: r.total / sum }))
}

/**
 * How much the agents *produced* over the range.
 *
 * This is the panel's reading of 「字数」, and it is a reading rather than the
 * thing itself, so it is worth being exact about. The panel cannot count
 * characters or lines: what reaches it is a token ledger read out of the
 * agents' own transcripts, and a token is not a character in any language and
 * is nothing like one in Chinese. Turning output tokens into a character count
 * would need a per-model tokeniser the panel does not have and a ratio it
 * would be inventing.
 *
 * Output tokens are the closest true answer to "how much did it write", and
 * they are the one column of the four that is unambiguously production —
 * input, cache read and cache write are all the cost of *asking*. So the
 * figure is output, and it is labelled output.
 */
export function outputTotal(days: UsageDay[], today: string, span: number): number {
  const from = dayBefore(today, span - 1)
  if (from === '') return 0
  let sum = 0
  for (const d of days) {
    if (d.day >= from && d.day <= today) sum += d.output
  }
  return sum
}
