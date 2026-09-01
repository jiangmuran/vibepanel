import type {
  ShareDashboard,
  ShareFlowBucket,
  ShareRepoDay,
  ShareWidget,
} from '../../protocol/wire'
import { t } from '../../i18n'
import { StateDot } from '../StateDot'
import { safeText } from '../text'
import { Bar, Empty, Tile } from './Tile'
import { bucketLabel, compact, duration, exact } from './format'
import { byLabel } from './labels'
import { DENSE, rows as rowsAt, showsDetail, useDensity } from './density'

/**
 * The widgets that say what happened rather than what is true right now.
 *
 * Two sources, both of which are new and both of which the board was empty
 * without:
 *
 *   the session-event log  what started, what went quiet waiting, what
 *                          finished, and how long things sat. The panel used to
 *                          keep state and no history, so every one of these was
 *                          a single current number.
 *   the working trees      commits, changed lines, files touched, pull
 *                          requests. The half of "what did it cost / what came
 *                          out of it" the panel could not answer at all.
 *
 * The second one replaced the first version of "what came out", which was
 * sessions finished and todos ticked. Both of those are self-reported — a todo
 * is ticked because somebody remembered to — and on the wall that prompted this
 * they read 0 and 0 next to a four-figure request count. Commits and changed
 * lines are things that exist now and did not this morning.
 *
 * Changed lines are labelled as change and never as output, and they are always
 * two numbers rather than a net one. A net figure hides a refactor completely,
 * and "lines of code produced" is a sentence nobody should be able to read off
 * a screen this panel drew.
 */

/** A figure and its name, at the size a wall reads. Local to this file so the
 *  production tiles line up with each other rather than with the spend ones. */
function Figure({
  value,
  label,
  detail,
  tone,
  testid,
}: {
  value: string
  label: string
  detail?: string
  tone?: string
  testid: string
}) {
  const density = useDensity()
  return (
    <div className="flex min-w-0 flex-col" data-testid={testid}>
      <span
        className="tabular truncate text-vp-2xl font-semibold"
        style={{ color: tone ?? 'var(--vp-ink)' }}
      >
        {value}
      </span>
      <span className="truncate text-vp-xl text-ink-2">{label}</span>
      {detail && showsDetail(density) && (
        <span className="tabular truncate text-vp-xl text-ink-3">{detail}</span>
      )}
    </div>
  )
}

/** What a repository widget shows before the first background read has landed.
 *
 *  "Not counted yet" and "nothing happened today" are different facts about a
 *  repository and the first one is said out loud — the same distinction
 *  shareSpend.readable already makes about the transcripts. */
function NotRead({ w, label }: { w: ShareWidget; label: string }) {
  return (
    <Tile kind={w.kind} span={w.span} height={w.height} testid={`widget-${w.kind}`} label={label}>
      <Empty text={t('dash.notRead')} />
    </Tile>
  )
}

/**
 * How old a background-refreshed figure is -- and only once it is old.
 *
 * The same rule as the panel's own spend footer, and it was broken the same
 * way: a wall showing 「read 3s ago」 under every figure is the board narrating
 * its own housekeeping to somebody standing three metres away who came for the
 * number. Freshness is worth a line when the figure is *behind*; the rest of
 * the time it is noise on every render, forever.
 *
 * The threshold is the refresh interval, so "old" means "a refresh has been
 * missed" rather than a duration somebody picked.
 */
const FRESH_SECONDS = 90

function agedLine(seconds: number): string | undefined {
  if (seconds < 0 || seconds <= FRESH_SECONDS) return undefined
  return t('dash.readAgo', { d: duration(seconds) })
}

/**
 * What was produced today: commits, changed lines, files touched.
 *
 * The hero of every production board. It shows how many of the projects in
 * scope are actually checkouts, because a panel whose projects are not
 * repositories shows zeroes and that has to look deliberate rather than broken.
 */
