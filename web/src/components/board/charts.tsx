import type { ShareDashboard, ShareSpendBucket, ShareWidget } from '../../protocol/wire'
import { t } from '../../i18n'
import { Empty, Tile } from './Tile'
import { bucketLabel, compact, exact } from './format'
import { byLabel } from './labels'
import { Spark } from '../spark'

/**
 * The board's area chart: the shared one, plus what a board shows instead of a
 * line it cannot draw.
 *
 * `Spark` returns null on a series too short to be a line, because each caller
 * has its own answer to that. On a wall the answer has to be words -- a blank
 * rectangle where a chart goes reads as a chart that failed, not as a series
 * with one reading in it.
 */
function Area(props: { values: number[]; max: number; tone: string; testid: string }) {
  const chart = Spark(props)
  return chart ?? <Empty text={t('dash.trendShort')} />
}

/**
 * The widgets that draw a shape rather than a figure.
 *
 * These are the movement tier, and the reason the tier exists is not
 * decoration. A wall of still numbers cannot be told from a wall that has
 * frozen — six sessions all "done" and a flat CPU figure is either a calm
 * afternoon or a page that stopped talking to the panel forty minutes ago — and
 * a line that visibly changes is the cheapest honest proof that the screen is
 * alive. The connection glyph says the same thing in words; this says it
 * without being read.
 *
 * Every one of them draws inline SVG from numbers already on the payload. There
 * is no chart library, no fetch and no URL below this line, which is the same
 * property the rest of the board has.
 */


/**
 * CPU, memory or load over the last few minutes.
 *
 * The one widget that needs history, and the history is a ring in the server's
 * memory rather than a table — see internal/httpapi/sharelive.go. It is short
 * after a restart and on a screen that has just been switched on, and it says
 * so rather than drawing a confident flat line from two points.
 */
export function MachineArea({ w, data }: { w: ShareWidget; data: ShareDashboard }) {
  const by = w.by ?? 'cpu'
  const trend = data.trend
  const readings = trend?.points ?? []

  // A CPU reading of null is "/proc could not be read", which is a different
  // fact from zero and must not be drawn as a floor. Those points are dropped;
  // if none survive, the tile says so.
  const values: number[] = []
  for (const p of readings) {
    if (by === 'cpu') {
      if (p.cpu === null) continue
      values.push(p.cpu)
    } else if (by === 'memory') {
      values.push(p.memory * 100)
    } else {
      values.push(p.load)
    }
  }
  const max = by === 'load' ? Math.max(1, ...values) : 100
  const latest = values.length > 0 ? values[values.length - 1] : null
  const tone = by === 'memory' ? 'var(--vp-state-working)' : 'var(--vp-accent)'

  return (
    <Tile
      kind={w.kind}
      span={w.span}
      height={w.height}
      testid="widget-machinearea"
      label={byLabel(by, by)}
    >
      <div className="flex h-full min-h-0 flex-col" data-testid="machinearea">
        <span className="tabular text-vp-3xl font-semibold text-ink">
          {latest === null
            ? '—'
            : by === 'load'
              ? latest.toFixed(2)
              : `${Math.round(latest)}%`}
        </span>
        <div className="mt-2 min-h-16 flex-1">
          <Area values={values} max={max} tone={tone} testid="machinearea-svg" />
        </div>
        {trend && trend.points.length > 0 && (
          <span className="mt-1 text-vp-xl text-ink-3">
            {t('dash.lastMinutes', {
              n: Math.max(1, Math.round((trend.points.length * trend.every) / 60)),
            })}
          </span>
        )}
      </div>
    </Tile>
  )
}

/**
 * The hero for token spend: how much today, how fast, and when it was counted.
 *
 * The last part is the honest one and it is not optional. These figures come
 * from a pass over the agents' transcripts, so "now" is "as of the last pass" —
 * a live meter implying a per-second reading would be a lie told in the largest
 * type on the screen. So the rate is drawn from the differences between
 * samples, which is a real rate over a real interval, and the tile says when
 * the last pass finished underneath it.
 *
 * When there are not yet two samples to difference, it falls back to the
 * average so far today and says *that* instead. Two different sentences,
 * because they are two different numbers.
 */
