import { useCallback, useEffect, useState } from 'react'
import {
  AlertTriangle,
  CheckCircle2,
  CircleDashed,
  Clock,
  GitBranch,
  GitPullRequest,
  Loader2,
  RefreshCw,
  XCircle,
} from 'lucide-react'

import { api } from '../../protocol/api'
import type { GitHubResult, GitInfo, GitPR, GitSession, Session } from '../../protocol/wire'
import { getLang, t, useLang, type Key } from '../../i18n'
import { safeText } from '../text'
import { StateDot } from '../StateDot'
import { agoParts } from './ago'
import { checkTone, dirtyTotal, prForBranch, reviewTone, type CheckTone } from './git'

/**
 * What the repository is doing, for somebody watching agents edit it.
 *
 * Four questions, in the order they get asked, and nothing else. What branch am
 * I on and how far from the remote. What is uncommitted. What just landed. What
 * is open upstream.
 *
 * The temptation here is a git client — a diff viewer, a blame, a branch
 * switcher, a stash list. All of it exists one keystroke away in the session
 * sitting right above this panel, in a real terminal with a real pager, and
 * none of it answers a question you have *while watching six agents work*. The
 * things that do are the ones this shows: whose branch is which, whose tree is
 * dirty, and whose pull request is red.
 *
 * The first three come off the disk with no credential and no network, and that
 * is what always works. The fourth needs a token and a button press, and its
 * absence costs one line.
 */

/**
 * How often the local half refreshes while it is on screen.
 *
 * Every tick is a `git status`, a `git log` and a `git remote` on the server,
 * plus one status per worktree a session is sitting in. The server collapses
 * overlapping readers — see internal/git/cache.go, whose TTL is deliberately
 * shorter than this number and has a test naming it — but the request this
 * timer makes when nobody is looking is a cost with no reader at all, which is
 * what the hidden-document check below is for.
 */
const POLL_MS = 5000

/** A word and a shape for a tone, never a colour on its own. Red line 4. */
function ToneMark({ tone, label }: { tone: CheckTone; label: string }) {
  const Icon =
    tone === 'good' ? CheckCircle2 : tone === 'bad' ? XCircle : tone === 'wait' ? Clock : CircleDashed
  const colour =
    tone === 'good'
      ? 'var(--vp-state-done)'
      : tone === 'bad'
        ? 'var(--vp-state-crashed)'
        : tone === 'wait'
          ? 'var(--vp-state-working)'
          : 'var(--vp-ink-2)'
  return (
    <span
      className="inline-flex shrink-0 items-center gap-1 text-vp-xs"
      style={{ color: colour }}
      data-testid="git-tone"
      data-tone={tone}
    >
      <Icon size={11} />
      {label}
    </span>
  )
}

/**
 * How long ago, in the reader's language, from the browser's own tables.
 *
 * `now` is a prop rather than a `Date.now()` in the body. Two reasons and both
 * are real: a clock read during render is a value that changes without the
 * component being told, which React's purity rule refuses outright; and one
 * clock for the whole panel means fifteen commit rows cannot disagree about
 * what time it is.
 */
function Ago({ when, now }: { when: number; now: number }) {
  const { value, unit } = agoParts(when, now)
  const fmt = new Intl.RelativeTimeFormat(getLang() === 'zh' ? 'zh-CN' : 'en', { numeric: 'auto' })
  return <span className="tabular shrink-0 text-ink-2">{fmt.format(value, unit)}</span>
}

/** The counts, each with its own word. A single "12 changes" hides the one that
 *  matters, which is the conflict. */
function DirtyLine({ info }: { info: GitInfo }) {
  const s = info.status
  const parts: { key: Key; n: number; testid: string }[] = [
    { key: 'git.staged', n: s.staged, testid: 'git-staged' },
    { key: 'git.unstaged', n: s.unstaged, testid: 'git-unstaged' },
    { key: 'git.untracked', n: s.untracked, testid: 'git-untracked' },
    { key: 'git.conflicted', n: s.conflicted, testid: 'git-conflicted' },
  ]
  if (dirtyTotal(s) === 0) {
    return (
      <p data-testid="git-clean" className="text-vp-sm text-ink-2">
        {t('git.clean')}
      </p>
    )
  }
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-vp-sm">
      {parts
        .filter((p) => p.n > 0)
        .map((p) => (
          <span key={p.key} data-testid={p.testid} className="text-ink">
            <span className="tabular">{p.n}</span> {t(p.key)}
          </span>
        ))}
      {s.conflicted > 0 && (
        <span className="inline-flex items-center gap-1 text-vp-xs" style={{ color: 'var(--vp-state-crashed)' }}>
          <AlertTriangle size={11} />
          {t('git.conflictWord')}
        </span>
      )}
    </div>
  )
}