export function Output({ w, data }: { w: ShareWidget; data: ShareDashboard }) {
  const repo = data.repo
  const density = useDensity()
  const label = t('board.kind.output')
  if (!repo || !repo.readable) return <NotRead w={w} label={label} />
  const today = repo.today
  return (
    <Tile kind={w.kind} span={w.span} height={w.height} testid="widget-output" label={label}>
      <div className="flex flex-wrap gap-x-10 gap-y-4">
        <Figure
          testid="output-commits"
          value={String(today.commits)}
          label={t('dash.commitsToday')}
          tone="var(--vp-state-done)"
          detail={agedLine(repo.ageSeconds)}
        />
        {/* Two numbers, never a net one. +1200/-800 is a different day from
            +400/-0, and the net figure is the same in both. */}
        <Figure
          testid="output-added"
          value={`+${compact(today.added)}`}
          label={t('dash.linesAdded')}
          tone="var(--vp-state-done)"
          detail={exact(today.added)}
        />
        <Figure
          testid="output-removed"
          value={`−${compact(today.removed)}`}
          label={t('dash.linesRemoved')}
          tone="var(--vp-state-crashed)"
          detail={exact(today.removed)}
        />
        {density >= DENSE && (
          <Figure
            testid="output-files"
            value={String(today.files)}
            label={t('dash.filesToday')}
          />
        )}
      </div>
      {repo.repos < repo.projects && (
        <p className="mt-3 text-vp-xl text-ink-3" data-testid="output-notrepos">
          {t('dash.someNotRepos', { n: repo.projects - repo.repos })}
        </p>
      )}
    </Tile>
  )
}

/**
 * Commits, changed lines or files touched, per day.
 *
 * Lines are drawn on both sides of a baseline rather than stacked, because
 * added and removed are two facts and a stack invites reading the total.
 */
export function CodeChurn({ w, data }: { w: ShareWidget; data: ShareDashboard }) {
  const repo = data.repo
  const by = w.by ?? 'lines'
  const label = `${t('board.kind.codechurn')} · ${byLabel(by, by)}`
  if (!repo || !repo.readable) return <NotRead w={w} label={label} />
  const days = repo.days
  const top = days.reduce(
    (n, d) => Math.max(n, by === 'commits' ? d.commits : by === 'files' ? d.files : d.added, by === 'lines' ? d.removed : 0),
    0,
  )
  return (
    <Tile kind={w.kind} span={w.span} height={w.height} testid="widget-codechurn" label={label}>
      {days.length === 0 ? (
        <Empty text={t('dash.emptyWidget')} />
      ) : (
        <div className="flex items-stretch gap-1" style={{ height: '9rem' }} data-testid="churn-bars">
          {days.map((d) => (
            <ChurnColumn key={d.label} day={d} by={by} top={top} />
          ))}
        </div>
      )}
      <div className="tabular mt-2 flex justify-between text-vp-xl text-ink-3">
        <span>{days.length > 0 ? bucketLabel(days[0].label) : ''}</span>
        <span className="text-ink-2">{compact(top)}</span>
        <span>{days.length > 0 ? bucketLabel(days[days.length - 1].label) : ''}</span>
      </div>
    </Tile>
  )
}

