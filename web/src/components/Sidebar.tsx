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

import type { GitRemote, Project, Session, SessionState } from '../protocol/wire'
import { useDragList } from '../hooks/useDragList'
import { projectLabel, sessionLabel } from './label'
import { StateDot } from './StateDot'
import { InlineName } from './InlineName'
import { ProjectMark } from './ProjectMark'
import { EXIT_VANISHED } from '../protocol/wire'
import { t, useLang } from '../i18n'

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
  /**
   * The pointer is a finger, which decides the *type* and nothing else.
   *
   * Separate from `overlay`, which decides the layout. A tablet gets the docked
   * sidebar -- it has the room -- and a finger, so every row picks up the 44px
   * touch floor from styles.css while the type stayed at the density a mouse
   * reads from two feet away. 「左侧选择tab还是框太大字太小」: the boxes were
   * right and the text in them was not.
   */
  touch: boolean
  onToggle: () => void
  onSelect: (id: string) => void
  onAddProject: () => void
  /**
   * Opens the launch picker for this project rather than creating a session.
   *
   * It used to create one in a single click, which was the right default for
   * whoever wanted a login shell and a click-then-type for everybody else.
   */
  onNewSession: (project: Project) => void
  onRenameProject: (project: Project, name: string) => void
  onRemoveProject: (project: Project) => void
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
  hooksInstalled: boolean
  onOpenSettings: () => void
}

/**
 * This panel, and where its source is.
 *
 * Constants rather than a fetch: the corner they fill is an attribution to the
 * software, not a reading of anything, so there is nothing to poll and nothing
 * that can come back empty. `ProjectMark` renders it exactly as it rendered a
 * project, which is the point -- the slot did not change shape, only what it is
 * about.
 */
