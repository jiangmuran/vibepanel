import { describe, expect, it } from 'vitest'

import { agoParts, checkTone, dirtyTotal, prForBranch, reviewTone } from './git'
import type { GitPR, GitStatus } from '../../protocol/wire'

const status = (over: Partial<GitStatus> = {}): GitStatus => ({
  repo: true,
  branch: 'main',
  detached: false,
  head: 'abc1234',
  upstream: 'origin/main',
  ahead: 0,
  behind: 0,
  staged: 0,
  unstaged: 0,
  untracked: 0,
  conflicted: 0,
  changes: [],
  changesTruncated: false,
  ...over,
})

const pr = (over: Partial<GitPR> = {}): GitPR => ({
  number: 1,
  title: 'a change',
  branch: 'feat/auth',
  base: 'main',
  draft: false,
  author: 'someone',
  url: 'https://github.com/o/r/pull/1',
  updatedAt: 0,
  review: '',
  checks: '',
  ...over,
})

describe('how dirty the tree is', () => {
  it('counts every kind, because a conflict is not an untracked file', () => {
    expect(dirtyTotal(status())).toBe(0)
    expect(dirtyTotal(status({ staged: 1, unstaged: 2, untracked: 3, conflicted: 4 }))).toBe(10)
  })

  it('is zero only when nothing at all is outstanding', () => {
    // Each of the four on its own. A summary that forgets one of them says
    // "clean" about a tree with a merge conflict in it.
    for (const key of ['staged', 'unstaged', 'untracked', 'conflicted'] as const) {
      expect(dirtyTotal(status({ [key]: 1 }))).toBe(1)
    }
  })
})

describe('joining a branch to its pull request', () => {
  it('matches exactly, never by prefix', () => {
    // The failure this rules out is a green tick on the wrong branch: showing
    // feat/auth's passing checks against feat/auth-2 is worse than showing
    // nothing at all.
    const prs = [pr({ branch: 'feat/auth', number: 7 })]
    expect(prForBranch(prs, 'feat/auth')?.number).toBe(7)
    expect(prForBranch(prs, 'feat/auth-2')).toBeNull()
    expect(prForBranch(prs, 'feat')).toBeNull()
  })

  it('has nothing to say about a detached head', () => {
    expect(prForBranch([pr()], '')).toBeNull()
    // With a branch in the list that is also empty, which is what makes the
    // guard load-bearing rather than incidental. `branch` arrives from
    // JSON.parse cast to an interface, so "GitHub would never send that" is
    // not a property this side gets to rely on — and matching it would put
    // somebody else's checks against a session with no branch at all.
    expect(prForBranch([pr({ branch: '' })], '')).toBeNull()
  })
})

describe('the words for a check rollup', () => {
  it('separates passing, failing and still running', () => {
    expect(checkTone('success')).toBe('good')
    expect(checkTone('failure')).toBe('bad')
    expect(checkTone('error')).toBe('bad')
    expect(checkTone('pending')).toBe('wait')
    expect(checkTone('expected')).toBe('wait')
  })

  it('does not render "no checks" as a failure', () => {
    // A repository with no CI reports an empty rollup. Calling that red is a
    // panel that tells you every branch is broken.
    expect(checkTone('')).toBe('none')
    expect(checkTone('something_new')).toBe('none')
  })
})

describe('the words for a review decision', () => {
  it('says what GitHub said and draws no conclusion', () => {
    expect(reviewTone('approved')).toBe('good')
    expect(reviewTone('changes_requested')).toBe('bad')
    expect(reviewTone('review_required')).toBe('wait')
    // No review required on this base is not "nobody looked".
    expect(reviewTone('')).toBe('none')
  })
})

describe('how long ago something happened', () => {
  const now = 1_700_000_000

  it('climbs a unit at a time', () => {
    expect(agoParts(now - 30, now)).toEqual({ value: -30, unit: 'second' })
    expect(agoParts(now - 120, now)).toEqual({ value: -2, unit: 'minute' })
    expect(agoParts(now - 7200, now)).toEqual({ value: -2, unit: 'hour' })
    expect(agoParts(now - 3 * 86400, now)).toEqual({ value: -3, unit: 'day' })
    expect(agoParts(now - 60 * 86400, now)).toEqual({ value: -2, unit: 'month' })
    expect(agoParts(now - 800 * 86400, now)).toEqual({ value: -2, unit: 'year' })
  })

  it('clamps a commit from the future to now', () => {
    // Clocks differ across machines that share a repository, and "in 3 hours"
    // under a commit reads as the panel being broken rather than as the clock
    // being wrong.
    expect(agoParts(now + 10_000, now)).toEqual({ value: 0, unit: 'second' })
  })
})
