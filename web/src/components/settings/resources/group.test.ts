import { describe, expect, it } from 'vitest'

import type { ResourceSession } from '../../../protocol/wire'
import { groupSessions } from './group'

function row(id: string, projectId: string, projectName: string, memory: number, archived = false): ResourceSession {
  return {
    id,
    title: id,
    projectId,
    projectName,
    archived,
    createdAt: 0,
    lastUsedAt: 0,
    memory,
    held: memory / 2,
    cpuPercent: 0,
    procs: 1,
    frozen: false,
    priority: 'normal',
  }
}

describe('sessions by project', () => {
  it('puts the heaviest project first and its heaviest session first', () => {
    const groups = groupSessions([
      row('a1', 'a', 'api', 100),
      row('w1', 'w', 'web', 300),
      row('a2', 'a', 'api', 250),
      row('w2', 'w', 'web', 10),
    ])
    expect(groups.map((g) => g.name)).toEqual(['api', 'web'])
    expect(groups[0].rows.map((r) => r.id)).toEqual(['a2', 'a1'])
    expect(groups[0].memory).toBe(350)
    expect(groups[0].held).toBe(175)
    expect(groups[1].rows.map((r) => r.id)).toEqual(['w1', 'w2'])
  })

  it('says a project is archived when its sessions are', () => {
    const [g] = groupSessions([row('o1', 'o', 'old', 5, true), row('o2', 'o', 'old', 6, true)])
    expect(g.archived).toBe(true)
    expect(groupSessions([row('x', 'p', 'live', 1)])[0].archived).toBe(false)
    // Whichever session is read first.
    expect(groupSessions([row('a', 'q', 'old', 9), row('b', 'q', 'old', 1, true)])[0].archived).toBe(true)
  })

  it('keeps sessions of a project the server could not identify under the name it sent', () => {
    const groups = groupSessions([row('a', '', 'gone', 5), row('b', '', 'gone', 6), row('c', '', '', 1)])
    expect(groups.map((g) => [g.key, g.rows.length])).toEqual([
      ['gone', 2],
      ['', 1],
    ])
  })

  it('is stable between equal sizes, so a poll does not shuffle the list', () => {
    const rows = [row('b', 'p2', 'two', 1), row('a', 'p1', 'one', 1)]
    expect(groupSessions(rows).map((g) => g.id)).toEqual(['p1', 'p2'])
    expect(groupSessions([...rows].reverse()).map((g) => g.id)).toEqual(['p1', 'p2'])
  })

  it('is empty for no sessions', () => {
    expect(groupSessions([])).toEqual([])
  })
})
