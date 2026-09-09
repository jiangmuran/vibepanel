import { tKey } from '../i18n'
import type { LaunchEnvVar, LaunchProfile } from '../protocol/wire'

/** Mirrors store.BuiltinPrefix. */
export const BUILTIN_PREFIX = 'builtin:'

/**
 * The name to show for a profile.
 *
 * A built-in's name is the server's, so this side cannot type-check it: a
 * profile from a newer build can name one this build has never heard of. The
 * fallback is the server's English name rather than the id, because a picker
 * reading "builtin:claude" has put an internal identifier in front of somebody
 * about to press it. A Go test walks the catalogue and fails if an id here has
 * no dictionary entry, so the fallback is for a *future* server rather than for
 * a translation somebody forgot.
 *
 * One expression, and the two guards that used to be above it are gone. Both
 * looked like rules -- "a row's name is never translated", "an id outside the
 * built-in namespace is not looked up" -- and neither changed a single output,
 * because a row's id has no dictionary entry and every path ends at `p.name`
 * anyway. Removing each of them left every test green, which is how they were
 * found and why they are not here: a line that reads as a rule and enforces
 * nothing is the kind the next person preserves at a cost.
 */
export function profileLabel(p: LaunchProfile): string {
  return tKey(`profile.name.${p.id}`) ?? p.name
}

/**
 * Whether a variable name looks like it holds a credential.
 *
 * Only the initial state of a checkbox in the form — the server decides nothing
 * from it, and the person adding the variable can change it. Getting it wrong
 * in the safe direction costs a value that has to be retyped to edit; getting
 * it wrong in the other costs a key on screen. So it errs towards secret, and
 * that is why it matches on a substring rather than on a whole name.
 */
export function looksSecret(name: string): boolean {
  return /KEY|TOKEN|SECRET|PASSWORD|PASSWD|CREDENTIAL/i.test(name)
}

/**
 * How many of a profile's variables will actually be set.
 *
 * Not `env.length`. A built-in is a list of variable names with nothing in
 * them, and a picker row saying "2 variables" next to a profile that sets none
 * would be describing the form rather than the session.
 */
export function envCount(p: LaunchProfile): number {
  return p.env.filter((v) => v.hasValue).length
}

/**
 * The profile a session says it was started with, or null.
 *
 * Returns null both for "no profile" and for "a profile that has since been
 * deleted", and the two are told apart by the caller looking at whether the id
 * was empty — which is what lets the restore dialog say the profile is gone
 * rather than implying the session never had one. An empty id finds nothing
 * here because no profile has an empty id, so there is no guard for it: one
 * would be a line that reads as a rule and returns the same answer.
 */
export function profileOf(profiles: LaunchProfile[], id: string): LaunchProfile | null {
  return profiles.find((p) => p.id === id) ?? null
}

/**
 * The variables an agent reads, for a command somebody typed.
 *
 * Derived from the catalogue the server already sent rather than from a table
 * on this side. That is the whole design of it: the names live in
 * `store.builtinProfiles`, a second copy here would be a second thing to keep
 * right, and red line 3 is a list of what happens when two places describe one
 * fact. The picker fetches the catalogue on every page load anyway.
 *
 * Matched on the program alone -- the basename of argv[0] -- because
 * `claude --model x` and `/usr/local/bin/claude` are the same agent and both
 * are what people actually type. A profile whose command is a shell, a build
 * or an agent this build has never heard of gets nothing, which is the honest
 * answer: guessing a variable name for opencode is what the catalogue's own
 * comment refuses to do, and it is refused here for the same reason.
 *
 * A hidden built-in is still in `all` for this purpose only if the server sent
 * it, and it does not -- so removing Codex from the list also removes its
 * template. That is the right way round: somebody who took an agent out of
 * their panel is not the person who wants its variables offered.
 */
export function envTemplateFor(command: string[], all: LaunchProfile[]): LaunchEnvVar[] {
  const program = basename(command[0] ?? '')
  if (!program) return []
  for (const p of all) {
    if (!p.builtin || p.env.length === 0) continue
    if (basename(p.command[0] ?? '') !== program) continue
    // Names and the secret flag; never a value. An empty value is not passed
    // to the process at all, so a form filled from this runs the agent exactly
    // as a bare terminal would until somebody types something in.
    return p.env.map((v) => ({ name: v.name, value: '', secret: v.secret, hasValue: false }))
  }
  return []
}

/** The last path segment, with no dependency on how the path is spelled. */
function basename(argv0: string): string {
  const cut = argv0.lastIndexOf('/')
  return cut < 0 ? argv0 : argv0.slice(cut + 1)
}
