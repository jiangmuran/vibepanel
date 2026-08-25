import { useMemo } from 'react'
import {
  ChevronLeft,
  Clock,
  GripVertical,
  ListOrdered,
  Pin,
  PinOff,
  Plus,
  RotateCcw,
  Terminal as TerminalIcon,
  X,
} from 'lucide-react'

import type { Project, Session, SessionState } from '../protocol/wire'
import { useDragList } from '../hooks/useDragList'
import { sessionLabel } from './label'
import { StateDot } from './StateDot'
import { InlineName } from './InlineName'
import { EXIT_VANISHED } from '../protocol/wire'

export interface SidebarProps {
  projects: Project[]
  sessions: Session[]
  /**
   * What to call each session, by id.
   *
   * Computed once by the caller rather than here, so the sidebar and the title
   * bar cannot disagree about the name of the session you are looking at —
   * which reads as a rendering glitch rather than as two functions.
   */
  labels: Map<string, string>
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
  onSetSessionState: (session: Session, state: SessionState) => void
  onKillSession: (session: Session) => void
  onRestartSession: (session: Session) => void

  projectOrder: 'auto' | 'manual'
  onReorderProjects: (ids: string[]) => void
  onAutoOrderProjects: () => void
  hasProjectOrder: boolean
  onRestoreProjectOrder: () => void

  /** An agent is running and nothing is reporting its state. */
  stateGuessed: boolean
  onOpenSettings: () => void
}


/** The most urgent state among a project's sessions, for the collapsed rail. */
/**
 * The one glyph the collapsed rail can show for a whole project.
 *
 * A crash outranks "done" but not the two live states: something still running
 * or still asking is more urgent than something that already failed and will
 * stay failed. Returning it as a crash rather than a state is what stops a
 * project whose every session died from wearing a green check.
 */
function summarise(sessions: Session[]): SessionState | 'crashed' | null {
  if (sessions.some((s) => s.state === 'waiting')) return 'waiting'
  if (sessions.some((s) => s.state === 'working')) return 'working'
  // A session that vanished is not a session that crashed. Counting it as one
  // put a crash marker on the project badge for a tmux session somebody had
  // closed from a shell on purpose.
  if (sessions.some((s) => s.exited && s.exitStatus !== 0 && s.exitStatus !== EXIT_VANISHED)) {
    return 'crashed'
  }
  return sessions.length > 0 ? 'done' : null
}

/**
 * Up to two letters, for the collapsed rail's project badge.
 *
 * Counted in code points, not code units. `str[0]` and `slice` work on UTF-16
 * units, and an emoji is a surrogate pair — so taking the first unit of
 * "📊 monitoring" yields half a character and the badge renders a replacement
 * glyph. Not a hypothetical input: naming things with an emoji in front is
 * ordinary, and the setup this panel was built to replace did exactly that.
 *
 * CJK is safe either way, being one unit per character, but this costs nothing
 * and removes the distinction.
 */
function initials(name: string): string {
  const chars = (s: string) => [...s]
  const words = name.split(/[\s_\-./]+/).filter(Boolean)
  if (words.length === 0) return '?'
  if (words.length === 1) return chars(words[0]).slice(0, 2).join('').toUpperCase()
  return (chars(words[0])[0] + chars(words[1])[0]).toUpperCase()
}