function ChurnColumn({ day, by, top }: { day: ShareRepoDay; by: string; top: number }) {
  const scale = (v: number) => (top > 0 ? Math.max((v / top) * 100, v > 0 ? 3 : 0) : 0)
  if (by !== 'lines') {
    const v = by === 'commits' ? day.commits : day.files
    return (
      <div className="flex min-w-0 flex-1 flex-col justify-end">
        <div
          className="rounded-md"
          style={{ height: `${scale(v)}%`, background: 'var(--vp-accent)' }}
          role="img"
          aria-label={`${day.label}: ${exact(v)}`}
          title={`${day.label}: ${exact(v)}`}
        />
      </div>
    )
  }
  return (
    <div className="flex min-w-0 flex-1 flex-col">
      <div className="flex flex-1 flex-col justify-end">
        <div
          className="rounded-md"
          style={{ height: `${scale(day.added)}%`, background: 'var(--vp-state-done)' }}
          role="img"
          aria-label={`${day.label}: +${exact(day.added)}`}
          title={`${day.label}: +${exact(day.added)}`}
        />
      </div>
      <div className="h-px" style={{ background: 'var(--vp-hairline)' }} />
      <div className="flex flex-1 flex-col justify-start">
        <div
          className="rounded-md"
          style={{ height: `${scale(day.removed)}%`, background: 'var(--vp-state-crashed)' }}
          role="img"
          aria-label={`${day.label}: −${exact(day.removed)}`}
          title={`${day.label}: −${exact(day.removed)}`}
        />
      </div>
    </div>
  )
}

/**
 * What it cost and what came out of it, on one time axis.
 *
 * The thing this dashboard was asked for and could not do until the panel read
 * repositories. Two series, deliberately not one ratio: tokens per line is a
 * nonsense number that would be quoted at somebody in a meeting, and the reader
 * comparing two shapes is doing something the arithmetic cannot do for them.
 *
 * Two scales, drawn as two bands rather than two lines in one box. A shared
 * axis would put a four-figure token count and a two-figure commit count on one
 * scale and flatten the second into the baseline; two lines with two hidden
 * scales in one box is a chart whose crossings mean nothing and are read as
 * though they do.
 */
export function SpentMade({ w, data }: { w: ShareWidget; data: ShareDashboard }) {
  const repo = data.repo
  const spend = data.spend
  const label = t('board.kind.spentmade')
  if (!repo?.readable || !spend?.readable) return <NotRead w={w} label={label} />

  // Aligned by date rather than by position: the two series are built from
  // different windows on different tables, and lining them up by index would
  // silently offset one against the other by however many days they differ.
  const commitsOn = new Map(repo.days.map((d) => [d.label, d]))
  const paired = spend.days
    .filter((d) => commitsOn.has(d.label))
    .map((d) => ({ label: d.label, tokens: d.total, day: commitsOn.get(d.label)! }))
  const topTokens = paired.reduce((n, p) => Math.max(n, p.tokens), 0)
  const topCommits = paired.reduce((n, p) => Math.max(n, p.day.commits), 0)

  return (
    <Tile kind={w.kind} span={w.span} height={w.height} testid="widget-spentmade" label={label}>
      {paired.length === 0 ? (
        <Empty text={t('dash.emptyWidget')} />
      ) : (
        <div data-testid="spentmade-bars">
          <div className="mb-1 flex items-baseline justify-between text-vp-xl text-ink-3">
            <span>{t('dash.spent')}</span>
            <span className="tabular text-ink-2">{compact(topTokens)}</span>
          </div>
          <div className="flex items-end gap-1" style={{ height: '5rem' }}>
            {paired.map((p) => (
              <div key={`s-${p.label}`} className="flex min-w-0 flex-1 flex-col justify-end">
                <div
                  className="rounded-md"
                  style={{
                    height: `${topTokens > 0 ? Math.max((p.tokens / topTokens) * 100, p.tokens > 0 ? 3 : 0) : 0}%`,
                    background: 'var(--vp-accent)',
                  }}
                  role="img"
                  aria-label={`${p.label}: ${exact(p.tokens)}`}
                  title={`${p.label}: ${exact(p.tokens)}`}
                />
              </div>
            ))}
          </div>
          <div className="mb-1 mt-3 flex items-baseline justify-between text-vp-xl text-ink-3">
            <span>{t('dash.made')}</span>
            <span className="tabular text-ink-2">{exact(topCommits)}</span>
          </div>
          <div className="flex items-end gap-1" style={{ height: '5rem' }}>
            {paired.map((p) => (
              <div key={`m-${p.label}`} className="flex min-w-0 flex-1 flex-col justify-end">
                <div
                  className="rounded-md"
                  style={{
                    height: `${topCommits > 0 ? Math.max((p.day.commits / topCommits) * 100, p.day.commits > 0 ? 3 : 0) : 0}%`,
                    background: 'var(--vp-state-done)',
                  }}
                  role="img"
                  aria-label={`${p.label}: ${exact(p.day.commits)}`}
                  title={`${p.label}: ${exact(p.day.commits)}`}
                />
              </div>
            ))}
          </div>
          <div className="tabular mt-2 flex justify-between text-vp-xl text-ink-3">
            <span>{bucketLabel(paired[0].label)}</span>
            <span>{bucketLabel(paired[paired.length - 1].label)}</span>
          </div>
        </div>
      )}
    </Tile>
  )
}

