import type { GitPR, GitStatus } from '../../protocol/wire'

/**
 * What the git tab works out before it draws anything.
 *
 * Separate from the component for the same reason preview.ts is: none of it
 * needs a DOM, and the parts worth getting wrong are the joins and the
 * vocabulary, not the markup.
 */

/** Everything uncommitted, in one number. */
export function dirtyTotal(s: GitStatus): number {
  return s.staged + s.unstaged + s.untracked + s.conflicted
}

/**
 * The pull request whose head branch is this one, if there is one.
 *
 * This join is the reason the GitHub half exists at all. The question somebody
 * with six agents on one repository has is not "what is open upstream", it is
 * "is the branch this agent is on green" — and the only thing connecting the
 * two sides is a branch name.
 *
 * Exact match, never a prefix. `feat/auth` and `feat/auth-2` are different
 * branches with different pull requests, and showing the wrong one's checks
 * against a branch is worse than showing none: it is a green tick on work that
 * was never tested.
 */
export function prForBranch(prs: GitPR[], branch: string): GitPR | null {
  if (!branch) return null
  return prs.find((p) => p.branch === branch) ?? null
}

/**
 * The five answers GitHub's check rollup gives, mapped to a word and a shape.
 *
 * Red line 4: a failing check is a cross and the word "failing", not a red dot.
 * The `tone` is on top of that, never instead of it.
 *
 * `expected` is GitHub's word for a check that has been declared and has not
 * started, and it is folded into pending because the difference is not one
 * anybody watching a branch can act on.
 */
export type CheckTone = 'good' | 'bad' | 'wait' | 'none'

export function checkTone(state: string): CheckTone {
  switch (state) {
    case 'success':
      return 'good'
    case 'failure':
    case 'error':
      return 'bad'
    case 'pending':
    case 'expected':
      return 'wait'
    default:
      // Including the empty string, which is what a pull request with no CI at
      // all reports. "No checks" is a fact, not a failure, and it must not
      // render as one.
      return 'none'
  }
}

/**
 * Whether a review decision blocks the branch.
 *
 * GitHub sends `approved`, `changes_requested`, `review_required` or nothing.
 * Nothing means no review is required on that base, which is not the same as
 * "nobody has looked" — the panel says the word it was given and draws no
 * conclusion.
 */
export function reviewTone(decision: string): CheckTone {
  switch (decision) {
    case 'approved':
      return 'good'
    case 'changes_requested':
      return 'bad'
    case 'review_required':
      return 'wait'
    default:
      return 'none'
  }
}
