import { describe, expect, it } from 'vitest'

import { compact, levelOf, levels, monthLabels, totalOf, weeks } from './tokens'
import type { UsageDay } from '../../protocol/wire'

function day(d: string, total: number): UsageDay {
  return { day: d, input: total, output: 0, cacheRead: 0, cacheWrite: 0, requests: 1 }
}

describe('token counts', () => {
  it('folds large numbers without losing their order of magnitude', () => {
    expect(compact(0)).toBe('0')
    expect(compact(999)).toBe('999')
    expect(compact(1200)).toBe('1.2k')
    expect(compact(812004112)).toBe('812M')
    expect(compact(1_500_000_000)).toBe('1.5B')
  })

  it('adds every column, cache included', () => {
    expect(
      totalOf({ input: 1, output: 2, cacheRead: 4, cacheWrite: 8, requests: 99 }),
    ).toBe(15)
  })
})

describe('the heatmap shading', () => {
  // Fixed thresholds cannot span a light week and an overnight agent run; and
  // including the empty days in the quantiles puts every cut at zero on a year
  // that is mostly empty, painting every working day the darkest shade.
  it('ignores empty days when choosing the cuts', () => {
    const days = [
      ...Array.from({ length: 300 }, (_, i) => day(`2026-01-${i}`, 0)),
      day('a', 10),
      day('b', 20),
      day('c', 30),
      day('d', 40),
    ]
    const cuts = levels(days)
    expect(cuts.every((c) => c > 0)).toBe(true)
    expect(levelOf(10, cuts)).toBeLessThan(levelOf(40, cuts))
  })

  it('keeps a day with nothing on it at level zero', () => {
    expect(levelOf(0, [1, 2, 3, 4])).toBe(0)
  })

  it('gives every level to a flat range rather than collapsing it', () => {
    const cuts = levels(Array.from({ length: 40 }, (_, i) => day(`d${i}`, i + 1)))
    expect(levelOf(1, cuts)).toBe(1)
    expect(levelOf(40, cuts)).toBe(4)
  })
})

describe('the year grid', () => {
  // The grid is drawn from the server's local `today`. Parsing it with
  // `new Date('2026-08-27')` is a UTC parse, which lands on the 26th for
  // anyone west of Greenwich and shifts every square by one.
  it('puts the server’s today in the last column', () => {
    const grid = weeks([day('2026-08-27', 100)], '2026-08-27', 371)
    const last = grid[grid.length - 1]
    const found = last.cells.find((c) => c && c.day === '2026-08-27')
    expect(found, 'today is not in the final column').toBeTruthy()
    expect(found?.total).toBe(100)
  })

  it('draws whole weeks, Sunday first', () => {
    const grid = weeks([], '2026-08-27', 371)
    expect(grid.every((w) => w.cells.length === 7)).toBe(true)
    const first = grid[0].cells[0]
    expect(first).toBeTruthy()
    // 0 = Sunday, in the local calendar.
    const [y, m, d] = (first as { day: string }).day.split('-').map(Number)
    expect(new Date(y, m - 1, d).getDay()).toBe(0)
  })

  // Tomorrow has not happened. Drawing it as an empty day is a claim about the
  // future, and it is the shade that means "nothing was spent".
  it('leaves the days after today blank rather than empty', () => {
    // 2026-08-27 is a Thursday, so Friday and Saturday of that column are
    // beyond it.
    const grid = weeks([], '2026-08-27', 371)
    const last = grid[grid.length - 1]
    expect(last.cells[last.cells.length - 1]).toBeNull()
  })

  // A day inside the range with no traffic is a real reading of zero; a day
  // the server did not send at all is not known. They render differently.
  it('distinguishes a day with no traffic from a day outside the range', () => {
    const grid = weeks([day('2026-08-27', 0)], '2026-08-27', 371)
    const cells = grid.flatMap((w) => w.cells).filter((c) => c !== null)
    const today = cells.find((c) => c.day === '2026-08-27')
    const earlier = cells.find((c) => c.day === '2026-08-20')
    expect(today?.total).toBe(0)
    expect(earlier?.total).toBeNull()
  })

  it('does not stack two month labels on top of each other', () => {
    const grid = weeks([], '2026-08-27', 371)
    const labels = monthLabels(grid)
    expect(labels.length).toBeGreaterThan(6)
    for (let i = 1; i < labels.length; i++) {
      expect(labels[i].index - labels[i - 1].index).toBeGreaterThanOrEqual(3)
    }
  })
})