/** One open pull request. */
function PRRow({ pr }: { pr: GitPR }) {
  return (
    <a
      href={pr.url}
      target="_blank"
      rel="noreferrer noopener"
      data-testid="git-pr"
      className="vp-press block rounded-md px-1.5 py-1 transition-colors duration-200 ease-vp hover:bg-surface-2"
    >
      <div className="flex items-baseline gap-1.5">
        <GitPullRequest size={11} className="shrink-0 self-center text-ink-2" />
        <span className="tabular shrink-0 text-vp-xs text-ink-2">#{pr.number}</span>
        <span className="min-w-0 flex-1 truncate text-vp-sm text-ink" title={safeText(pr.title)}>
          {safeText(pr.title)}
        </span>
        {pr.draft && (
          <span data-testid="git-pr-draft" className="shrink-0 text-vp-xs text-ink-2">
            {t('git.draft')}
          </span>
        )}
      </div>
      <div className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-0.5 pl-4">
        <span className="min-w-0 truncate font-mono text-vp-xs text-ink-2" title={safeText(pr.branch)}>
          {safeText(pr.branch)}
        </span>
        <ToneMark tone={checkTone(pr.checks)} label={t(checkLabel(pr.checks))} />
        {pr.review !== '' && <ToneMark tone={reviewTone(pr.review)} label={t(reviewLabel(pr.review))} />}
      </div>
    </a>
  )
}

/** The dictionary key for a rollup state, so the word is translated rather than
 *  echoed from GitHub in English under a Chinese heading. */
function checkLabel(state: string): Key {
  switch (checkTone(state)) {
    case 'good':
      return 'git.checksPass'
    case 'bad':
      return 'git.checksFail'
    case 'wait':
      return 'git.checksRunning'
    default:
      return 'git.checksNone'
  }
}

function reviewLabel(decision: string): Key {
  switch (reviewTone(decision)) {
    case 'good':
      return 'git.reviewApproved'
    case 'bad':
      return 'git.reviewChanges'
    case 'wait':
      return 'git.reviewRequired'
    default:
      return 'git.reviewNone'
  }
}

/** One session sitting somewhere other than where the project is. */
function SessionRow({
  row,
  session,
  prs,
}: {
  row: GitSession
  session: Session | undefined
  prs: GitPR[]
}) {
  const pr = prForBranch(prs, row.branch)
  const dirty = row.staged + row.unstaged + row.untracked + row.conflicted
  return (
    <div className="mb-1.5" data-testid="git-session">
      <div className="flex items-baseline gap-1.5">
        {session && (
          <StateDot
            state={session.state}
            size={9}
            exited={session.exited}
            exitStatus={session.exitStatus}
          />
        )}
        <span className="min-w-0 flex-1 truncate text-vp-sm text-ink">
          {safeText(session?.title ?? row.sessionId)}
        </span>
      </div>
      <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5 pl-4 text-vp-xs">
        <span className="min-w-0 truncate font-mono text-ink-2">
          {row.detached ? row.head : safeText(row.branch)}
        </span>
        {dirty > 0 && (
          <span data-testid="git-session-dirty" className="tabular text-ink-2">
            {t('git.uncommitted', { n: dirty })}
          </span>
        )}
        {(row.ahead > 0 || row.behind > 0) && (
          <span className="tabular text-ink-2">{t('git.aheadBehind', { a: row.ahead, b: row.behind })}</span>
        )}
        {pr && <ToneMark tone={checkTone(pr.checks)} label={`#${pr.number} ${t(checkLabel(pr.checks))}`} />}
      </div>
    </div>
  )
}

