import { describe, expect, it } from 'vitest'

import type { TokenUsage, UsageDay } from '../../protocol/wire'
import { dayBefore, dayTotal, outputTotal, projectTotal, toolShares, windowTotal, daySeries } from './spend'

/** A day row, with the four token columns spelled out. */
function day(d: string, over: Partial<UsageDay> = {}): UsageDay {
  return { day: d, input: 10, output: 1, cacheRead: 100, cacheWrite: 5, requests: 2, ...over }
}

/** Everything one of the rows above costs: 10 + 1 + 100 + 5. */
const PER_DAY = 116

const usage = (over: Partial<TokenUsage> = {}): TokenUsage => ({
  scannedAt: 1_700_000_000,
  scanning: false,
  passMs: 12,
  passError: '',
  sources: [],
  today: '2026-03-10',
  from: '2026-02-09',
  to: '2026-03-10',
  days: 30,
  total: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, requests: 0 },
  byDay: [],
  heatmap: [],
  byMonth: [],
  byTool: [],
  byModel: [],
  projects: [],
  sessions: [],
  sessionCount: 0,
  sessionLimit: 100,
  ...over,
})

describe('the day before', () => {
  it('walks back through a month boundary', () => {
    expect(dayBefore('2026-03-01', 1)).toBe('2026-02-28')
    expect(dayBefore('2026-03-10', 6)).toBe('2026-03-04')
  })

  it('walks back through a leap day and a year', () => {
    expect(dayBefore('2024-03-01', 1)).toBe('2024-02-29')
    expect(dayBefore('2026-01-01', 1)).toBe('2025-12-31')
  })

  it('answers nothing for a date it cannot read', () => {
    // Every caller reads the empty string as "no window". A window starting at
    // the epoch would quietly total the whole payload and call it this week.
    for (const bad of ['', 'today', '2026-03', 'xxxx-yy-zz']) {
      expect(dayBefore(bad, 7), bad).toBe('')
    }
  })
})

describe('a window of days', () => {
  const days = [
    day('2026-03-04'),
    day('2026-03-05'),
    day('2026-03-09'),
    day('2026-03-10'),
  ]

  it('is closed at the near end', () => {
    // Seven days ending on the 10th reaches back to the 4th, inclusive.
    expect(windowTotal(days, '2026-03-10', 7)).toBe(PER_DAY * 4)
  })

  it('leaves out a day older than the window', () => {
    // Three days reaches back to the 8th, so the 4th and the 5th are out. A
    // payload's range is longer than this window by design, and counting all
    // of it is how "this week" becomes "this month".
    expect(windowTotal(days, '2026-03-10', 3)).toBe(PER_DAY * 2)
  })

  it('leaves out a day the server has not reached', () => {
    // A browser clock a day ahead of the server's, or a row from a machine in
    // another timezone. Tomorrow has not happened.
    const withFuture = [...days, day('2026-03-11')]
    expect(windowTotal(withFuture, '2026-03-10', 7)).toBe(PER_DAY * 4)
  })

  it('is zero rather than everything when the date is unreadable', () => {
    expect(windowTotal(days, 'not-a-date', 7)).toBe(0)
  })

  it('answers for one day too', () => {
    expect(dayTotal(days, '2026-03-10')).toBe(PER_DAY)
    expect(dayTotal(days, '2026-03-06')).toBe(0)
  })
})

describe('this project', () => {
  const data = usage({
    projects: [
      { id: 'p1', name: 'One', path: '', input: 1, output: 2, cacheRead: 3, cacheWrite: 4, requests: 5 },
    ],
  })

  it('is the row for the project that is selected', () => {
    expect(projectTotal(data, 'p1')).toBe(10)
  })

  it('is nothing at all when no project is selected', () => {
    // A total with nothing scoping it, under a heading saying "this project",
    // is the panel answering a question it was not asked.
    expect(projectTotal(data, null)).toBeNull()
    expect(projectTotal(data, '')).toBeNull()
  })

  it('is nothing rather than zero for a project the range never saw', () => {
    // The row is absent because the window does not reach whatever was spent
    // in it. An em dash says that; a zero says the project cost nothing.
    expect(projectTotal(data, 'p2')).toBeNull()
  })
})

describe('who spent it', () => {
  const data = usage({
    byTool: [
      { tool: 'codex', input: 10, output: 0, cacheRead: 0, cacheWrite: 0, requests: 1, files: 1, skipped: 0, problems: 0, problem: '' },
      { tool: 'claude', input: 30, output: 0, cacheRead: 0, cacheWrite: 0, requests: 1, files: 1, skipped: 0, problems: 0, problem: '' },
      { tool: 'opencode', input: 0, output: 0, cacheRead: 0, cacheWrite: 0, requests: 0, files: 0, skipped: 0, problems: 0, problem: '' },
    ],
  })

  it('is largest first, so the bar and the legend read in one order', () => {
    expect(toolShares(data).map((t) => t.tool)).toEqual(['claude', 'codex'])
  })

  it('drops a tool that spent nothing', () => {
    // A legend entry reading 0% says nothing, and a segment too small to see is
    // a segment nobody can aim at.
    expect(toolShares(data).map((t) => t.tool)).not.toContain('opencode')
  })

  it('divides the bar between the tools and nothing else', () => {
    const shares = toolShares(data)
    expect(shares.reduce((n, t) => n + t.share, 0)).toBeCloseTo(1, 9)
    expect(shares[0].share).toBeCloseTo(0.75, 9)
  })

  it('draws no bar at all when nothing was spent', () => {
    // Rather than four equal segments of nothing, which reads as a machine
    // where every agent is equally busy.
    expect(toolShares(usage({ byTool: [] }))).toEqual([])
    expect(toolShares(usage({ byTool: [data.byTool[2]] }))).toEqual([])
  })
})

describe('what was produced', () => {
  const days = [day('2026-03-09'), day('2026-03-10')]

  it('is the output column and not the total', () => {
    // The panel's reading of 「字数」. Input, cache read and cache write are
    // all the cost of *asking*; output is the only one of the four that is
    // unambiguously production, and it is labelled output rather than words
    // because a token is not a character in any language.
    expect(outputTotal(days, '2026-03-10', 30)).toBe(2)
    expect(outputTotal(days, '2026-03-10', 30)).not.toBe(windowTotal(days, '2026-03-10', 30))
  })

  it('is measured over the same window as everything else', () => {
    expect(outputTotal(days, '2026-03-10', 1)).toBe(1)
  })

  it('is zero rather than everything when the date is unreadable', () => {
    expect(outputTotal(days, '', 30)).toBe(0)
  })
})

describe('the day series behind the sparkline', () => {
  const days = [
    { day: '2026-08-20', input: 1, output: 0, cacheRead: 0, cacheWrite: 0, requests: 1 },
    // 21st absent: a day with nothing on it is not in byDay at all.
    { day: '2026-08-22', input: 3, output: 0, cacheRead: 0, cacheWrite: 0, requests: 1 },
  ]

  it('is oldest first, so a line reads left to right', () => {
    expect(daySeries(days, '2026-08-22', 3)).toEqual([1, 0, 3])
  })

  it('fills a quiet day with zero rather than closing the gap', () => {
    // Joining the 20th to the 22nd draws two days as one step and hides the
    // day off, which is the shape somebody opens a chart to see.
    const s = daySeries(days, '2026-08-22', 3)
    expect(s).toHaveLength(3)
    expect(s[1]).toBe(0)
  })

  it('is all zeros where nothing has been recorded', () => {
    expect(daySeries([], '2026-08-22', 4)).toEqual([0, 0, 0, 0])
  })
})
