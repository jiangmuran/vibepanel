import type { Session } from '../protocol/wire'
import { safeText } from './text'

/**
 * What to call a session on screen.
 *
 * One definition, imported by both the sidebar and the header, because it was
 * two: the same three lines copied into App.tsx and Sidebar.tsx. Nothing would
 * have caught them drifting, and the symptom — the row and the title bar
 * disagreeing about the name of the session you are looking at — reads as a
 * rendering glitch rather than as two functions.
 *
 * It cannot live in App.tsx: App renders Sidebar, so importing back the other
 * way is a cycle. It does not belong in wire.ts either, which describes what
 * the server sends rather than how it is shown.
 *
 * The fallbacks matter in order. `title` is what the server derived — the pane
 * title an agent set, or the directory for a shell. `command` is the honest
 * second best. The last one exists because a row with no label at all cannot be
 * clicked back into.
 */
export function sessionLabel(s: Session): string {
  // Sanitised here rather than at each call site, because this is the funnel:
  // `title` is whatever `pane_title` held, and any program running in a pane
  // sets that with a two-byte escape sequence. A title carrying U+202E renders
  // its own suffix backwards in the sidebar. See safeText.
  return safeText(s.title || s.command || 'session')
}

/**
 * What to call a project on screen.
 *
 * A project's name defaults to the basename of the directory it points at
 * (`filepath.Base` in handleCreateProject), so it is no more trustworthy than a
 * filename: an agent creates directories, and a directory name is arbitrary
 * bytes. It went to the sidebar, to a tooltip, and into the text of the
 * confirmation that asks before killing every session in it — none of them
 * sanitised, while the equivalent question about a *session* used
 * sessionLabel and was. Two paths, one of them updated.
 *
 * A funnel for the same reason as sessionLabel: sanitising at each call site is
 * how the next one gets forgotten.
 *
 * The fallbacks are the other half of what sessionLabel does, and were missing
 * when the sanitising was added. A session with no title falls back to its
 * command and then to the word "session"; a project fell back to nothing, so a
 * blank name left a sidebar row with no text — one that cannot be identified or
 * clicked back into to fix. InlineName trims before committing and refuses an
 * empty result, and the CLI substitutes the directory's base name for an empty
 * --name, so this is the third guard rather than the first; a name of nothing
 * but spaces still reaches the database through `--name "   "`, and covering
 * every entrance one at a time is how one of them stays open.
 */
export function projectLabel(p: { name: string; path?: string }): string {
  const name = safeText(p.name).trim()
  if (name) return name
  const base = (p.path ?? '').replace(/\/+$/, '').split('/').pop() ?? ''
  return safeText(base).trim() || 'project'
}

/**
 * What to call a scratch terminal in the strip under a session.
 *
 * No fallback to the command name, unlike sessionLabel. The server leaves a
 * scratch terminal's title empty precisely when it has no useful automatic
 * name — every shell is called "bash", and a strip of tabs all reading "bash"
 * tells you nothing. Trust that judgement rather than re-deriving it.
 *
 * Here rather than in BottomTerminals so that all three funnels are in one
 * file: it was the one that did not sanitise, and it was the one that lived
 * somewhere else.
 */
export function terminalLabel(s: Session, index: number): string {
  return safeText(s.title) || `term ${index + 1}`
}

/**
 * What to call a passkey.
 *
 * The fourth name-rendering site and the only one that was not funnelled: a
 * passkey's name was rendered raw in the list and again inside the
 * confirmation that asks before deleting it. Lower stakes than the others,
 * because the name comes from something the user typed rather than from a
 * directory basename or a `pane_title` an agent set -- but "lower stakes"
 * described the project-name site too, one file over, until it did not.
 *
 * The confirm is the one that matters. A dialog is the last thing between a
 * credential and being deleted, and a name carrying a bidi override can make it
 * ask about a different key than the one it will remove.
 */
export function passkeyLabel(k: { name?: string }): string {
  return safeText(k.name ?? '').trim() || 'Passkey'
}

/**
 * Labels that can tell sessions apart.
 *
 * A shell is named after the directory it sits in, so a project containing
 * `services/web` and `admin/web` shows two rows both reading "web". The sidebar
 * exists to answer "which one needs me", and two rows with the same name in the
 * same group cannot answer it — you click one to find out, which is exactly the
 * thing this panel was built to stop.
 *
 * Disambiguated within a project, not globally: the sidebar groups by project
 * and prints the project name above the group, so the same name under two
 * different projects is already distinguished by where it is. Doing it globally
 * would add a qualifier to rows that never needed one.
 *
 * One level of parent is enough for the case that actually happens. Two
 * sessions in the *same* directory really are indistinguishable by anything the
 * machine knows — that is what renaming is for, and renaming is two clicks or
 * one long press away.
 */
export function disambiguatedLabels(sessions: Session[]): Map<string, string> {
  const out = new Map<string, string>()
  const byProject = new Map<string, Session[]>()
  for (const s of sessions) {
    const group = byProject.get(s.projectId)
    if (group) group.push(s)
    else byProject.set(s.projectId, [s])
  }

  for (const group of byProject.values()) {
    const counts = new Map<string, number>()
    for (const s of group) {
      const label = sessionLabel(s)
      counts.set(label, (counts.get(label) ?? 0) + 1)
    }
    for (const s of group) {
      const label = sessionLabel(s)
      const parent = (counts.get(label) ?? 0) > 1 ? parentName(s.cwd) : ''
      out.set(s.id, parent ? `${parent}/${label}` : label)
    }
  }
  return out
}

/** The name of the directory containing `path`, or '' if there is not one. */
function parentName(path: string): string {
  const parts = path.split('/').filter(Boolean)
  return parts.length >= 2 ? parts[parts.length - 2] : ''
}

/**
 * Why a pane stopped, in words rather than a number.
 *
 * tmux reports a kill and an exit differently: `pane_dead_status` is the wait
 * status of a process that returned, and it is empty for one that was killed,
 * where `pane_dead_signal` holds the signal instead. The panel read only the
 * first, so an agent the OOM killer took reported "exited with status 0" — the
 * same as one that finished its work.
 *
 * The server now carries 128+signal, which is what every shell does. Here that
 * is turned back into something a person recognises at 2am: "killed (SIGKILL)"
 * is the machine running out of memory, and it should not look like success.
 */
const SIGNAL_NAMES: Record<number, string> = {
  1: 'SIGHUP',
  2: 'SIGINT',
  6: 'SIGABRT',
  9: 'SIGKILL',
  11: 'SIGSEGV',
  13: 'SIGPIPE',
  15: 'SIGTERM',
}

export function exitReason(status: number): string {
  if (status === 0) return 'exited'
  // 128 is the shell convention's base; nothing sends signal 0.
  if (status > 128 && status < 128 + 64) {
    const sig = status - 128
    const name = SIGNAL_NAMES[sig]
    return name ? `killed (${name})` : `killed by signal ${sig}`
  }
  return `exited with status ${status}`
}