const PANEL_NAME = 'vibepanel'
const PANEL_REPO: GitRemote = {
  url: 'https://github.com/jiangmuran/vibepanel',
  host: 'github.com',
  owner: 'jiangmuran',
  name: 'vibepanel',
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
  useLang()
  const { projects, sessions, expanded, overlay, touch } = props

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
          title={t('app.projects')}
          className="vp-control mb-1"
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
              title={`${projectLabel(p)} — ${list.length} session(s)`}
              // shrink-0, or the scroller above never gets a chance.
              //
              // Flex children compress before they overflow, so a rail with
              // twenty projects did not scroll — it squeezed every badge from
              // 36px down to 17, which is neither readable nor tappable, and
              // the overflow rule added to fix "the rail spills" never fired
              // because nothing ever spilled.
              className={`relative flex h-9 w-9 shrink-0 items-center justify-center rounded-vp text-vp-sm font-semibold transition-colors duration-200 ease-vp ${
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
          title={t('app.addProject')}
          data-testid="add-project"
          className="vp-control mt-1"
        >
          <Plus size={15} />
        </button>
      </aside>
    )
  }

  // A docked sidebar sits on the flat page background, so it can be frosted.
  // One step up on every rank, in the drawer only.
  //
  // A coarse pointer grows a row to 44px for a thumb and does nothing to the
  // type inside it, so the phone drawer was a 16px label floating in a 56px
  // pill -- measured, session row 271x56, and the reported 「字号和单 tab 容器
  // 大小严重不对称」. The rule is the relationship rather than four numbers:
  // where a control was made bigger for a finger, what it says grows with it.
  //
  // Chosen here rather than by remapping the type tokens in CSS. Those are
  // literals in `@theme`, which inlines them into every utility it generates,
  // so a descendant redefining `--text-vp-base` changes nothing at all -- the
  // same trap that made the wall scale inert, twice. A ternary is duller and
  // it is a thing that can be read.
  // Bigger type for a finger, whichever layout it is in. The drawer already
  // had it; the docked sidebar on a tablet is the case that was missed.
  const big = overlay || touch
  const rank = {
    head: big ? 'text-vp-lg' : 'text-vp-md',
    // A project's name is the heading over its sessions and was set one step
    // *below* them -- grey, small, tracked out -- so the thing you scan for
    // read as weaker than the rows under it. 「字太小」, with a screenshot of a
    // drawer where the only large text was the sessions.
    group: big ? 'text-vp-md' : 'text-vp-base',
    row: big ? 'text-vp-md' : 'text-vp-base',
  }

  // How much room a project with no sessions takes.
  //
  // Measured before changing it: 63px in the drawer and 44px docked, for one
  // line of text -- so ten projects filled a phone and most of what was on
  // screen was gap. 「间距太宽 ... 很多项目的时候 不明显 而且多了很不友好」.
  //
  // The floor is the touch target, not the type: the grip, the new-terminal
  // button and the remove button are all `.vp-tap`, which is 44px under a
  // coarse pointer, so the header row cannot usefully go below that on a phone
  // and there is no reason for it to be taller. Padding comes off the row and
  // the gap between groups, and nothing comes off the buttons.
  const pack = {
    // Space *between* groups, none inside one. A project and its sessions are
    // one thing; the gap that says so has to be bigger than the gap between
    // two sessions, and it used to be the same.
    section: overlay ? 'mb-1 mt-3 first:mt-0' : 'mb-1 mt-2.5 first:mt-0',
    header: overlay ? 'px-2 py-0' : 'px-2 py-0.5',
    // Indented, and that is the hierarchy. Once the project's name stopped
    // being smaller and greyer than its sessions, the two read as one flat
    // list: same size, same colour, same left edge, and the only difference a
    // state glyph. The indent says which belongs to which and costs no height,
    // which is the whole point of it rather than more space between groups.
    // py-0.5 docked: the row's height is set by the 28px control boxes in it,
    // not by the text, so padding on top of them is pure air. 「太松散了」.
    row: overlay ? 'px-2 py-1' : 'px-2 py-0.5',
    // The connector. Once the project's name stopped being smaller and greyer
    // than its sessions -- correctly, it is what you scan for -- the two read
    // as one flat list of identical rows. A rule down the left of a project's
    // sessions says which belong to which, and costs no height at all, which
    // more space between groups would.
    kids: overlay ? 'ml-4 border-l border-hairline pl-1' : 'ml-3 border-l border-hairline pl-1',
  }

  // On a touch screen the row controls live on the selected row only.
  //
  // They cannot be small there -- every button is 44px, which the render check
  // measures -- so pin and kill on every row set the height of every row, and
  // a phone showed six sessions where it had room for a dozen. Selecting first
  // and acting second is one more tap and it is how every list on a phone
  // works; it also gives the row you are on something to be, which is the
  // other half of 「没有层次感」.
  const rowControls = (isSelected: boolean) => !overlay || isSelected

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
          className="vp-control"
        >
          <ChevronLeft size={15} />
        </button>
        <span className={`${rank.head} font-semibold tracking-tight`}>{t('app.projects')}</span>
        {/* Two views of the same projects, and switching between them costs
            nothing now. This used to be one button that erased the
            arrangement and then removed itself, so there was no way back and
            nothing left to click. */}
        {props.projectOrder === 'manual' && (
          <button
            type="button"
            data-testid="order-auto"
            onClick={props.onAutoOrderProjects}
            title={t('app.sortByActivity')}
            className="vp-control ml-auto"
          >
            <Clock size={14} />
          </button>
        )}
        {props.projectOrder === 'auto' && props.hasProjectOrder && (
          <button
            type="button"
            data-testid="order-manual"
            onClick={props.onRestoreProjectOrder}
            title={t('project.orderManual')}
            className="vp-control ml-auto"
          >
            <ListOrdered size={14} />
          </button>
        )}
        <button
          type="button"
          onClick={props.onAddProject}
          title={t('app.addProject')}
          data-testid="add-project"
          className={`vp-control ${
            props.projectOrder === 'manual' || props.hasProjectOrder ? '' : 'ml-auto'
          }`}
        >
          <Plus size={15} />
        </button>
      </header>

      <nav className="flex-1 overflow-y-auto px-2 pb-3">
        {projects.length === 0 && (
          <p className="px-2 py-6 text-vp-base leading-relaxed text-ink-2">
            {t('app.noProjects')}
          </p>
        )}
        {projects.map((p, index) => (
          <section
            key={p.id}
            ref={(el) => drag.register(p.id, el)}
            data-testid="project-group"
            className={`${pack.section} transition-opacity duration-200 ease-vp ${
              drag.draggingId === p.id ? 'opacity-40' : ''
            }`}
          >
            {/* The gap the dragged project would land in. A ghost that follows
                the pointer looks better but tells you less: what matters is
                where it goes, not where your finger is. */}
            {drag.overIndex === index && drag.draggingId !== null && (
              <div className="mx-2 mb-1 h-0.5 rounded-full bg-accent" />
            )}
            <div className={`group flex items-center gap-1 ${pack.header}`}>
              <span
                {...drag.handleProps(p.id)}
                data-testid="project-grip"
                title={t('project.reorder')}
                className="vp-tap -ml-1 cursor-grab rounded-md p-0.5 text-ink-2 vp-reveal active:cursor-grabbing"
              >
                <GripVertical size={12} />
              </span>
              <InlineName
                value={projectLabel(p)}
                onCommit={(next) => props.onRenameProject(p, next)}
                // `text-ink`, and no extra tracking. It is a heading; the
                // sessions under it are the detail. Tracking also widens CJK
                // names for nothing, and this panel has them.
                className={`${rank.group} min-w-0 flex-1 truncate font-semibold text-ink`}
                title={p.path}
              />
              <button
                type="button"
                onClick={() => props.onNewSession(p)}
                data-testid="project-new-shell"
                title={t('session.new')}
                className="vp-control vp-tap ml-auto vp-reveal"
              >
                <TerminalIcon size={13} />
              </button>
              {/* You can add a project by typing a path into a prompt, and the
                  first thing anybody does is type one. A path that is wrong but
                  happens to exist gives you a project you could not remove from
                  here at all — the endpoint, the CLI and even the client method
                  were all there, and nothing in the panel called it. */}
              <button
                type="button"
                onClick={() => props.onRemoveProject(p)}
                data-testid="project-remove"
                title={t('project.remove')}
                className="vp-control vp-tap vp-reveal"
              >
                <X size={13} />
              </button>
            </div>

            <div className={(byProject.get(p.id) ?? []).length > 0 ? pack.kids : ''}>
            {(byProject.get(p.id) ?? []).map((s) => {
              const isLive = props.live.includes(s.id)
              const isSelected = props.selected === s.id
              return (
                <div
                  key={s.id}
                  data-testid="session-row"
                  // The id, so a check can ask about *this* session's terminal
                  // rather than about whichever one it happens to find. The
                  // scale check waited on "any terminal has content", which is
                  // true of the one that was already on screen.
                  data-session-id={s.id}
                  className={`group flex cursor-pointer items-center gap-2 rounded-vp ${pack.row} transition-colors duration-200 ease-vp ${
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
                    className={`flex-1 ${rank.row}`}
                  />
                  {/* The glyph says "gone" and this says how. A shape cannot
                      carry an exit code, and 3 vs 0 is the difference between
                      "it crashed" and "it finished and closed". */}
                  {s.exited && (
                    <span
                      className={`shrink-0 text-vp-xs tabular ${
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
                    <span className="shrink-0 text-vp-xs text-ink-2">idle</span>
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
                      title={
                        s.exitStatus === EXIT_VANISHED
                            ? t('restore.gone')
                            : t('app.restartHintStatus', { n: s.exitStatus })
                      }
                      className="vp-control vp-tap"
                    >
                      <RotateCcw size={12} />
                    </button>
                  )}
                  {(rowControls(isSelected) || s.pinned) && (
                  <button
                    type="button"
                    onClick={(e) => {
                      e.stopPropagation()
                      props.onPinSession(s, !s.pinned)
                    }}
                    data-testid="pin-session"
                    title={s.pinned ? t('session.unpin') : t('session.pin')}
                    // Not vp-reveal while it is on. Being pinned is state you
                    // have to be able to see without hovering, so the row used
                    // to draw a second pin badge beside the name -- and on
                    // hover both were on screen at once, which is the reported
                    // 「按下后会出现两个icon」. One element: the control is the
                    // badge, and it stops hiding once it has something to say.
                    className={`vp-control vp-tap ${s.pinned ? 'text-accent' : 'vp-reveal'}`}
                  >
                    {s.pinned ? <PinOff size={12} /> : <Pin size={12} />}
                  </button>
                  )}
                  {rowControls(isSelected) && (
                  <button
                    type="button"
                    onClick={(e) => {
                      e.stopPropagation()
                      props.onKillSession(s)
                    }}
                    data-testid="kill-session"
                    title={t('session.kill')}
                    className="vp-control vp-tap vp-reveal"
                  >
                    <X size={12} />
                  </button>
                  )}
                </div>
              )
            })}
            </div>
          </section>
        ))}
        {drag.overIndex === projects.length && drag.draggingId !== null && (
          <div className="mx-2 h-0.5 rounded-full bg-accent" />
        )}
      </nav>

      {/* What this software is, at the bottom-left, always.
          
          「面板左下角等等地方 都加上GitHub链接和项目名」 was read as *your*
          project and its remote, so the corner changed with the selection and
          showed a link only for the projects that happen to be GitHub
          checkouts -- six of eleven on the machine it was reported from had no
          origin at all. 「左下角应当始终显示vibepanel的项目信息」: the slot is
          the panel's own attribution, not a second copy of where you are.
          
          Which project you are in is already answered twice over, by the
          heading the session sits under and by the header above the terminal.
          This was the third answer and the only one that could be blank.
          
          Static, because it is a fact about this source tree rather than about
          anything the panel is looking at -- so there is nothing to fetch, and
          nothing that can come back empty. Hidden in the collapsed rail: a 48px
          column has room for a glyph and not for two names. */}
      {props.expanded && (
        <div className="shrink-0 border-t border-hairline px-3 py-1.5">
          <ProjectMark testid="sidebar-project" name={PANEL_NAME} remote={PANEL_REPO} />
        </div>
      )}

      {/* Self-clearing: it disappears the moment anything reports state, so it
          is a statement of fact rather than a prompt to be dismissed. */}
      {props.stateGuessed && (
        <button
          type="button"
          data-testid="state-guessed-notice"
          onClick={props.onOpenSettings}
          className="vp-press border-t border-hairline px-3 py-2 text-left text-vp-sm leading-relaxed text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
        >
          {props.hooksInstalled ? t('guessed.installed') : t('guessed.notInstalled')}
        </button>
      )}
    </aside>
  )
}
