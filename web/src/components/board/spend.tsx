import type {
  ShareDashboard,
  ShareSpend,
  ShareSpendBucket,
  ShareSpendGroup,
  ShareSpendTotals,
  ShareWidget,
} from '../../protocol/wire'
import { t } from '../../i18n'
import { rows as rowsAt, useDensity } from './density'
import { safeText } from '../text'
import { Bar, Empty, Tile } from './Tile'
import { bucketLabel, compact, exact } from './format'
import { byLabel } from './labels'

/**
 * What the agents recorded spending, and how fast.
 *
 * Tokens, never money: prices differ per model, per tier and over time, and a
 * currency figure derived from a stale table is a confident wrong number on a
 * wall. Every widget here has to hold one line: `readable` false means nothing
 * has been counted yet, which is not the same claim as nothing being spent, and
 * a zero shown for the first is the whole failure the flag exists to prevent.
 */

/** The tile a spend widget shows before any pass has finished. */
function NotCounted({ w }: { w: ShareWidget }) {
  return (
    <Tile kind={w.kind} span={w.span} height={w.height} testid="widget-spend-unknown">
      <Empty text={t('dash.noSpendYet')} />
    </Tile>
  )
}

/** Today, this month, the window — with the split underneath. */
export function SpendTotals({ w, data }: { w: ShareWidget; data: ShareDashboard }) {
  const spend = data.spend
  if (!spend?.readable) return <NotCounted w={w} />
  return (
    <Tile kind={w.kind} span={w.span} height={w.height} testid="widget-spendtotals" label={t('board.kind.spendtotals')}>
      <div className="mb-4 flex flex-wrap gap-x-10 gap-y-3">
        <Figure label={t('dash.today')} value={spend.today.total} />
        <Figure label={t('dash.thisMonth')} value={spend.month.total} />
        <Figure label={t('dash.lastDays', { n: spend.windowDays })} value={spend.window.total} />
      </div>
      <Split totals={spend.window} />
    </Tile>
  )
}

function Figure({ label, value }: { label: string; value: number }) {
  return (
    <div className="min-w-0" data-testid="spend-figure">
      <div className="tabular text-vp-2xl font-semibold text-ink">{compact(value)}</div>
      <div className="truncate text-vp-xl text-ink-2">{label}</div>
    </div>
  )
}

/**
 * Input, output and cache as one bar in four segments.
 *
 * The proportion is the interesting part and it is invisible in four separate
 * numbers: a month that is nine-tenths cache reads and one where it is
 * nine-tenths fresh input cost the same and are not the same thing. Each
 * segment carries its own figure in the legend below, because four hues in one
 * bar is exactly the case red line 4 is about.
 */
function Split({ totals }: { totals: ShareSpendTotals }) {
  const parts = [
    { key: 'input', label: t('dash.input'), value: totals.input, tone: 'var(--vp-accent)' },
    { key: 'output', label: t('dash.output'), value: totals.output, tone: 'var(--vp-state-done)' },
    {
      key: 'cacheRead',
      label: t('dash.cacheRead'),
      value: totals.cacheRead,
      tone: 'var(--vp-state-working)',
    },
    {
      key: 'cacheWrite',
      label: t('dash.cacheWrite'),
      value: totals.cacheWrite,
      tone: 'var(--vp-state-waiting)',
    },
  ]
  const sum = parts.reduce((n, p) => n + p.value, 0)
  return (
    <div data-testid="spend-split">
      <div className="flex h-3 overflow-hidden rounded-full" style={{ background: 'var(--vp-surface-2)' }}>
        {parts.map((p) => (
          <div
            key={p.key}
            style={{ width: `${sum > 0 ? (p.value / sum) * 100 : 0}%`, background: p.tone }}
          />
        ))}
      </div>
      <div className="mt-2 flex flex-wrap gap-x-6 gap-y-1">
        {parts.map((p) => (
          <span key={p.key} className="flex items-center gap-2 text-vp-xl text-ink-2">
            {/* A shape as well as a hue: the legend is the only thing tying a
                segment to a word, and a photograph of this screen loses hue
                first. */}
            <span
              aria-hidden="true"
              className="inline-block rounded-full"
              style={{ width: '0.6em', height: '0.6em', background: p.tone }}
            />
            {p.label} <span className="tabular text-ink">{compact(p.value)}</span>
          </span>
        ))}
      </div>
    </div>
  )
}