/** Where the commits went, by project. */
export function RepoProjects({ w, data }: { w: ShareWidget; data: ShareDashboard }) {
  const repo = data.repo
  const by = w.by ?? 'lines'
  const density = useDensity()
  const label = `${t('board.kind.repoprojects')} · ${byLabel(by, by)}`
  if (!repo?.readable) return <NotRead w={w} label={label} />
  const value = (p: (typeof repo.byProject)[number]) =>
    by === 'commits' ? p.window.commits : by === 'files' ? p.window.files : p.window.added + p.window.removed
  const ranked = [...repo.byProject].sort((a, b) => value(b) - value(a))
  const top = ranked.reduce((n, p) => Math.max(n, value(p)), 0)
  return (
    <Tile kind={w.kind} span={w.span} height={w.height} testid="widget-repoprojects" label={label}>
      {ranked.length === 0 ? (
        <Empty text={t('dash.emptyWidget')} />
      ) : (
        ranked.slice(0, rowsAt(density, 6)).map((p, i) => (
          <Bar
            key={p.id}
            testid="repoproject-row"
            label={p.repo ? p.name || t('dash.group', { n: i + 1 }) : t('dash.notARepo')}
            value={
              by === 'lines'
                ? `+${compact(p.window.added)} −${compact(p.window.removed)}`
                : exact(value(p))
            }
            fraction={top > 0 ? value(p) / top : 0}
            tone={p.repo ? 'var(--vp-accent)' : 'var(--vp-surface-2)'}
          />
        ))
      )}
    </Tile>
  )
}

/**
 * Open pull requests, and whether the checks are green.
 *
 * Counts only. No title, no number, no author, no branch, no URL — a count says
 * how much is in flight, a title says what somebody is building for whom. Red
 * line 4 applies: green and red carry a word each as well as a hue.
 */
export function PRs({ w, data }: { w: ShareWidget; data: ShareDashboard }) {
  const prs = data.repo?.prs
  const label = t('board.kind.prs')
  if (!prs || !prs.readable) return <NotRead w={w} label={label} />
  return (
    <Tile kind={w.kind} span={w.span} height={w.height} testid="widget-prs" label={label}>
      <div className="flex flex-wrap gap-x-10 gap-y-4">
        <Figure
          testid="prs-open"
          value={String(prs.open)}
          label={t('dash.prsOpen')}
          detail={prs.draft > 0 ? t('dash.prsDraft', { n: prs.draft }) : agedLine(prs.ageSeconds)}
        />
        <Figure
          testid="prs-green"
          value={String(prs.green)}
          label={t('dash.checksGreen')}
          tone="var(--vp-state-done)"
        />
        <Figure
          testid="prs-red"
          value={String(prs.red)}
          label={t('dash.checksRed')}
          tone={prs.red > 0 ? 'var(--vp-state-crashed)' : undefined}
        />
        <Figure
          testid="prs-merged"
          value={prs.mergedPartial ? `${prs.mergedToday}+` : String(prs.mergedToday)}
          label={t('dash.prsMerged')}
        />
      </div>
    </Tile>
  )
}

