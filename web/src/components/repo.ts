import type { GitRemote } from '../protocol/wire'

/**
 * A repository URL, built from parts the server has already validated.
 *
 * Never from `remote.url`. That string is whatever is in somebody's git config
 * — an ssh remote, a relative path, a host this panel has never heard of — and
 * putting it in an `href` is letting a file on disk decide where a click goes.
 * `internal/git` parses it once, refuses anything that is not github.com, and
 * hands back an owner and a name; this builds a URL out of those two and
 * nothing else, and answers null when either is missing.
 *
 * The character class is a second wall rather than the only one. The server's
 * parser already refuses a name with a slash or a colon in it — but the server
 * is not the side that writes the `href`, and a wall on the side that does is
 * the difference between "the parser is correct" and "the link is safe". The
 * cases it has to hold against are all the same shape: something that makes
 * the browser read the path as more than two segments, or as a different
 * scheme, or as a different host.
 *
 * Its own module rather than beside the component, so it can be tested without
 * a DOM and so the next surface that wants a repository link imports the
 * decision rather than re-deriving it.
 */
export function githubURL(remote: GitRemote | null | undefined): string | null {
  if (!remote) return null
  if (remote.host !== 'github.com') return null
  if (remote.owner === '' || remote.name === '') return null
  if (!segment(remote.owner) || !segment(remote.name)) return null
  return `https://github.com/${remote.owner}/${remote.name}`
}

/**
 * One path segment that means only itself.
 *
 * The character class is the obvious half. The other half is `.` and `..`,
 * which pass it and are the reason this is a function rather than one regular
 * expression: `https://github.com/../payroll` is a valid URL that resolves to
 * `https://github.com/payroll`, so a repository called `..` — or a parser bug
 * that ever produced one — would send the reader to somebody else's project
 * under this project's name. Found by the test that asserts every URL this
 * builds has exactly two path segments.
 */
function segment(v: string): boolean {
  if (v === '.' || v === '..') return false
  return /^[A-Za-z0-9._-]+$/.test(v)
}