/**
 * How fast it is being spent, rather than how much in total.
 *
 * Per hour and not per second: the transcripts are rolled up to a day, so a
 * per-second figure would be a day's total divided by 86,400 and dressed up as
 * a live rate. Today's rate is today's tokens over the hours elapsed on the
 * *server's* clock, which is why `hoursToday` comes down the wire.
 */
export function SpendRate({ w, data }: { w: ShareWidget; data: ShareDashboard }) {
  const spend = data.spend
  if (!spend?.readable) return <NotCounted w={w} />
  const hours = Math.max(spend.hoursToday, 1 / 60)
  const perHour = spend.today.total / hours
  const windowPerHour = spend.window.total / (spend.windowDays * 24)
  return (
    <Tile kind={w.kind} span={w.span} height={w.height} testid="widget-spendrate" label={t('board.kind.spendrate')}>
      <div className="tabular text-vp-3xl font-semibold text-ink" data-testid="spend-rate-value">
        {compact(perHour)}
      </div>
      <div className="text-vp-xl text-ink-2">{t('dash.perHourToday')}</div>
      <div className="tabular mt-3 text-vp-xl text-ink-2">
        {t('dash.perHourAverage', { n: compact(windowPerHour), days: spend.windowDays })}
      </div>
      <div className="tabular mt-1 text-vp-xl text-ink-3">
        {t('dash.requestsPerHour', { n: compact(spend.today.requests / hours) })}
      </div>
    </Tile>
  )
}

/**
 * A total against the one before it.
 *
 * The arrow is a shape and the sign is a word, so neither the hue nor the
 * triangle is carrying the meaning alone. "No comparison" is its own state:
 * a first day, or a month with nothing before it, has no percentage and says
 * so rather than showing an infinite rise.
 */
function Delta({ now, before }: { now: number; before: number }) {
  if (before <= 0) {
    return <span className="text-vp-xl text-ink-3">{t('dash.noComparison')}</span>
  }
  const change = ((now - before) / before) * 100
  const up = change >= 0
  const tone = up ? 'var(--vp-state-waiting)' : 'var(--vp-state-done)'
  return (
    <span className="tabular text-vp-xl" style={{ color: tone }} data-testid="spend-delta">
      <span aria-hidden="true">{up ? '▲' : '▼'}</span>{' '}
      {up
        ? t('dash.upBy', { n: Math.abs(change).toFixed(0) })
        : t('dash.downBy', { n: Math.abs(change).toFixed(0) })}
    </span>
  )
}

export function SpendCompare({ w, data }: { w: ShareWidget; data: ShareDashboard }) {
  const spend = data.spend
  if (!spend?.readable) return <NotCounted w={w} />
  return (
    <Tile
      kind={w.kind}
      span={w.span} height={w.height}
      testid="widget-spendcompare"
      label={t('board.kind.spendcompare')}
    >
      <div className="mb-4" data-testid="compare-today">
        <div className="flex items-baseline gap-4">
          <span className="tabular text-vp-2xl font-semibold text-ink">
            {compact(spend.today.total)}
          </span>
          <Delta now={spend.today.total} before={spend.yesterday.total} />
        </div>
        <div className="text-vp-xl text-ink-2">
          {t('dash.today')} · {t('dash.versusYesterday', { n: compact(spend.yesterday.total) })}
        </div>
      </div>
      <div data-testid="compare-month">
        <div className="flex items-baseline gap-4">
          <span className="tabular text-vp-2xl font-semibold text-ink">
            {compact(spend.month.total)}
          </span>
          <Delta now={spend.month.total} before={spend.lastMonth.total} />
        </div>
        <div className="text-vp-xl text-ink-2">
          {t('dash.thisMonth')} · {t('dash.versusLastMonth', { n: compact(spend.lastMonth.total) })}
        </div>
      </div>
    </Tile>
  )
}