export function TokenBurn({ w, data }: { w: ShareWidget; data: ShareDashboard }) {
  const spend = data.spend
  const trend = data.trend
  if (!spend?.readable) {
    return (
      <Tile kind={w.kind} span={w.span} height={w.height} testid="widget-tokenburn" plain>
        <Empty text={t('dash.noSpendYet')} />
      </Tile>
    )
  }

  const deltas = burnDeltas(trend?.points ?? [])
  const perMinute = rateFrom(trend?.points ?? [])
  const fallback = spend.today.total / Math.max(spend.hoursToday, 1 / 60)

  return (
    <Tile kind={w.kind} span={w.span} height={w.height} testid="widget-tokenburn" plain>
      <div className="flex h-full min-h-0 flex-col justify-center" data-testid="tokenburn">
        <span className="tabular truncate text-vp-3xl font-semibold text-ink">
          {compact(spend.today.total)}
        </span>
        <span className="truncate text-vp-xl text-ink-2">
          {t('board.metric.tokensToday')} · {exact(spend.today.total)}
        </span>
        <span className="tabular truncate text-vp-2xl font-semibold text-ink-2">
          {perMinute === null
            ? `${compact(fallback)} · ${t('dash.perHourToday')}`
            : `${compact(perMinute)} · ${t('dash.perMinute')}`}
        </span>
        {deltas.length >= 2 && (
          <div className="mt-2 min-h-12 flex-1">
            <Area
              values={deltas}
              max={Math.max(1, ...deltas)}
              tone="var(--vp-accent)"
              testid="tokenburn-svg"
            />
          </div>
        )}
        <span className="truncate text-vp-xl text-ink-3">
          {t('dash.spendAt', { time: new Date(spend.scannedAt * 1000).toLocaleTimeString() })}
        </span>
      </div>
    </Tile>
  )
}

/**
 * Tokens between one sample and the next.
 *
 * Clamped at zero on purpose: the running total is the day's, so it resets at
 * the server's local midnight and the sample across that boundary is a large
 * negative. A negative bar on a spend chart is not a refund.
 */
function burnDeltas(pts: { tokens: number }[]): number[] {
  const out: number[] = []
  for (let i = 1; i < pts.length; i++) {
    out.push(Math.max(0, pts[i].tokens - pts[i - 1].tokens))
  }
  return out
}

/** Tokens per minute across the whole ring, or null with too little to say. */
function rateFrom(pts: { at: number; tokens: number }[]): number | null {
  if (pts.length < 2) return null
  const first = pts[0]
  const last = pts[pts.length - 1]
  const seconds = last.at - first.at
  const tokens = last.tokens - first.tokens
  if (seconds < 30 || tokens < 0) return null
  return (tokens / seconds) * 60
}

/**
 * Every token this panel has ever recorded, as one accumulating figure.
 *
 * A chart says how it is going; an odometer says how far it has come, and it is
 * the only number on a board that only ever goes up. Summed on the server from
 * the months already in hand, so it costs nothing.
 */
export function Odometer({ w, data }: { w: ShareWidget; data: ShareDashboard }) {
  const spend = data.spend
  return (
    <Tile kind={w.kind} span={w.span} height={w.height} testid="widget-odometer" plain>
      {spend?.readable ? (
        <div className="flex min-w-0 flex-col justify-center" data-testid="odometer">
          <span className="tabular truncate text-vp-3xl font-semibold text-ink">
            {compact(spend.allTime.total)}
          </span>
          <span className="truncate text-vp-xl text-ink-2">{t('dash.allTime')}</span>
          <span className="tabular truncate text-vp-xl text-ink-3">
            {exact(spend.allTime.total)} · {exact(spend.allTime.requests)}{' '}
            {t('board.metric.requestsToday')}
          </span>
        </div>
      ) : (
        <Empty text={t('dash.noSpendYet')} />
      )}
    </Tile>
  )
}

