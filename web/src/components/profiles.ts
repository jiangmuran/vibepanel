import { tKey } from '../i18n'
import type { LaunchProfile } from '../protocol/wire'

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
