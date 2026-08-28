import { useEffect, useState } from 'react'
import { ChevronsUpDown, GitBranch } from 'lucide-react'

import { api } from '../../protocol/api'
import type { GitInfo } from '../../protocol/wire'
import { t, useLang } from '../../i18n'
import { safeText } from '../text'
import { dirtyTotal } from './git'

/**
 * How often the line refreshes while the files tab is on screen.
 *
 * Slower than the repository panel's five seconds, because this is one line
 * and the panel is fifteen. Every tick is a `git status` on the server — the
 * cache in internal/git collapses overlapping readers, but a request nobody is
 * reading is still a cost, which is what the hidden-document guard below is
 * for.
 */
const POLL_MS = 10000

/**
 * The repository, as one line above the file list.
 *
 * The repository used to be a tab, then the bottom half of the files tab. Both
 * were too much furniture for what it answers. What somebody watching agents
 * edit a directory actually wants at a glance is three things — which branch,
 * how far from the remote, how dirty — and those fit on the line above the
 * listing, next to the path they are about.
 *
 * The rest of it (the changed files, the recent commits, the open pull
 * requests, the sessions sitting on other branches) has not gone anywhere: the
 * line is a press target, and pressing it opens the whole repository panel with
 * the side panel to itself. Same gesture as the two blocks in the dock, and
 * deliberately so — one way to open a thing, in three places.
 *
 * A directory that is not a repository renders nothing at all. A file list with
 * an empty branch chip over it looks broken; a file list is a perfectly good
 * thing to be.
 */
export function RepoLine({
  projectId,
  onOpen,
}: {
  projectId: string
  /** Opens the full repository panel. See PanelDetail. */
  onOpen: () => void
}) {
  useLang()
  const [info, setInfo] = useState<GitInfo | null>(null)

  useEffect(() => {
    let cancelled = false
    let inFlight = false
    // Not reset to null on a project change: the caller keys the file tree by
    // project, so this is a fresh instance whose state is already null.
    const read = () => {
      // A background tab has no reader and the browser keeps the timer
      // running. Skipped rather than cleared, so coming back refreshes on the
      // next tick without a second listener.
      if (inFlight || document.hidden) return
      inFlight = true
      api.git(projectId).then(
        (v) => {
          inFlight = false
          if (!cancelled) setInfo(v)
        },
        () => {
          inFlight = false
          // Silent. A line that turns into an error message takes over the
          // header it was meant to sit quietly in, and the file list below it
          // is unaffected by whatever went wrong — the repository panel says
          // what happened when it is asked.
        },
      )
    }
    read()
    const timer = setInterval(read, POLL_MS)
    const wake = () => {
      if (!document.hidden) read()
    }
    document.addEventListener('visibilitychange', wake)
    return () => {
      cancelled = true
      clearInterval(timer)
      document.removeEventListener('visibilitychange', wake)
    }
  }, [projectId])

  if (!info || !info.status.repo) return null

  const s = info.status
  const dirty = dirtyTotal(s)
  const head = s.detached ? s.head : safeText(s.branch)

  return (
    <button
      type="button"
      data-testid="repo-line"
      onClick={onOpen}
      title={t('detail.open', { what: t('panel.git') })}
      aria-label={t('detail.open', { what: t('panel.git') })}
      className="vp-control w-full justify-start gap-1.5 px-2"
    >
      <GitBranch size={11} className="shrink-0" />
      <span className="min-w-0 truncate font-mono text-vp-xs text-ink">{head}</span>
      {/* Ahead and behind as a pair of signed counts rather than a colour: the
          direction is the whole of what they say, and an arrow alone is
          unreadable when both are zero. Hidden when they are. */}
      {(s.ahead > 0 || s.behind > 0) && (
        <span data-testid="repo-line-sync" className="tabular shrink-0 text-vp-xs">
          {t('git.aheadBehind', { a: s.ahead, b: s.behind })}
        </span>
      )}
      {/* A number and a word, never a dot. "3 changed" is legible in a dark
          room and an amber pip is not (red line 4). */}
      {dirty > 0 && (
        <span data-testid="repo-line-dirty" className="tabular shrink-0 text-vp-xs">
          {t('git.uncommitted', { n: dirty })}
        </span>
      )}
      {s.conflicted > 0 && (
        <span
          data-testid="repo-line-conflict"
          className="shrink-0 text-vp-xs"
          style={{ color: 'var(--vp-state-crashed)' }}
        >
          {t('git.conflictWord')}
        </span>
      )}
      <ChevronsUpDown size={11} className="ml-auto shrink-0" />
    </button>
  )
}