/** Spend over time, bucketed by whichever dimension the board chose. */
export function SpendBars({ w, data }: { w: ShareWidget; data: ShareDashboard }) {
  const spend = data.spend
  if (!spend?.readable) return <NotCounted w={w} />
  const by = w.by ?? 'day'
  const buckets: ShareSpendBucket[] = by === 'month' ? spend.months : spend.days
  const top = buckets.reduce((n, b) => Math.max(n, b.total), 0)
  return (
    <Tile
      kind={w.kind}
      span={w.span} height={w.height}
      testid="widget-spendbars"
      label={`${t('board.kind.spendbars')} · ${byLabel(by, by)}`}
    >
      {buckets.length === 0 ? (
        <Empty text={t('dash.emptyWidget')} />
      ) : (
        <div className="flex items-end gap-1" style={{ height: '9rem' }} data-testid="spend-bars">
          {buckets.map((b) => (
            <div key={b.label} className="flex min-w-0 flex-1 flex-col justify-end">
              <div
                className="rounded-md"
                style={{
                  height: `${top > 0 ? Math.max((b.total / top) * 100, b.total > 0 ? 2 : 0) : 0}%`,
                  background: 'var(--vp-accent)',
                }}
                title={`${b.label}: ${exact(b.total)}`}
                aria-label={`${b.label}: ${exact(b.total)}`}
                role="img"
              />
            </div>
          ))}
        </div>
      )}
      <div className="tabular mt-2 flex justify-between text-vp-xl text-ink-3">
        <span>{buckets.length > 0 ? bucketLabel(buckets[0].label) : ''}</span>
        <span className="text-ink-2">{compact(top)}</span>
        <span>{buckets.length > 0 ? bucketLabel(buckets[buckets.length - 1].label) : ''}</span>
      </div>
    </Tile>
  )
}

/** Where it went, ranked. `by` picks the dimension: agent, project, model. */
export function SpendSplit({ w, data }: { w: ShareWidget; data: ShareDashboard }) {
  const density = useDensity()
  const spend = data.spend
  if (!spend?.readable) return <NotCounted w={w} />
  const by = w.by ?? 'tool'
  const groups: ShareSpendGroup[] =
    by === 'project' ? spend.projects : by === 'model' ? spend.models : spend.tools
  const top = groups.reduce((n, g) => Math.max(n, g.total), 0)
  return (
    <Tile
      kind={w.kind}
      span={w.span} height={w.height}
      testid="widget-spendsplit"
      label={`${t('board.kind.spendsplit')} · ${byLabel(by, by)}`}
    >
      {groups.length === 0 ? (
        <Empty text={t('dash.emptyWidget')} />
      ) : (
        groups.slice(0, rowsAt(density, 8)).map((g, i) => (
          <Bar
            key={g.id || `outside-${i}`}
            testid="spend-split-row"
            label={groupName(g, by, i)}
            value={compact(g.total)}
            fraction={top > 0 ? g.total / top : 0}
            tone="var(--vp-accent)"
          />
        ))
      )}
    </Tile>
  )
}

function groupName(g: ShareSpendGroup, by: string, index: number): string {
  if (g.name) return safeText(g.name)
  // An empty id on a project row is the residue: work done in a directory the
  // panel has never been told about. It is named as what it is rather than
  // numbered as though it were a project.
  if (by === 'project' && !g.id) return t('dash.outsideProjects')
  // The id is only a name where it is a name.
  //
  // A tool's id is the agent -- claude, codex -- which is one of a fixed set
  // and reads as a label. A project's is a per-link pseudonym, and this read
  // `if (g.id) return g.id` for all of them: on a board in counts mode, where
  // every name is withheld by design, four bars were labelled
  // `6e3e6771653812e2`. Not a disclosure -- the pseudonym is exactly what it is
  // for -- but a wall of hex where a name should be, next to a session grid on
  // the same screen numbering its own groups correctly.
  if (g.id && (by === 'tool' || by === 'model')) return safeText(g.id)
  return t('dash.group', { n: index + 1 })
}