export function Sidebar(props: SidebarProps) {
  const { projects, sessions, expanded, overlay } = props

  const projectIds = useMemo(() => projects.map((p) => p.id), [projects])
  const drag = useDragList(projectIds, props.onReorderProjects)

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
      // overflow-y-auto: fourteen projects reach the bottom of a 520px window,
      // and the fifteenth would have been drawn past it with no way to scroll —
      // the same defect as the tab strip below and the key bar on a phone.
      <aside
        data-testid="sidebar-rail"
        className="flex w-12 shrink-0 flex-col items-center gap-1 overflow-y-auto border-r border-hairline py-2 vp-blur vp-safe-pad-top"
      >
        <button
          type="button"
          onClick={props.onToggle}
          title="Show projects"
          className="mb-1 shrink-0 rounded-md p-1.5 text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
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
              data-testid="rail-project"
              onClick={props.onToggle}
              title={`${p.name} — ${list.length} session(s)`}
              // shrink-0, or the scroller above never gets a chance.
              //
              // Flex children compress before they overflow, so a rail with
              // twenty projects did not scroll — it squeezed every badge from
              // 36px down to 17, which is neither readable nor tappable, and
              // the overflow rule added to fix "the rail spills" never fired
              // because nothing ever spilled.
              className={`relative flex h-9 w-9 shrink-0 items-center justify-center rounded-vp text-[11px] font-semibold transition-colors duration-200 ease-vp ${
                active ? 'bg-surface-2 text-ink' : 'text-ink-2 hover:bg-surface-2'
              }`}
            >
              {initials(p.name)}
              {state && (
                <span className="absolute -right-0.5 -bottom-0.5">
                  {state === 'crashed' ? (
                    <StateDot state="done" size={8} exited exitStatus={1} />
                  ) : (
                    <StateDot state={state} size={8} />
                  )}
                </span>
              )}
            </button>
          )
        })}
        <button
          type="button"
          onClick={props.onAddProject}
          title="Add project"
          className="mt-1 shrink-0 rounded-md p-1.5 text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
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
      <header className="flex items-center gap-1 px-3 py-2 vp-safe-pad-top">
        <button
          type="button"
          onClick={props.onToggle}
          title={overlay ? 'Close' : 'Collapse'}
          className="rounded-md p-1.5 text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
        >
          <ChevronLeft size={15} />
        </button>
        <span className="text-[13px] font-semibold tracking-tight">Projects</span>
        {/* Two views of the same projects, and switching between them costs
            nothing now. This used to be one button that erased the
            arrangement and then removed itself, so there was no way back and
            nothing left to click. */}
        {props.projectOrder === 'manual' && (
          <button
            type="button"
            data-testid="order-auto"
            onClick={props.onAutoOrderProjects}
            title="Sort by recent activity instead — your arrangement is kept"
            className="ml-auto rounded-md p-1.5 text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
          >
            <Clock size={14} />
          </button>
        )}
        {props.projectOrder === 'auto' && props.hasProjectOrder && (
          <button
            type="button"
            data-testid="order-manual"
            onClick={props.onRestoreProjectOrder}
            title="Back to the order you arranged"
            className="ml-auto rounded-md p-1.5 text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
          >
            <ListOrdered size={14} />
          </button>
        )}
        <button
          type="button"
          onClick={props.onAddProject}
          title="Add project"
          className={`rounded-md p-1.5 text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink ${
            props.projectOrder === 'manual' || props.hasProjectOrder ? '' : 'ml-auto'
          }`}
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
        {projects.map((p, index) => (
          <section
            key={p.id}
            ref={(el) => drag.register(p.id, el)}
            data-testid="project-group"
            className={`mb-3 transition-opacity duration-200 ease-vp ${
              drag.draggingId === p.id ? 'opacity-40' : ''
            }`}
          >
            {/* The gap the dragged project would land in. A ghost that follows
                the pointer looks better but tells you less: what matters is
                where it goes, not where your finger is. */}
            {drag.overIndex === index && drag.draggingId !== null && (
              <div className="mx-2 mb-1 h-0.5 rounded-full bg-accent" />
            )}
            <div className="group flex items-center gap-1 px-2 py-1">
              <span
                {...drag.handleProps(p.id)}
                data-testid="project-grip"
                title="Drag to reorder"
                className="vp-tap -ml-1 cursor-grab rounded p-0.5 text-ink-2 vp-reveal active:cursor-grabbing"
              >
                <GripVertical size={12} />
              </span>
              <InlineName
                value={p.name}
                onCommit={(next) => props.onRenameProject(p, next)}
                className="text-[11px] font-semibold tracking-wide text-ink-2 uppercase"
                title={p.path}
              />
              <button
                type="button"
                onClick={() => props.onNewSession(p)}
                data-testid="project-new-shell"
                title="New shell in this project"
                className="vp-tap ml-auto rounded p-1 text-ink-2 vp-reveal hover:text-ink"
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
                  <StateDot
                    state={s.state}
                    exited={s.exited}
                    exitStatus={s.exitStatus}
                    onToggle={(next) => props.onSetSessionState(s, next)}
                  />
                  <InlineName
                    value={props.labels.get(s.id) ?? sessionLabel(s)}
                    onCommit={(next) => props.onRenameSession(s, next)}
                    className="flex-1 text-[12.5px]"
                  />
                  {s.pinned && <Pin size={11} className="shrink-0 text-ink-2" />}
                  {/* The glyph says "gone" and this says how. A shape cannot
                      carry an exit code, and 3 vs 0 is the difference between
                      "it crashed" and "it finished and closed". */}
                  {s.exited && (
                    <span
                      className={`shrink-0 text-[10px] tabular ${
                        s.exitStatus === 0 || s.exitStatus === EXIT_VANISHED
                          ? 'text-ink-2'
                          : 'text-state-crashed'
                      }`}
                    >
                      {s.exitStatus === EXIT_VANISHED
                        ? 'gone'
                        : s.exitStatus === 0
                          ? 'exited'
                          : `exit ${s.exitStatus}`}
                    </span>
                  )}
                  {!isLive && !s.exited && (
                    <span className="shrink-0 text-[10px] text-ink-2">idle</span>
                  )}
                  {/* Always visible, unlike pin and kill: a dead session is a
                      thing to act on, not an affordance to discover on hover —
                      and hover does not exist on the phone. */}
                  {s.exited && (
                    <button
                      type="button"
                      data-testid="restart-session"
                      onClick={(e) => {
                        e.stopPropagation()
                        props.onRestartSession(s)
                      }}
                      title="Restart this session's command in the same pane"
                      className="vp-tap shrink-0 rounded p-0.5 text-ink-2 transition-colors duration-200 ease-vp hover:text-ink"
                    >
                      <RotateCcw size={12} />
                    </button>
                  )}
                  <button
                    type="button"
                    onClick={(e) => {
                      e.stopPropagation()
                      props.onPinSession(s, !s.pinned)
                    }}
                    data-testid="pin-session"
                    title={s.pinned ? 'Unpin' : 'Pin to the top of this project'}
                    className="vp-tap shrink-0 rounded p-0.5 text-ink-2 vp-reveal hover:text-ink"
                  >
                    {s.pinned ? <PinOff size={12} /> : <Pin size={12} />}
                  </button>
                  <button
                    type="button"
                    onClick={(e) => {
                      e.stopPropagation()
                      props.onKillSession(s)
                    }}
                    data-testid="kill-session"
                    title="Kill session"
                    className="vp-tap shrink-0 rounded p-0.5 text-ink-2 vp-reveal hover:text-ink"
                  >
                    <X size={12} />
                  </button>
                </div>
              )
            })}
          </section>
        ))}
        {drag.overIndex === projects.length && drag.draggingId !== null && (
          <div className="mx-2 h-0.5 rounded-full bg-accent" />
        )}
      </nav>

      {/* Self-clearing: it disappears the moment anything reports state, so it
          is a statement of fact rather than a prompt to be dismissed. */}
      {props.stateGuessed && (
        <button
          type="button"
          data-testid="state-guessed-notice"
          onClick={props.onOpenSettings}
          className="border-t border-hairline px-3 py-2 text-left text-[11px] leading-relaxed text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
        >
          States are being guessed from output. Claude Code does not ring the terminal bell when it
          stops for a decision, so <span className="text-ink">waiting for you</span> will be missed.
          Turn on state reporting →
        </button>
      )}
    </aside>
  )
}