export function GitPanel({ projectId, sessions }: { projectId: string; sessions: Session[] }) {
  useLang()
  const [info, setInfo] = useState<GitInfo | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [hub, setHub] = useState<GitHubResult | null>(null)
  const [hubError, setHubError] = useState<string | null>(null)
  const [asking, setAsking] = useState(false)
  // One clock for every relative time on the tab, ticked by the same effect
  // that polls. See Ago.
  const [now, setNow] = useState(() => Math.floor(Date.now() / 1000))

  useEffect(() => {
    let cancelled = false
    // Not overlapping. A tree big enough that `git status` takes longer than
    // the poll interval — a monorepo, a cold cache, a network filesystem — is
    // exactly the tree where a timer that fires anyway would queue reads
    // faster than the server can answer them, and the panel would be the
    // reason the repository is slow.
    let inFlight = false
    const read = () => {
      // A background tab has no reader, and the browser keeps the timer
      // running regardless. Skipped rather than cleared, so coming back to the
      // tab refreshes on the next tick without an extra listener.
      if (inFlight || document.hidden) return
      inFlight = true
      setNow(Math.floor(Date.now() / 1000))
      api.git(projectId).then(
        (v) => {
          inFlight = false
          if (cancelled) return
          setInfo(v)
          setError(null)
        },
        (e: unknown) => {
          inFlight = false
          if (!cancelled) setError(e instanceof Error ? e.message : String(e))
        },
      )
    }
    read()
    const timer = setInterval(read, POLL_MS)
    // Coming back to a hidden tab should not wait out a whole interval to
    // stop showing a minutes-old tree.
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

  // The one outbound request, and it happens here and nowhere else: inside a
  // click handler, with no timer and no retry. Anything that put this on a
  // schedule would turn "the panel does not phone home" into a sentence the
  // product no longer means.
  const ask = useCallback(() => {
    setAsking(true)
    setHubError(null)
    api.github(projectId).then(
      (v) => {
        setHub(v)
        setAsking(false)
      },
      (e: unknown) => {
        setHubError(e instanceof Error ? e.message : String(e))
        setAsking(false)
      },
    )
  }, [projectId])

  if (error) {
    return (
      <p data-testid="git-error" className="px-3 py-4 text-vp-sm text-ink-2">
        {safeText(error)}
      </p>
    )
  }
  if (!info) {
    return (
      <p className="flex items-center gap-2 px-3 py-4 text-vp-sm text-ink-2">
        <Loader2 size={12} className="animate-spin" />
        {t('git.reading')}
      </p>
    )
  }
  if (!info.status.repo) {
    return (
      <p data-testid="git-none" className="px-3 py-4 text-vp-sm text-ink-2">
        {t('git.notARepo')}
      </p>
    )
  }

  const s = info.status
  const prs = hub?.prs ?? []
  const byId = new Map(sessions.map((x) => [x.id, x]))

  return (
    <div className="px-3 py-2" data-testid="git-panel">
      {/* Where you are. The branch is the largest thing here because it is the
          answer people come for. */}
      <div className="flex items-baseline gap-1.5">
        <GitBranch size={12} className="shrink-0 self-center text-ink-2" />
        <span
          data-testid="git-branch"
          className="min-w-0 flex-1 truncate font-mono text-vp-base text-ink"
          title={s.detached ? s.head : safeText(s.branch)}
        >
          {s.detached ? s.head : safeText(s.branch)}
        </span>
        {s.detached && (
          <span data-testid="git-detached" className="shrink-0 text-vp-xs text-ink-2">
            {t('git.detached')}
          </span>
        )}
      </div>
      <div className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-0.5 pl-[18px] text-vp-xs text-ink-2">
        {s.upstream ? (
          <span className="min-w-0 truncate font-mono" title={safeText(s.upstream)}>
            {safeText(s.upstream)}
          </span>
        ) : (
          <span data-testid="git-no-upstream">{t('git.noUpstream')}</span>
        )}
        {(s.ahead > 0 || s.behind > 0) && (
          <span data-testid="git-aheadbehind" className="tabular">
            {t('git.aheadBehind', { a: s.ahead, b: s.behind })}
          </span>
        )}
      </div>

      <div className="mt-3 border-t border-hairline pt-2">
        <DirtyLine info={info} />
        {s.changes.length > 0 && (
          <div className="mt-1.5 max-h-40 overflow-y-auto">
            {s.changes.map((c, i) => (
              <div
                key={`${c.kind}:${c.path}:${i}`}
                data-testid="git-change"
                data-kind={c.kind}
                className="flex items-baseline gap-1.5 text-vp-xs"
              >
                {/* The kind as a word, in a fixed column. A two-letter porcelain
                    code is precise and unreadable, and a colour alone would be
                    red line 4. */}
                <span className="w-14 shrink-0 text-ink-2">{t(kindLabel(c.kind))}</span>
                <span className="min-w-0 flex-1 truncate font-mono text-ink" title={safeText(c.path)}>
                  {safeText(c.path)}
                </span>
              </div>
            ))}
            {s.changesTruncated && (
              <p data-testid="git-changes-truncated" className="mt-1 text-vp-xs text-ink-2">
                {t('git.changesTruncated', { n: dirtyTotal(s) })}
              </p>
            )}
          </div>
        )}
      </div>

      {info.sessions.length > 0 && (
        <div className="mt-3 border-t border-hairline pt-2">
          <h3 className="mb-1.5 text-vp-xs text-ink-2">{t('git.elsewhere')}</h3>
          {info.sessions.map((row) => (
            <SessionRow key={row.sessionId} row={row} session={byId.get(row.sessionId)} prs={prs} />
          ))}
          {info.sessionsTruncated && (
            <p className="text-vp-xs text-ink-2">{t('git.sessionsTruncated')}</p>
          )}
        </div>
      )}

      {info.commits.length > 0 && (
        <div className="mt-3 border-t border-hairline pt-2">
          <h3 className="mb-1.5 text-vp-xs text-ink-2">{t('git.recent')}</h3>
          {info.commits.map((c) => (
            <div key={c.sha} data-testid="git-commit" className="mb-1 flex items-baseline gap-1.5 text-vp-xs">
              <span className="tabular shrink-0 font-mono text-ink-2">{c.sha.slice(0, 7)}</span>
              <span className="min-w-0 flex-1 truncate text-ink" title={safeText(c.subject)}>
                {safeText(c.subject)}
              </span>
              <Ago when={c.when} now={now} />
            </div>
          ))}
        </div>
      )}

      <div className="mt-3 border-t border-hairline pt-2">
        <div className="flex items-baseline gap-2">
          <h3 className="min-w-0 flex-1 text-vp-xs text-ink-2">{t('git.upstream')}</h3>
          {info.github && info.tokenSet && (
            <button
              type="button"
              onClick={ask}
              disabled={asking}
              data-testid="git-ask"
              title={t('git.ask')}
              // .vp-control, not a class list of its own. The one button on
              // this tab is the same object as every other button in the
              // chrome, and a hand-written copy is how the strip below the
              // terminal ended up three different heights.
              className="vp-control text-vp-xs disabled:opacity-50"
            >
              <RefreshCw size={11} className={asking ? 'animate-spin' : undefined} />
              {t('git.ask')}
            </button>
          )}
        </div>
        {/* There was a line here saying nothing on this tab reaches the
            network until the button is pressed. True, and an argument: it
            defends the design to somebody who had not asked. The two lines
            below are different — each one names a thing that is missing and
            what would fix it, which is a fact the reader can act on. The
            reasoning lives in internal/git/github.go. */}
        {!info.github && (
          <p data-testid="git-not-github" className="mt-1 text-vp-xs text-ink-2">
            {t('git.notGitHub')}
          </p>
        )}
        {info.github && !info.tokenSet && (
          <p data-testid="git-no-token" className="mt-1 text-vp-xs text-ink-2">
            {t('git.noToken')}
          </p>
        )}
        {hubError && (
          <p data-testid="git-hub-error" className="mt-1 text-vp-xs text-ink-2">
            {safeText(hubError)}
          </p>
        )}
        {hub && (
          <div className="mt-1.5">
            {hub.prs.length === 0 ? (
              <p data-testid="git-no-prs" className="text-vp-xs text-ink-2">
                {t('git.noPRs')}
              </p>
            ) : (
              hub.prs.map((pr) => <PRRow key={pr.number} pr={pr} />)
            )}
            {hub.total > hub.prs.length && (
              <p className="text-vp-xs text-ink-2">
                {t('git.prsTruncated', { shown: hub.prs.length, total: hub.total })}
              </p>
            )}
            {/* A list with no poller behind it is as old as the last press, and
                nothing else on screen would say so. */}
            <p data-testid="git-checked-at" className="mt-1 text-vp-xs text-ink-2">
              <Ago when={hub.checkedAt} now={now} />
            </p>
          </div>
        )}
      </div>
    </div>
  )
}

function kindLabel(kind: string): Key {
  switch (kind) {
    case 'staged':
      return 'git.staged'
    case 'unstaged':
      return 'git.unstaged'
    case 'conflict':
      return 'git.conflictWord'
    default:
      return 'git.untracked'
  }
}
