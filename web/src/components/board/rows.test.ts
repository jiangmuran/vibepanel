import { describe, expect, it } from 'vitest'

import type { ShareSession } from '../../protocol/wire'
import { EXIT_VANISHED } from '../../protocol/wire'
import { filterRows, orderRows } from './rows'
import { compact, ratio } from './format'

/**
 * The two pure decisions a session widget makes.
 *
 * They are pure on purpose: the widget renders whatever comes back, so if these
 * are right the only thing left in the component is layout. Both are settings
 * somebody chooses in the editor, which means both are read from a stored row
 * and neither can be trusted to be a value this build knows.
 */

function row(over: Partial<ShareSession>): ShareSession {
  return {
    id: 'x',
    projectId: 'p',
    name: '',
    state: 'done',
    kind: 'agent',
    stateChangedAt: 0,
    exited: false,
    exitStatus: 0,
    measured: false,
    cpuPercent: 0,
    rss: 0,
    procs: 0,
    ...over,
  }
}

describe('which rows a filter keeps', () => {
  const rows = [
    row({ id: 'waiting', state: 'waiting' }),
    row({ id: 'working', state: 'working' }),
    row({ id: 'finished', state: 'done' }),
    row({ id: 'clean-exit', state: 'done', exited: true, exitStatus: 0 }),
    row({ id: 'crashed', state: 'done', exited: true, exitStatus: 2 }),
    row({ id: 'vanished', state: 'done', exited: true, exitStatus: EXIT_VANISHED }),
  ]

  it('keeps everything by default, including a filter it has never heard of', () => {
    // The filter comes out of a database row. An unknown one has to mean "show
    // the rows", because the alternative is a wall that has quietly emptied
    // itself and nobody standing there to notice.
    expect(filterRows(rows, 'all')).toHaveLength(6)
    expect(filterRows(rows, 'from-a-newer-build')).toHaveLength(6)
  })

  it('waiting is what is waiting, and not what has stopped', () => {
    expect(filterRows(rows, 'waiting').map((r) => r.id)).toEqual(['waiting'])
  })

  it('active is everything still running', () => {
    expect(filterRows(rows, 'active').map((r) => r.id)).toEqual([
      'waiting',
      'working',
      'finished',
    ])
  })

  it('trouble is what ended badly, and a session waiting for you is not trouble', () => {
    // A session waiting for an answer is working exactly as designed. A board
    // that called that trouble would be red all day, which is the same as not
    // being red at all.
    expect(filterRows(rows, 'trouble').map((r) => r.id)).toEqual(['crashed', 'vanished'])
  })
})

describe('how an order sorts them', () => {
  it('by state puts what needs you first', () => {
    const rows = [row({ id: 'd', state: 'done' }), row({ id: 'w', state: 'waiting' })]
    expect(orderRows(rows, 'state').map((r) => r.id)).toEqual(['w', 'd'])
  })

  it('by wait puts the longest-ignored first, and a row with no clock last', () => {
    // A missing timestamp sorted as zero would put "never observed" at the top
    // of a queue meant to say what has been ignored longest.
    const rows = [
      row({ id: 'unknown', stateChangedAt: 0 }),
      row({ id: 'recent', stateChangedAt: 200 }),
      row({ id: 'old', stateChangedAt: 100 }),
    ]
    expect(orderRows(rows, 'waited').map((r) => r.id)).toEqual(['old', 'recent', 'unknown'])
  })

  it('by cpu puts the heaviest first', () => {
    const rows = [row({ id: 'idle', cpuPercent: 1 }), row({ id: 'busy', cpuPercent: 90 })]
    expect(orderRows(rows, 'cpu').map((r) => r.id)).toEqual(['busy', 'idle'])
  })

  it('does not mutate what it was given', () => {
    const rows = [row({ id: 'd', state: 'done' }), row({ id: 'w', state: 'waiting' })]
    orderRows(rows, 'state')
    expect(rows.map((r) => r.id)).toEqual(['d', 'w'])
  })
})

describe('numbers at wall size', () => {
  it('reads as a magnitude rather than as eight digits', () => {
    expect(compact(412)).toBe('412')
    expect(compact(41_283_904)).toBe('41M')
    expect(compact(9_400_000)).toBe('9.4M')
    expect(compact(1_200_000_000)).toBe('1.2B')
  })

  it('answers null rather than zero when there is nothing to divide by', () => {
    // Null is not zero, and this is the third place in the codebase that has
    // had to say so. A meter with no denominator is not a machine at rest.
    expect(ratio(1, 0)).toBeNull()
    expect(ratio(1, Number.NaN)).toBeNull()
    expect(ratio(1, 2)).toBe(50)
    expect(ratio(9, 2)).toBe(100)
  })
})