/**
 * The year, as a grid of days.
 *
 * Five shades of one accent rather than a fixed palette, so the grid follows
 * the theme instead of becoming the white-on-white failure with an extra step.
 * Every square carries its exact figure in `aria-label` and on hover, and a day
 * the range never covered is drawn as a dashed outline — a different *shape*
 * from an empty day that was covered, which is the distinction a fainter fill
 * cannot make.
 */
export function SpendHeatmap({ w, data }: { w: ShareWidget; data: ShareDashboard }) {
  const spend = data.spend
  if (!spend?.readable || !spend.date) return <NotCounted w={w} />

  const totals = new Map(spend.heatmap.map((b) => [b.label, b.total]))
  // Quantiles from the non-empty days only. Including the empty ones, on a year
  // that is mostly empty, puts every cut at zero and paints every working day
  // the darkest shade.
  const nonEmpty = spend.heatmap.filter((b) => b.total > 0).map((b) => b.total).sort((a, b) => a - b)
  const cut = (q: number) => (nonEmpty.length === 0 ? Infinity : nonEmpty[Math.floor(nonEmpty.length * q)] ?? Infinity)
  const cuts = [cut(0.25), cut(0.5), cut(0.75), cut(0.9)]

  const days: { key: string; level: number; total: number }[] = []
  const end = new Date(`${spend.date}T00:00:00`)
  for (let i = WEEKS * 7 - 1; i >= 0; i--) {
    const d = new Date(end)
    d.setDate(end.getDate() - i)
    const key = `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
    const total = totals.get(key) ?? 0
    days.push({ key, total, level: level(total, cuts) })
  }

  return (
    <Tile kind={w.kind} span={w.span} height={w.height} testid="widget-spendheatmap" label={t('board.kind.spendheatmap')}>
      <div
        className="grid gap-1"
        style={{ gridTemplateRows: 'repeat(7, minmax(0, 1fr))', gridAutoFlow: 'column' }}
        data-testid="dash-heatmap"
      >
        {days.map((d) => (
          <div
            key={d.key}
            data-testid="heatmap-day"
            data-level={d.level}
            role="img"
            aria-label={t('dash.heatmapDay', { date: d.key, n: exact(d.total) })}
            title={t('dash.heatmapDay', { date: d.key, n: exact(d.total) })}
            className="rounded-md"
            style={{
              aspectRatio: '1',
              background:
                d.level === 0 ? 'var(--vp-surface-2)' : 'var(--vp-accent)',
              opacity: d.level === 0 ? 1 : 0.25 + d.level * 0.1875,
            }}
          />
        ))}
      </div>
      <div className="mt-2 flex items-center justify-end gap-2 text-vp-xl text-ink-3">
        <span>{t('dash.heatmapLess')}</span>
        {[0, 1, 2, 3, 4].map((l) => (
          <span
            key={l}
            aria-hidden="true"
            className="inline-block rounded-md"
            style={{
              width: '0.8em',
              height: '0.8em',
              background: l === 0 ? 'var(--vp-surface-2)' : 'var(--vp-accent)',
              opacity: l === 0 ? 1 : 0.25 + l * 0.1875,
            }}
          />
        ))}
        <span>{t('dash.heatmapMore')}</span>
      </div>
    </Tile>
  )
}

/** 53 whole weeks, so the first column is complete and the month labels are true. */
const WEEKS = 53

function level(total: number, cuts: number[]): number {
  if (total <= 0) return 0
  let l = 1
  for (const c of cuts) {
    if (total >= c) l++
  }
  return Math.min(l, 4)
}

function pad(n: number): string {
  return n < 10 ? `0${n}` : String(n)
}

export type { ShareSpend }
