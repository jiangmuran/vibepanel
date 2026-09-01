import { useEffect, useState } from 'react'

import type { ShareDashboard, ShareWidget } from '../../protocol/wire'
import { t } from '../../i18n'
import { StateDot } from '../StateDot'
import { safeText } from '../text'
import { Tile } from './Tile'
import { compact, duration, exact } from './format'
import { metricLabel } from './labels'
import { filterRows, orderRows } from './rows'

/**
 * The widgets that are a number, and the ones that are barely a widget at all.
 *
 * These are what a wall board is mostly made of. The constraint that shapes all
 * of them: a figure read at three metres has about a second of attention, so
 * whatever it is has to be the largest thing on the tile and whatever qualifies
 * it has to be underneath rather than beside.
 */

/** One figure, its name, and an optional line of context. */
function Headline({
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
  return (
    <div className="flex min-w-0 flex-col justify-center" data-testid={testid}>
      <span
        // Clipped on one axis, not two. `truncate` is overflow:hidden on both,
        // and this line-height (1.1) is shorter than the font's own content
        // box, so a figure had its ascender and descender shaved off -- worst
        // on a television, where the type is largest and the shave is widest.
        className="tabular overflow-x-hidden text-ellipsis whitespace-nowrap text-vp-3xl font-semibold"
        style={{ color: tone ?? 'var(--vp-ink)' }}
        data-testid="headline-value"
      >
        {value}
      </span>
      <span className="truncate text-vp-xl text-ink-2">{label}</span>
      {detail && <span className="tabular truncate text-vp-xl text-ink-3">{detail}</span>}
    </div>
  )
}

/** What one metric reads. Everything a single figure can be pointed at. */
function readMetric(
  metric: string,
  data: ShareDashboard,
  now: number,
): { value: string; detail?: string; tone?: string } {
  const c = data.counts
  const m = data.machine
  const spend = data.spend
  const todos = data.todos
  const repo = data.repo
  const flow = data.flow
  const unknown = { value: '—', detail: t('dash.unknown') }
  // "Not counted yet" rather than "unknown", and the distinction is the whole
  // point of ShareRepo.readable: a repository figure of 0 before the first
  // background read has landed is a lie about a morning's work.
  const notRead = { value: '—', detail: t('dash.notRead') }

  switch (metric) {
    case 'waiting':
      return { value: String(c.waiting), tone: 'var(--vp-state-waiting)' }
    case 'working':
      return { value: String(c.working), tone: 'var(--vp-state-working)' }
    case 'done':
      return { value: String(c.done), tone: 'var(--vp-state-done)' }
    case 'sessions':
      return { value: String(c.sessions), detail: `${c.projects} ${t('dash.projects')}` }
    case 'projects':
      return { value: String(c.projects) }
    case 'crashed':
      return { value: String(c.crashed), tone: c.crashed > 0 ? 'var(--vp-state-crashed)' : undefined }
    case 'exited':
      return { value: String(c.exited) }
    case 'doneToday':
      return { value: String(c.doneToday), tone: 'var(--vp-state-done)' }
    case 'longestWait':
      return c.longestWaitAt > 0
        ? { value: duration(Math.max(0, now - c.longestWaitAt)), tone: 'var(--vp-state-waiting)' }
        : { value: '—', detail: t('dash.nothingWaiting') }
    case 'todosOpen':
      return todos ? { value: String(todos.open) } : unknown
    case 'todosDone':
      return todos ? { value: String(todos.done) } : unknown
    // What was produced. These replaced "sessions finished today" and "todos
    // ticked today" as the headline figures, because both of those are
    // self-reported and on a real wall both read 0. See internal/store/board.go.
    case 'commitsToday':
      return repo?.readable
        ? { value: String(repo.today.commits), tone: 'var(--vp-state-done)' }
        : notRead
    case 'commitsWindow':
      return repo?.readable
        ? {
            value: String(repo.window.commits),
            detail: t('dash.lastDays', { n: repo.windowDays }),
          }
        : notRead
    case 'linesAdded':
      return repo?.readable
        ? {
            value: `+${compact(repo.today.added)}`,
            detail: exact(repo.today.added),
            tone: 'var(--vp-state-done)',
          }
        : notRead
    case 'linesRemoved':
      return repo?.readable
        ? {
            value: `−${compact(repo.today.removed)}`,
            detail: exact(repo.today.removed),
            tone: 'var(--vp-state-crashed)',
          }
        : notRead
    case 'linesChanged':
      // Added and removed shown as two halves under one figure, never a net
      // number: a net figure hides a refactor completely, and the two are
      // different days.
      return repo?.readable
        ? {
            value: compact(repo.today.added + repo.today.removed),
            detail: `+${exact(repo.today.added)} −${exact(repo.today.removed)}`,
          }
        : notRead
    case 'filesToday':
      return repo?.readable ? { value: String(repo.today.files) } : notRead
    case 'openPRs':
      return repo?.prs?.readable
        ? {
            value: String(repo.prs.open),
            detail: repo.prs.draft > 0 ? t('dash.prsDraft', { n: repo.prs.draft }) : undefined,
          }
        : notRead
    case 'prsMergedToday':
      return repo?.prs?.readable
        ? {
            value: repo.prs.mergedPartial
              ? `${repo.prs.mergedToday}+`
              : String(repo.prs.mergedToday),
            tone: 'var(--vp-state-done)',
          }
        : notRead
    case 'checksRed':
      return repo?.prs?.readable
        ? {
            value: String(repo.prs.red),
            tone: repo.prs.red > 0 ? 'var(--vp-state-crashed)' : undefined,
          }
        : notRead
    // How the day has gone, out of the session-event log.
    case 'startedToday':
      return flow ? { value: String(flow.today.started) } : unknown
    case 'waitsToday':
      return flow
        ? { value: String(flow.today.waited), tone: 'var(--vp-state-waiting)' }
        : unknown
    case 'avgWaitToday':
      // Zero waits is not a zero-second wait, which is why the wire carries the
      // sum and the count rather than the average.
      return flow && flow.today.waitEnded > 0
        ? {
            value: duration(flow.today.waitSeconds / flow.today.waitEnded),
            detail: t('dash.waitsCounted', { n: flow.today.waitEnded }),
          }
        : { value: '—', detail: t('dash.noWaitsToday') }
    case 'todoPercent': {
      if (!todos) return unknown
      const total = todos.open + todos.done
      if (total === 0) return unknown
      return {
        value: `${Math.round((todos.done / total) * 100)}%`,
        detail: `${todos.done} / ${total}`,
      }
    }
    case 'cpu':
      return m.cpuReadable && m.cpuPercent !== null
        ? { value: `${Math.round(m.cpuPercent)}%`, detail: t('monitor.cores', { n: m.cores }) }
        : unknown
    case 'memory':
      return m.memTotal > 0
        ? { value: `${Math.round(((m.memTotal - m.memAvailable) / m.memTotal) * 100)}%` }
        : unknown
    case 'disk':
      return m.diskTotal > 0
        ? { value: `${Math.round(((m.diskTotal - m.diskFree) / m.diskTotal) * 100)}%` }
        : unknown
    case 'load':
      return { value: m.load1.toFixed(2), detail: `${m.load5.toFixed(2)} · ${m.load15.toFixed(2)}` }
    case 'uptime':
      return { value: duration(m.uptime) }
    case 'tokensToday':
      return spend?.readable
        ? { value: compact(spend.today.total), detail: exact(spend.today.total) }
        : { value: '—', detail: t('dash.noSpendYet') }
    case 'tokensMonth':
      return spend?.readable
        ? { value: compact(spend.month.total), detail: exact(spend.month.total) }
        : { value: '—', detail: t('dash.noSpendYet') }
    case 'tokensWindow':
      return spend?.readable
        ? {
            value: compact(spend.window.total),
            detail: t('dash.lastDays', { n: spend.windowDays }),
          }
        : { value: '—', detail: t('dash.noSpendYet') }
    case 'requestsToday':
      return spend?.readable
        ? { value: exact(spend.today.requests) }
        : { value: '—', detail: t('dash.noSpendYet') }
    case 'tokensPerHour': {
      if (!spend?.readable) return { value: '—', detail: t('dash.noSpendYet') }
      const hours = Math.max(spend.hoursToday, 1 / 60)
      return { value: compact(spend.today.total / hours), detail: t('dash.perHourToday') }
    }
    default:
      // A metric this build has never heard of. Blank rather than an
      // identifier: a wall reading "tokensPerFortnight" has put an internal
      // name on a screen behind somebody's desk.
      return unknown
  }
}

export function BigNumber({ w, data, now }: { w: ShareWidget; data: ShareDashboard; now: number }) {
  const metric = w.metric ?? 'waiting'
  const read = readMetric(metric, data, now)
  return (
    <Tile kind={w.kind} span={w.span} height={w.height} testid="widget-bignumber" plain>
      <Headline
        testid="dash-bignumber"
        value={read.value}
        label={metricLabel(metric, metric)}
        detail={read.detail}
        tone={read.tone}
      />
    </Tile>
  )
}

/**
 * "Does anything need me", answered from across a room.
 *
 * The number is the answer and the row of shapes under it is the evidence: one
 * glyph per session that is waiting, so a glance says both "three" and "these
 * three have been waiting a while" without reading anything.
 */
export function Attention({ w, data, now }: { w: ShareWidget; data: ShareDashboard; now: number }) {
  const waiting = orderRows(filterRows(data.sessions, 'waiting'), 'waited')
  const oldest = data.counts.longestWaitAt > 0 ? now - data.counts.longestWaitAt : 0
  const quiet = data.counts.waiting === 0
  return (
    <Tile kind={w.kind} span={w.span} height={w.height} testid="widget-attention" plain>
      <div className="flex flex-wrap items-baseline gap-x-8 gap-y-2">
        <span
          className="tabular text-vp-3xl font-semibold"
          style={{ color: quiet ? 'var(--vp-state-done)' : 'var(--vp-state-waiting)' }}
          data-testid="attention-count"
        >
          {quiet ? '✓' : data.counts.waiting}
        </span>
        <span className="text-vp-2xl text-ink">
          {quiet ? t('dash.allClear') : t('dash.waiting')}
        </span>
        {!quiet && oldest > 0 && (
          <span className="tabular text-vp-xl text-ink-2">
            {t('dash.longestWait', { d: duration(oldest) })}
          </span>
        )}
      </div>
      {waiting.length > 0 && (
        <div className="mt-4 flex flex-wrap gap-2">
          {waiting.slice(0, 24).map((row) => (
            <StateDot key={row.id} state="waiting" size={26} />
          ))}
        </div>
      )}
    </Tile>
  )
}

/** The state tallies, each carrying its own shape. */
export function States({ w, data }: { w: ShareWidget; data: ShareDashboard }) {
  const c = data.counts
  return (
    <Tile kind={w.kind} span={w.span} height={w.height} testid="widget-states" plain>
      <div className="flex flex-wrap items-start gap-x-12 gap-y-6" data-testid="dash-counts">
        <Tally
          value={c.waiting}
          label={t('dash.waiting')}
          tone="var(--vp-state-waiting)"
          glyph={<StateDot state="waiting" size={30} />}
        />
        <Tally
          value={c.working}
          label={t('dash.working')}
          tone="var(--vp-state-working)"
          glyph={<StateDot state="working" size={30} />}
        />
        <Tally
          value={c.done}
          label={t('dash.done')}
          tone="var(--vp-state-done)"
          glyph={<StateDot state="done" size={30} />}
        />
        {c.crashed > 0 && (
          <Tally
            value={c.crashed}
            label={t('dash.crashed')}
            tone="var(--vp-state-crashed)"
            glyph={<StateDot state="done" size={30} exited exitStatus={1} />}
          />
        )}
        <div className="flex min-w-0 flex-col items-start gap-1">
          <span className="tabular text-vp-3xl font-semibold text-ink-2">{c.sessions}</span>
          <span className="truncate text-vp-xl text-ink-2">
            {t('dash.sessions')} · {c.projects} {t('dash.projects')}
          </span>
        </div>
      </div>
    </Tile>
  )
}

function Tally({
  value,
  label,
  glyph,
  tone,
}: {
  value: number
  label: string
  glyph: React.ReactNode
  tone: string
}) {
  return (
    <div className="flex min-w-0 flex-col items-start gap-1" data-testid="dash-tally">
      <div className="flex items-center gap-3">
        {glyph}
        <span className="tabular text-vp-3xl font-semibold" style={{ color: tone }}>
          {value}
        </span>
      </div>
      <span className="truncate text-vp-xl text-ink-2">{label}</span>
    </div>
  )
}

/** The wall clock, for a screen somebody walks past. */
export function Clock({ w }: { w: ShareWidget }) {
  const [now, setNow] = useState(() => new Date())
  useEffect(() => {
    const timer = window.setInterval(() => setNow(new Date()), 1000)
    return () => clearInterval(timer)
  }, [])
  return (
    <Tile kind={w.kind} span={w.span} height={w.height} testid="widget-clock" plain>
      {/* Tight leading and an explicit gap, because tiles clip now.
        *
        * A display figure at 3xl carries about a tenth of its size in leading
        * above and below, which is space a one-row tile does not have to give:
        * the block fits, but with nothing left over. Setting the leading and
        * putting the gap back by hand is the same look with the slack under
        * this file's control rather than the font's. `truncate` is for the
        * date, which is a locale string and can be much longer than the one
        * this was designed against. */}
      <div
        className="tabular text-vp-3xl font-semibold leading-none text-ink"
        data-testid="dash-clock"
      >
        {now.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })}
      </div>
      <div className="mt-1 truncate text-vp-xl leading-none text-ink-2">
        {now.toLocaleDateString()}
      </div>
    </Tile>
  )
}

/**
 * Words the owner typed.
 *
 * The only free text on a board, so it is the only thing here that goes through
 * safeText — a caption is stored, and a stored string with a bidi override in
 * it is one that reorders the text around it on somebody's wall.
 */
export function Caption({ w }: { w: ShareWidget }) {
  return (
    <Tile kind={w.kind} span={w.span} height={w.height} testid="widget-caption" plain>
      <p className="text-vp-2xl font-medium text-ink" data-testid="dash-caption">
        {safeText(w.text ?? '')}
      </p>
    </Tile>
  )
}