/** Which series a widget is pointed at, and its label. */
function seriesOf(data: ShareDashboard, by: string): ShareSpendBucket[] {
  return by === 'month' ? (data.spend?.months ?? []) : (data.spend?.days ?? [])
}

/**
 * The spend series as one line, at tile size.
 *
 * A bar chart needs a tile to itself; a line reads beside the number it belongs
 * to, which is the only way a trend fits on a board that is mostly figures.
 */
export function Sparkline({ w, data }: { w: ShareWidget; data: ShareDashboard }) {
  const by = w.by ?? 'day'
  const series = seriesOf(data, by)
  const values = series.map((b) => b.total)
  const last = values.length > 0 ? values[values.length - 1] : 0
  return (
    <Tile
      kind={w.kind}
      span={w.span}
      height={w.height}
      testid="widget-sparkline"
      label={byLabel(by, by)}
    >
      <div className="flex h-full min-h-0 flex-col" data-testid="sparkline">
        <span className="tabular text-vp-2xl font-semibold text-ink">{compact(last)}</span>
        <div className="mt-2 min-h-12 flex-1">
          <Area
            values={values}
            max={Math.max(1, ...values)}
            tone="var(--vp-accent)"
            testid="sparkline-svg"
          />
        </div>
      </div>
    </Tile>
  )
}

/** The four token columns, in the order they stack. */
const COLUMNS = [
  { key: 'input', tone: 'var(--vp-accent)' },
  { key: 'output', tone: 'var(--vp-state-working)' },
  { key: 'cacheRead', tone: 'var(--vp-state-done)' },
  { key: 'cacheWrite', tone: 'var(--vp-state-waiting)' },
] as const

/**
 * The spend series with the four columns stacked.
 *
 * A total hides the thing worth seeing: a day that is nine tenths cache reads
 * and a day that is nine tenths output are the same number and two completely
 * different afternoons. Red line 4 — the legend under it names every band, so
 * the composition is readable without separating the hues.
 */
export function SpendStack({ w, data }: { w: ShareWidget; data: ShareDashboard }) {
  const by = w.by ?? 'day'
  const series = seriesOf(data, by)
  const most = series.reduce((n, b) => Math.max(n, b.total), 0)
  return (
    <Tile
      kind={w.kind}
      span={w.span}
      height={w.height}
      testid="widget-spendstack"
      label={byLabel(by, by)}
    >
      {series.length === 0 ? (
        <Empty text={t('dash.noSpendYet')} />
      ) : (
        <>
          <div className="flex h-full min-h-16 items-end gap-1" data-testid="spendstack">
            {series.map((b) => (
              <div
                key={b.label}
                className="flex min-w-0 flex-1 flex-col justify-end"
                style={{ height: `${most > 0 ? (b.total / most) * 100 : 0}%` }}
                title={`${bucketLabel(b.label)} · ${exact(b.total)}`}
                data-testid="spendstack-bar"
              >
                {COLUMNS.map((c) => (
                  <div
                    key={c.key}
                    style={{
                      height: `${b.total > 0 ? (b[c.key] / b.total) * 100 : 0}%`,
                      background: c.tone,
                    }}
                  />
                ))}
              </div>
            ))}
          </div>
          <div className="mt-3 flex flex-wrap gap-x-5 gap-y-1">
            {COLUMNS.map((c) => (
              <span key={c.key} className="flex items-center gap-2 text-vp-xl text-ink-3">
                <span
                  aria-hidden="true"
                  className="inline-block h-3 w-3 rounded-vp"
                  style={{ background: c.tone }}
                />
                {t(`dash.col${c.key[0].toUpperCase()}${c.key.slice(1)}` as 'dash.colInput')}
              </span>
            ))}
          </div>
        </>
      )}
    </Tile>
  )
}
