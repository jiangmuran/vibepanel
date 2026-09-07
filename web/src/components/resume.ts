/**
 * What a restored session will actually run.
 *
 * A mirror of internal/session/resume.go, and a deliberate one. The dialog's
 * whole justification is that it prints the argv before anything launches two
 * dozen agents on somebody's machine, so it has to print the argv that will
 * run rather than the one that was recorded — and the server is where the
 * decision belongs, because the server is what runs it.
 *
 * Two implementations of one rule is the drift AGENTS.md红线 3 is about, so
 * `resume.test.ts` reads resume.go as text and fails when the two tables stop
 * agreeing. That is cruder than a generated file and it runs on every commit,
 * which the generator this project has never had does not.
 *
 * The reasons — why these two agents, why not opencode, what resuming does and
 * does not bring back — are in the Go file. They are not repeated here, where
 * they would be the copy that goes stale.
 *
 * One rule is the server's alone and is not mirrored: it also declines to
 * resume when the recorded directory has been deleted, because the session then
 * comes back in the project's directory instead and "the most recent
 * conversation here" is somebody else's. A browser cannot stat the server's
 * disk, so this file cannot know. The consequence is bounded and one-way — a
 * row that says it will resume and comes back cold, never the reverse — and the
 * banner printed into the pane is what says which actually happened.
 */

/** Sessions being restored, as much of one as this needs. */
export interface ResumeCandidate {
  launchCommand: string[]
  cwd: string
}

function baseName(p: string): string {
  const i = p.lastIndexOf('/')
  return i >= 0 ? p.slice(i + 1) : p
}

/**
 * The argv that brings the conversation back, or null when nothing better than
 * the recorded one exists.
 */
export function resumeArgv(launch: readonly string[]): string[] | null {
  if (launch.length === 0) return null
  const rest = launch.slice(1)
  switch (baseName(launch[0])) {
    case 'claude':
      if (rest.some((a) => a === '-c' || a === '--continue' || a === '-r' || a === '--resume')) {
        return null
      }
      return [launch[0], '--continue', ...rest]
    case 'codex':
      if (rest[0] === 'resume') return null
      return [launch[0], 'resume', '--last', ...rest]
    default:
      return null
  }
}

/** The agent an argv would resume, or '' for one that would not. */
export function resumeProgram(launch: readonly string[]): string {
  return resumeArgv(launch) === null ? '' : baseName(launch[0])
}

/**
 * Whether more than one of these sessions would resume out of one directory.
 *
 * Every session is the input — not the ticked ones, and not only the dead ones.
 * A session still running in that directory is holding the most recent
 * conversation in it, so a dead sibling resuming there would attach to the live
 * one's transcript. Restoring a batch also revives rows as it goes, so a set
 * that counted only the dead would shrink underneath itself and let the last of
 * a pair resume onto the conversation the first had just taken.
 *
 * It is the same list the server reads, which is what lets the two sides agree
 * without asking each other.
 */
export function resumeIsAmbiguous(
  rows: readonly ResumeCandidate[],
  program: string,
  dir: string,
): boolean {
  if (!program || !dir) return false
  let n = 0
  for (const row of rows) {
    if (row.launchCommand.length === 0 || row.cwd !== dir) continue
    if (resumeProgram(row.launchCommand) !== program) continue
    if (++n > 1) return true
  }
  return false
}

/**
 * What one row will run, and whether that resumes anything.
 *
 * `null` means the row has no recorded argv at all and comes back as a login
 * shell, which the dialog already has its own two sentences for.
 */
export function restoreLaunch(
  row: ResumeCandidate,
  all: readonly ResumeCandidate[],
): { argv: string[]; resumed: boolean } | null {
  if (row.launchCommand.length === 0) return null
  const resumed = resumeArgv(row.launchCommand)
  if (resumed === null) return { argv: [...row.launchCommand], resumed: false }
  if (resumeIsAmbiguous(all, resumeProgram(row.launchCommand), row.cwd)) {
    return { argv: [...row.launchCommand], resumed: false }
  }
  return { argv: resumed, resumed: true }
}
