import { describe, expect, it } from 'vitest'

import type { Session } from './protocol/wire'
import { nextSelection } from './App'

/**
 * What the panel falls back to when the selection is gone.
 *
 * The panel draws `mainSessions.find(s => s.id === selected)`, and
 * `mainSessions` is the list with the scratch terminals taken out. So a
 * fallback that picks out of the whole list can hand it an id it will never
 * find, and the answer to that is not "no session" on screen — it is a panel
 * with a session list beside an empty frame, remembered in localStorage.
 */
const session = (id: string, parent: string | null = null): Session =>
  ({ id, parentSessionId: parent }) as Session

describe('choosing a session after a snapshot', () => {
  // The order the server actually sends: no parent predicate anywhere in the
  // ORDER BY, so a scratch pane that rang the bell (`waiting`) sorts ahead of
  // its own parent. See internal/store/sessions.go.
  const childFirst = [session('s_child', 's_parent'), session('s_parent')]

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
    expect(nextSelection([session('s_child', 's_parent')], null)).toBeNull()
    expect(nextSelection([], null)).toBeNull()
  })
})
