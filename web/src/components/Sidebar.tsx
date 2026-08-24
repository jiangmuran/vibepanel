import { useMemo } from 'react'
import { ChevronLeft, Pin, PinOff, Plus, Terminal as TerminalIcon, X } from 'lucide-react'

import type { Project, Session, SessionState } from '../protocol/wire'
import { StateDot } from './StateDot'
import { InlineName } from './InlineName'

export interface SidebarProps {
  projects: Project[]
  sessions: Session[]
  live: string[]
  selected: string | null
  expanded: boolean
  /** Overlay mode: the sidebar floats above the content instead of taking a column. */
  overlay: boolean
  onToggle: () => void
  onSelect: (id: string) => void
  onAddProject: () => void
  onNewSession: (project: Project) => void
  onRenameProject: (project: Project, name: string) => void
  onRenameSession: (session: Session, title: string) => void
  onPinSession: (session: Session, pinned: boolean) => void
  onKillSession: (session: Session) => void
}

function sessionLabel(s: Session): string {
  return s.title || s.command || 'session'
}

/** The most urgent state among a project's sessions, for the collapsed rail. */
function summarise(sessions: Session[]): SessionState | null {
  if (sessions.some((s) => s.state === 'waiting')) return 'waiting'
  if (sessions.some((s) => s.state === 'working')) return 'working'
  return sessions.length > 0 ? 'done' : null
}

/** Up to two letters, for the collapsed rail's project badge. */
function initials(name: string): string {
  const words = name.split(/[\s_\-./]+/).filter(Boolean)
  if (words.length === 0) return '?'
  if (words.length === 1) return words[0].slice(0, 2).toUpperCase()
  return (words[0][0] + words[1][0]).toUpperCase()
}

export function Sidebar(props: SidebarProps) {
  const { projects, sessions, expanded, overlay } = props

  const byProject = useMemo(() => {
    const map = new Map<string, Session[]>()
    for (const s of sessions) {
      const list = map.get(s.projectId)
      if (list) list.push(s)
      else map.set(s.projectId, [s])
    }
    return map
  }, [sessions])

  // Collapsed, the sidebar is a rail of project badges carrying a single
  // status each. It exists so a wide terminal is not paying 260px for a list
  // the user only consults when switching tasks.
  if (!expanded && !overlay) {
    return (
      <aside data-testid="sidebar-rail" className="flex w-12 shrink-0 flex-col items-center gap-1 border-r border-hairline py-2 vp-blur">
        <button
          type="button"
          onClick={props.onToggle}
          title="Show projects"
          className="mb-1 rounded-md p-1.5 text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
        >
          <ChevronLeft size={15} className="rotate-180" />
        </button>
        {projects.map((p) => {
          const list = byProject.get(p.id) ?? []
          const state = summarise(list)
          const active = list.some((s) => s.id === props.selected)
          return (
            <button
              key={p.id}
              type="button"
              onClick={props.onToggle}
              title={`${p.name} — ${list.length} session(s)`}
              className={`relative flex h-9 w-9 items-center justify-center rounded-vp text-[11px] font-semibold transition-colors duration-200 ease-vp ${
                active ? 'bg-surface-2 text-ink' : 'text-ink-2 hover:bg-surface-2'
              }`}
            >
              {initials(p.name)}
              {state && (
                <span className="absolute -right-0.5 -bottom-0.5">
                  <StateDot state={state} size={8} />
                </span>
              )}
            </button>
          )
        })}
        <button
          type="button"
          onClick={props.onAddProject}
          title="Add project"
          className="mt-1 rounded-md p-1.5 text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
        >
          <Plus size={15} />
        </button>
      </aside>
    )
  }

  // A docked sidebar sits on the flat page background, so it can be frosted.
  // The overlay covers the terminal and must be opaque.
  const shell = overlay
    ? 'absolute inset-y-0 left-0 z-20 w-72 border-r border-hairline shadow-2xl vp-solid'
    : 'w-64 shrink-0 border-r border-hairline vp-blur'

  return (
    <aside data-testid="sidebar" data-overlay={overlay} className={`flex flex-col ${shell}`}>
      <header className="flex items-center gap-1 px-3 py-2">
        <button
          type="button"
          onClick={props.onToggle}
          title={overlay ? 'Close' : 'Collapse'}
          className="rounded-md p-1.5 text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
        >
          <ChevronLeft size={15} />
        </button>
        <span className="text-[13px] font-semibold tracking-tight">Projects</span>
        <button
          type="button"
          onClick={props.onAddProject}
          title="Add project"
          className="ml-auto rounded-md p-1.5 text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
        >
          <Plus size={15} />
        </button>
      </header>

      <nav className="flex-1 overflow-y-auto px-2 pb-3">
        {projects.length === 0 && (
          <p className="px-2 py-6 text-[12px] leading-relaxed text-ink-2">
            No projects yet. Add one to point the panel at a directory.
          </p>
        )}
        {projects.map((p) => (
          <section key={p.id} className="mb-3">
            <div className="group flex items-center gap-1 px-2 py-1">
              <InlineName
                value={p.name}
                onCommit={(next) => props.onRenameProject(p, next)}
                className="text-[11px] font-semibold tracking-wide text-ink-2 uppercase"
                title={p.path}
              />
              <button
                type="button"
                onClick={() => props.onNewSession(p)}
                title="New shell in this project"
                className="ml-auto rounded p-1 text-ink-2 opacity-0 transition-opacity duration-200 ease-vp group-hover:opacity-100 hover:text-ink focus-visible:opacity-100"
              >
                <TerminalIcon size={13} />
              </button>
            </div>

            {(byProject.get(p.id) ?? []).map((s) => {
              const isLive = props.live.includes(s.id)
              const isSelected = props.selected === s.id
              return (
                <div
                  key={s.id}
                  data-testid="session-row"
                  className={`group flex cursor-pointer items-center gap-2 rounded-vp px-2 py-1.5 transition-colors duration-200 ease-vp ${
                    isSelected ? 'bg-surface-2' : 'hover:bg-surface-2'
                  }`}
                  onClick={() => props.onSelect(s.id)}
                >
                  <StateDot state={s.state} />
                  <InlineName
                    value={sessionLabel(s)}
                    onCommit={(next) => props.onRenameSession(s, next)}
                    className="flex-1 text-[12.5px]"
                  />
                  {s.pinned && <Pin size={11} className="shrink-0 text-ink-2" />}
                  {!isLive && <span className="shrink-0 text-[10px] text-ink-2">idle</span>}
                  <button
                    type="button"
                    onClick={(e) => {
                      e.stopPropagation()
                      props.onPinSession(s, !s.pinned)
                    }}
                    title={s.pinned ? 'Unpin' : 'Pin to the top of this project'}
                    className="shrink-0 rounded p-0.5 text-ink-2 opacity-0 transition-opacity duration-200 ease-vp group-hover:opacity-100 hover:text-ink focus-visible:opacity-100"
                  >
                    {s.pinned ? <PinOff size={12} /> : <Pin size={12} />}
                  </button>
                  <button
                    type="button"
                    onClick={(e) => {
                      e.stopPropagation()
                      props.onKillSession(s)
                    }}
                    title="Kill session"
                    className="shrink-0 rounded p-0.5 text-ink-2 opacity-0 transition-opacity duration-200 ease-vp group-hover:opacity-100 hover:text-ink focus-visible:opacity-100"
                  >
                    <X size={12} />
                  </button>
                </div>
              )
            })}
          </section>
        ))}
      </nav>
    </aside>
  )
}
