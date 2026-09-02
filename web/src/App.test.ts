import { describe, expect, it } from 'vitest'

import type { Session } from './protocol/wire'
import { nextSelection, stripFor } from './App'

/**
 * What the panel falls back to when the selection is gone.
 *
 * The panel draws `mainSessions.find(s => s.id === selected)`, and
 * `mainSessions` is the list with the scratch terminals taken out. So a
 * fallback that picks out of the whole list can hand it an id it will never
 * find, and the answer to that is not "no session" on screen — it is a panel
 * with a session list beside an empty frame, remembered in localStorage.
 */
const session = (id: string, scratch = false): Session => ({ id, scratch }) as Session

const term = (id: string, projectId: string, createdAt = 0): Session =>
  ({ id, projectId, createdAt, scratch: true }) as Session

describe('choosing a session after a snapshot', () => {
  // The order the server actually sends: no scratch predicate anywhere in the
  // ORDER BY, so a scratch pane that rang the bell (`waiting`) sorts ahead of
  // the agents. See internal/store/sessions.go.
  const childFirst = [session('s_child', true), session('s_parent')]

  it('never lands on a scratch terminal', () => {
    expect(nextSelection(childFirst, null)).toBe('s_parent')
  })

  it('drops a scratch terminal that arrived from localStorage', () => {
    expect(nextSelection(childFirst, 's_child')).toBe('s_parent')
  })

  it('keeps a session that is still there', () => {
    expect(nextSelection(childFirst, 's_parent')).toBe('s_parent')
  })

  it('replaces one that has gone', () => {
    expect(nextSelection(childFirst, 's_gone')).toBe('s_parent')
  })

  it('has nothing to offer when every session is a scratch terminal', () => {
    expect(nextSelection([session('s_child', true)], null)).toBeNull()
    expect(nextSelection([], null)).toBeNull()
  })
})

describe('the terminal strip follows the project', () => {
  // 「下方term跟随项目走吧 别跟随session走」. The strip used to be filtered by
  // the selected session's id, so moving between two agents in one project
  // replaced the whole set -- and what is in there is a build and a `git log`
  // for the repository, not for the agent.
  const sessions = [
    term('t_b', 'p_one', 20),
    term('t_a', 'p_one', 10),
    term('t_other', 'p_two', 5),
    { id: 's_agent', projectId: 'p_one', scratch: false } as Session,
  ]

  it('keeps the same terminals for every session in one project', () => {
    expect(stripFor(sessions, 'p_one').map((s) => s.id)).toEqual(['t_a', 't_b'])
  })

  it('changes when the project does', () => {
    expect(stripFor(sessions, 'p_two').map((s) => s.id)).toEqual(['t_other'])
  })

  it('leaves the agents out', () => {
    expect(stripFor(sessions, 'p_one').every((s) => s.scratch)).toBe(true)
  })

  it('has nothing without a project', () => {
    expect(stripFor(sessions, null)).toEqual([])
  })

  // Creation order, not arrival order: a tab strip that reorders itself while
  // you are using it is hostile, and the server sorts sessions by state.
  it('orders by creation regardless of the order it is handed', () => {
    const shuffled = [sessions[0], sessions[3], sessions[1], sessions[2]]
    expect(stripFor(shuffled, 'p_one').map((s) => s.id)).toEqual(['t_a', 't_b'])
  })
})