/**
 * How the day went: what started, what went quiet waiting, what finished.
 *
 * Out of the session-event log, and it is a *flow*: each column is transitions
 * that happened in that hour, not how many sessions were in each state. A stock
 * cannot be honestly reconstructed from a flow log — see
 * internal/store/events.go — and a chart that pretended to would be wrong in a
 * way nothing could detect.
 */
export function Flow({ w, data }: { w: ShareWidget; data: ShareDashboard }) {
  const flow = data.flow
  const by = w.by ?? 'hour'
  const label = `${t('board.kind.flow')} · ${byLabel(by, by)}`
  if (!flow) return <NotRead w={w} label={label} />
  const top = flow.buckets.reduce((n, b) => Math.max(n, b.started + b.waited + b.finished), 0)
  return (
    <Tile kind={w.kind} span={w.span} height={w.height} testid="widget-flow" label={label}>
      {/* `top === 0` is empty too, and it is the common one.
        *
        * Buckets exist for every hour of the window whether or not anything
        * happened in them, so a quiet day is a full array of zeroes: every
        * column drew at 0% and the strip was 144px of blank inside a labelled
        * tile, which reads as a broken chart rather than as a quiet day. */}
      {flow.buckets.length === 0 || top === 0 ? (
        <Empty text={t('dash.emptyWidget')} />
      ) : (
        <div
          className="flex items-end gap-1"
          // Scaled with the board's own type unit rather than a fixed rem: on
          // a television the strip was the same 144px as on a laptop, beside
          // numbers three times the size.
          style={{ height: 'calc(9 * var(--vp-wall, 1rem))' }}
          data-testid="flow-bars"
        >
          {flow.buckets.map((b) => (
            <FlowColumn key={b.at} bucket={b} top={top} />
          ))}
        </div>
      )}
      {/* Red line 4: the legend carries the same shapes the sidebar uses, so
          the three bands are told apart by glyph as well as by hue. */}
      <div className="mt-2 flex flex-wrap items-center gap-x-6 gap-y-1 text-vp-xl text-ink-3">
        <span className="flex items-center gap-2">
          <StateDot state="working" size={18} /> {flow.today.started} {t('dash.flowStarted')}
        </span>
        <span className="flex items-center gap-2">
          <StateDot state="waiting" size={18} /> {flow.today.waited} {t('dash.flowWaited')}
        </span>
        <span className="flex items-center gap-2">
          <StateDot state="done" size={18} /> {flow.today.finished} {t('dash.flowFinished')}
        </span>
      </div>
    </Tile>
  )
}

function FlowColumn({ bucket, top }: { bucket: ShareFlowBucket; top: number }) {
  const total = bucket.started + bucket.waited + bucket.finished
  const h = (v: number) => (top > 0 ? (v / top) * 100 : 0)
  const when = new Date(bucket.at * 1000).toLocaleString()
  return (
    <div
      className="flex min-w-0 flex-1 flex-col justify-end"
      role="img"
      aria-label={`${when}: ${total}`}
      title={`${when}: ${total}`}
    >
      <div style={{ height: `${h(bucket.finished)}%`, background: 'var(--vp-state-done)' }} />
      <div style={{ height: `${h(bucket.waited)}%`, background: 'var(--vp-state-waiting)' }} />
      <div style={{ height: `${h(bucket.started)}%`, background: 'var(--vp-state-working)' }} />
    </div>
  )
}

/**
 * How long things sat waiting before somebody got to them.
 *
 * The queue question, answered as a duration rather than a depth. An average
 * per bucket, from two numbers on the wire rather than one, so a bucket where
 * nothing finished waiting is empty instead of reading as a zero-second wait.
 */
