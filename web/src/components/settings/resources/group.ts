import type { ResourceSession } from '../../../protocol/wire'

/** One project's sessions on the resources page. */
export interface ProjectGroup {
  /** The project id; the name when the server could not say which project. */
  key: string
  id: string
  name: string
  archived: boolean
  rows: ResourceSession[]
  memory: number
  held: number
}

/**
 * Sessions by project, the heaviest project first and each project's sessions
 * the same way.
 *
 * The page is for finding what holds memory, so the order is memory and
 * nothing else: not the sidebar's order, which is about what somebody was
 * doing. A project the server has no row for -- its sessions outlived the
 * project, or the poller has not read it yet -- is grouped by the name the
 * server sent with each session, and by nothing when it sent none.
 */
export function groupSessions(rows: ResourceSession[]): ProjectGroup[] {
  const by = new Map<string, ProjectGroup>()
  for (const r of rows) {
    const key = r.projectId || r.projectName || ''
    let g = by.get(key)
    if (!g) {
      g = { key, id: r.projectId, name: r.projectName, archived: r.archived, rows: [], memory: 0, held: 0 }
      by.set(key, g)
    }
    g.rows.push(r)
    g.memory += r.memory
    g.held += r.held
    // Per session on the wire, per project on the page: one archived session
    // means the project is, since that is where the server read it from.
    g.archived ||= r.archived
  }
  const out = [...by.values()]
  for (const g of out) g.rows.sort((a, b) => b.memory - a.memory || a.id.localeCompare(b.id))
  out.sort((a, b) => b.memory - a.memory || a.key.localeCompare(b.key))
  return out
}