export function Waits({ w, data }: { w: ShareWidget; data: ShareDashboard }) {
  const flow = data.flow
  const by = w.by ?? 'hour'
  const label = `${t('board.kind.waits')} · ${byLabel(by, by)}`
  if (!flow) return <NotRead w={w} label={label} />
  const avg = (b: { waitSeconds: number; waitEnded: number }) =>
    b.waitEnded > 0 ? b.waitSeconds / b.waitEnded : 0
  const top = flow.buckets.reduce((n, b) => Math.max(n, avg(b)), 0)
  const overall = avg(flow.today)
  return (
    <Tile kind={w.kind} span={w.span} height={w.height} testid="widget-waits" label={label}>
      {flow.buckets.length === 0 ? (
        <Empty text={t('dash.emptyWidget')} />
      ) : (
        <div className="flex items-end gap-1" style={{ height: '7rem' }} data-testid="wait-bars">
          {flow.buckets.map((b) => {
            const v = avg(b)
            const when = new Date(b.at * 1000).toLocaleString()
            return (
              <div key={b.at} className="flex min-w-0 flex-1 flex-col justify-end">
                <div
                  className="rounded-md"
                  style={{
                    height: `${top > 0 ? Math.max((v / top) * 100, v > 0 ? 3 : 0) : 0}%`,
                    background: 'var(--vp-state-waiting)',
                  }}
                  role="img"
                  aria-label={`${when}: ${duration(v)}`}
                  title={`${when}: ${duration(v)}`}
                />
              </div>
            )
          })}
        </div>
      )}
      <p className="tabular mt-2 text-vp-xl text-ink-2">
        {flow.today.waitEnded > 0
          ? t('dash.typicalWait', { d: duration(overall), n: flow.today.waitEnded })
          : t('dash.noWaitsToday')}
      </p>
    </Tile>
  )
}

/**
 * What just happened, newest first.
 *
 * The honest way to fill a television. A screen where nothing ever changes
 * cannot be told from a screenshot somebody left up, and a list that gains a
 * line when an agent finishes is the cheapest proof it is live — cheaper than a
 * spinner, and true.
 *
 * It carries exactly what a session row already carries: a per-link pseudonym,
 * a state, a time. No new fact reaches the wire because a board asked for a
 * feed; these are the same facts in the order they happened.
 */
export function Feed({ w, data, now }: { w: ShareWidget; data: ShareDashboard; now: number }) {
  const feed = data.feed
  const density = useDensity()
  const label = t('board.kind.feed')
  if (!feed) return <NotRead w={w} label={label} />
  const shown = feed.entries.slice(0, rowsAt(density, 8))
  return (
    <Tile kind={w.kind} span={w.span} height={w.height} testid="widget-feed" label={label}>
      {shown.length === 0 ? (
        <Empty text={t('dash.feedQuiet')} />
      ) : (
        <ul className="flex min-h-0 flex-1 flex-col gap-2 overflow-y-auto" data-testid="feed-rows">
          {shown.map((e, i) => (
            <li
              key={`${e.at}-${e.sessionId}-${i}`}
              className="flex items-center gap-3"
              data-testid="feed-row"
            >
              <StateDot state={e.to} size={22} />
              <span className="min-w-0 flex-1 truncate text-vp-xl text-ink">
                {e.name ? safeText(e.name) : t('dash.row', { n: i + 1 })}
              </span>
              <span className="shrink-0 text-vp-xl text-ink-2">{feedVerb(e.to)}</span>
              {showsDetail(density) && (
                <span className="tabular shrink-0 text-vp-xl text-ink-3">
                  {duration(Math.max(0, now - e.at))}
                </span>
              )}
            </li>
          ))}
        </ul>
      )}
    </Tile>
  )
}

/** What a transition is called on screen. A `switch` with a default, like the
 *  widget renderer's: the state came off the wire and a build that has never
 *  heard of it must say nothing rather than print the identifier. */
function feedVerb(to: string): string {
  switch (to) {
    case 'working':
      return t('dash.flowStarted')
    case 'waiting':
      return t('dash.flowWaited')
    case 'done':
      return t('dash.flowFinished')
    default:
      return ''
  }
}
