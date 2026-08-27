import { useEffect, useState } from 'react'
import { AlertTriangle, Maximize2, RefreshCw } from 'lucide-react'

import { api } from '../../protocol/api'
import type { TokenUsage as Usage, UsageDay } from '../../protocol/wire'
import { t, useLang } from '../../i18n'
import type { PanelDensity } from '../chrome'
import { safeText } from '../text'
import { formatAgo } from './ago'
import { compact, exact, sum, totalOf } from './tokens'

/** How often the panel refreshes while it is on screen. */
const POLL_MS = 20000

/** How many of the range's days the sparkline shows. */
const SPARK_DAYS = 30

/**
 * A summary of what the agents have spent, in a side panel.
 *
 * The panel is a glance, not the analysis — everything that needs width lives
 * behind the button at the bottom. The measurement that settled it: a
 * GitHub-style year grid is 53 columns, and 53 columns of anything legible is
 * about 580 pixels before a day-of-week gutter. Squeezing it into the panel
 * means either three months instead of a year or squares too small to aim at,
 * and both are worse than a link.
 *
 * What the panel holds is everything that fits in a column of numbers, which
 * turned out to be a good deal more than it was showing. It had two figures and
 * a sparkline; the payload it was already fetching carries the four token
 * classes separately, a request count, a month-by-month series, a per-tool
 * breakdown, how many agent sessions are behind the figures and when the last
 * pass finished. All of it is a label and a number — the shape a narrow column
 * is *good* at — and none of it was on screen.
 *
 * What is deliberately still absent is money and lines of code. The panel does
 * not know either: pricing is per model, per tier and changes, and the reader
 * cannot tell a price the panel guessed from one it was told. That is settled.
 */
export function TokenUsage({
  projectId,
  density = 'narrow',
  onOpen,
}: {
  projectId: string | null
  density?: PanelDensity
  onOpen: () => void
}) {
  useLang()
  const [data, setData] = useState<Usage | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  // One clock for every relative time on the panel, moved on by the poll. See
  // GitPanel's Ago for why this is not a Date.now() in the body.
  const [now, setNow] = useState(() => Math.floor(Date.now() / 1000))

  useEffect(() => {
    // Self-scheduling and only while mounted, like the monitor: a panel nobody
    // is looking at should cost nothing, and this one starts a disk pass.
    let cancelled = false
    let timer = 0
    const tick = async () => {
      try {
        const next = await api.tokenUsage({ days: SPARK_DAYS, project: projectId ?? undefined })
        if (!cancelled) {
          setData(next)
          setNow(Math.floor(Date.now() / 1000))
          setError(null)
        }
      } catch (e) {
        if (!cancelled) setError(e instanceof Error ? e.message : String(e))
      }
      if (!cancelled) timer = window.setTimeout(() => void tick(), POLL_MS)
    }
    void tick()
    return () => {
      cancelled = true
      window.clearTimeout(timer)
    }
  }, [projectId])

  const refresh = async () => {
    setBusy(true)
    try {
      await api.refreshTokenUsage()
      setData(await api.tokenUsage({ days: SPARK_DAYS, project: projectId ?? undefined }))
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  if (error) {
    return (
      <p className="px-3 py-4 text-vp-base" style={{ color: 'var(--vp-state-waiting)' }}>
        {safeText(error)}
      </p>
    )
  }
  if (!data) {
    return <p className="px-3 py-4 text-vp-base text-ink-2">{t('spend.scanning')}</p>
  }

  const today = data.byDay.find((d) => d.day === data.today)
  const todayTotal = today ? totalOf(today) : 0
  const range = sum(data.byDay)
  const rangeTotal = totalOf(range)
  // Never read is not zero, and the difference is the whole point. Until a
  // pass has finished there is no figure to show at all.
  const known = data.scannedAt > 0
  const missing = data.sources.filter((s) => !s.found)
  const skipped = data.sources.reduce((n, s) => n + s.skipped, 0)

  // `byMonth` is every month there has been, oldest first, and the panel had
  // never opened it. The month you are in and the one before it is the
  // comparison people actually make — a range of thirty days straddles two of
  // them and answers neither.
  const thisMonth = monthAt(data.byMonth, data.today.slice(0, 7))
  const lastMonth = monthAt(data.byMonth, previousMonth(data.today.slice(0, 7)))

  // Requests are how the four token classes become a rate rather than a pile.
  // 400k tokens is meaningless; 400k over 60 requests is a session with a large
  // context being re-sent, which is a thing somebody can act on.
  const perRequest = range.requests > 0 ? Math.round(rangeTotal / range.requests) : null

  return (
    <div className="px-3 py-2" data-testid="token-panel">
      {/* The two headline figures side by side rather than stacked. They are
          the same kind of fact measured over two windows, and reading them as
          a pair is the point; stacked, each had a whole row to itself and the
          panel was two lines deep before it said anything else. */}
      <div className="flex items-baseline justify-between gap-3">
        <Headline label={t('spend.today')} value={known ? todayTotal : null} emphasis />
        <Headline label={t('spend.rangeDays', { n: SPARK_DAYS })} value={known ? rangeTotal : null} />
      </div>

      {known && <Spark data={data} />}

      {!known && (
        <p className="mt-2 text-vp-sm leading-relaxed text-ink-2">
          {data.scanning ? t('spend.scanning') : t('spend.neverScanned')}
        </p>
      )}

      {known && (
        <>
          {/* What the range is made of. Four classes and a count, all of which
              the payload has always carried and none of which reached the
              panel — cache read is usually the largest of the four by an order
              of magnitude, so a single total hides the one number that
              explains the rest. Two columns above 380px. */}
          <Section label={t('spend.breakdown')}>
            <div className={`grid gap-x-4 ${density === 'wide' ? 'grid-cols-2' : 'grid-cols-1'}`}>
              <Row label={t('spend.input')} value={range.input} />
              <Row label={t('spend.output')} value={range.output} />
              <Row label={t('spend.cacheRead')} value={range.cacheRead} />
              <Row label={t('spend.cacheWrite')} value={range.cacheWrite} />
              <Row label={t('spend.requests')} value={range.requests} plain />
              {perRequest !== null && <Row label={t('spend.perRequest')} value={perRequest} />}
            </div>
          </Section>

          {(thisMonth !== null || lastMonth !== null) && (
            <Section label={t('spend.month')}>
              <div className={`grid gap-x-4 ${density === 'wide' ? 'grid-cols-2' : 'grid-cols-1'}`}>
                <Row label={t('spend.thisMonth')} value={thisMonth} />
                <Row label={t('spend.lastMonth')} value={lastMonth} />
              </div>
            </Section>
          )}

          {data.byTool.length > 0 && (
            <Section label={t('spend.tools')}>
              <div className="vp-rows">
                {data.byTool.map((tool) => (
                  <div
                    key={tool.tool}
                    data-testid="token-tool"
                    className="flex items-baseline justify-between gap-2 py-[1px] text-vp-xs"
                  >
                    <span className="flex min-w-0 items-baseline gap-1">
                      {/* A tool that could not be read fully is marked, not
                          silently short. Shape and a word, not a tint. */}
                      {tool.problems > 0 && (
                        <AlertTriangle
                          size={9}
                          className="shrink-0 self-center"
                          style={{ color: 'var(--vp-state-waiting)' }}
                        />
                      )}
                      <span className="truncate text-ink-2">{safeText(tool.tool)}</span>
                    </span>
                    <span className="tabular shrink-0 text-ink-2">
                      {exact(tool.requests)} · {compact(totalOf(tool))}
                    </span>
                  </div>
                ))}
              </div>
            </Section>
          )}

          {/* Where the numbers came from and how old they are. `sources` was
              read only for its failures, so a working panel said nothing at all
              about what it had read — and "0 today" from a reader that has not
              run since Tuesday looks exactly like a quiet Tuesday. */}
          <Section label={t('spend.source')}>
            {data.sessionCount > 0 && (
              <div className="tabular text-vp-xs text-ink-2" data-testid="token-sessions">
                {t('spend.sessionCount', { n: exact(data.sessionCount) })}
              </div>
            )}
            {data.sources
              .filter((s) => s.found)
              .map((s) => (
                <div
                  key={s.tool}
                  data-testid="token-source"
                  className="tabular truncate text-vp-xs text-ink-2"
                  title={safeText(s.root)}
                >
                  {t('spend.sourceRead', { tool: safeText(s.tool), files: s.files })}
                </div>
              ))}
            <div className="tabular text-vp-xs text-ink-2" data-testid="token-scanned">
              {t('spend.scannedAgo', { ago: formatAgo(data.scannedAt, now) })}
            </div>
          </Section>
        </>
      )}

      {/* Said on the panel, not only behind the button. Somebody who never
          opens the full view still has to know these are the agents' figures
          and not the panel's, or every number above is read as a claim the
          panel cannot make. */}
      <p className="mt-2 text-vp-xs leading-relaxed text-ink-3">{t('spend.whose')}</p>

      {missing.map((s) => (
        <Warning
          key={s.tool}
          text={t('spend.sourceMissing', { tool: s.tool, why: s.problem || '?' })}
        />
      ))}
      {skipped > 0 && <Warning text={t('spend.lowerBound', { n: exact(skipped) })} />}
      {data.passError !== '' && <Warning text={t('spend.passError', { why: data.passError })} />}

      <div className="mt-2 flex items-center gap-1">
        <button
          type="button"
          data-testid="token-open"
          onClick={onOpen}
          className="vp-press flex flex-1 items-center justify-center gap-1 rounded-vp border border-hairline bg-surface-2 px-2 py-1.5 text-vp-sm text-ink transition-colors duration-200 ease-vp hover:bg-surface"
        >
          <Maximize2 size={12} />
          {t('spend.open')}
        </button>
        <button
          type="button"
          data-testid="token-refresh"
          onClick={() => void refresh()}
          disabled={busy}
          title={busy ? t('spend.refreshing') : t('spend.refresh')}
          className="vp-press shrink-0 rounded-vp border border-hairline p-1.5 text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink disabled:opacity-50"
        >
          <RefreshCw size={12} className={busy || data.scanning ? 'animate-spin' : ''} />
        </button>
      </div>
    </div>
  )
}

/** The total for one `YYYY-MM` bucket, or null if the series has no such month. */
function monthAt(byMonth: UsageDay[], key: string): number | null {
  const found = byMonth.find((m) => m.day === key)
  return found ? totalOf(found) : null
}

/** The month before `YYYY-MM`, in the same form. */
function previousMonth(key: string): string {
  const [y, m] = key.split('-').map(Number)
  if (!Number.isFinite(y) || !Number.isFinite(m)) return ''
  const back = m === 1 ? [y - 1, 12] : [y, m - 1]
  return `${back[0]}-${String(back[1]).padStart(2, '0')}`
}

/** A heading over a set of rows, with a hairline above it. */
function Section({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="mt-2 border-t border-hairline pt-1.5">
      <p className="mb-0.5 text-vp-xs uppercase tracking-wide text-ink-2">{label}</p>
      {children}
    </div>
  )
}

/**
 * One headline number, or an explicit absence.
 *
 * `null` renders an em dash rather than a zero. There is no formatting trick
 * that makes a zero mean "not known", so it does not get to try.
 */
function Headline({
  label,
  value,
  emphasis,
}: {
  label: string
  value: number | null
  emphasis?: boolean
}) {
  return (
    <div className="min-w-0">
      <div className="truncate text-vp-xs text-ink-2">{label}</div>
      <div
        className={`tabular ${emphasis ? 'text-vp-lg' : 'text-vp-md'} text-ink`}
        title={value === null ? undefined : `${exact(value)} ${t('spend.tokens')}`}
      >
        {value === null ? '—' : compact(value)}
      </div>
    </div>
  )
}

/**
 * One label and one figure in a column of them.
 *
 * `plain` prints the number as it is rather than folding it: a request count is
 * four digits at most and "1.2k requests" throws away the digit somebody is
 * counting. Token totals are nine digits and always fold.
 */
function Row({ label, value, plain }: { label: string; value: number | null; plain?: boolean }) {
  return (
    <div className="flex min-w-0 items-baseline justify-between gap-2 py-[1px] text-vp-xs">
      <span className="truncate text-ink-2">{label}</span>
      <span
        className="tabular shrink-0 text-ink-2"
        title={value === null || plain ? undefined : `${exact(value)} ${t('spend.tokens')}`}
      >
        {value === null ? '—' : plain ? exact(value) : compact(value)}
      </span>
    </div>
  )
}

/**
 * The range as bars.
 *
 * Height carries the value and every bar has its exact figure on hover, which
 * is the same rule the meters follow: colour alone says nothing, and this one
 * is a single hue anyway.
 */
function Spark({ data }: { data: Usage }) {
  const byDay = new Map(data.byDay.map((d) => [d.day, totalOf(d)]))
  const [y, m, d] = data.today.split('-').map(Number)
  const end = new Date(y, (m || 1) - 1, d || 1)
  const days: { day: string; total: number | null }[] = []
  for (let i = SPARK_DAYS - 1; i >= 0; i--) {
    const at = new Date(end)
    at.setDate(at.getDate() - i)
    const key = `${at.getFullYear()}-${String(at.getMonth() + 1).padStart(2, '0')}-${String(
      at.getDate(),
    ).padStart(2, '0')}`
    days.push({ day: key, total: byDay.has(key) ? (byDay.get(key) as number) : null })
  }
  const peak = Math.max(1, ...days.map((x) => x.total ?? 0))

  return (
    <div className="mt-1 flex h-8 items-end gap-px" data-testid="token-spark">
      {days.map((x) => (
        <div
          key={x.day}
          title={
            x.total === null
              ? t('spend.cellOutside', { day: x.day })
              : x.total === 0
                ? t('spend.cellNone', { day: x.day })
                : t('spend.cellSpent', { day: x.day, n: exact(x.total) })
          }
          className="min-w-0 flex-1 rounded-md"
          style={{
            height: `${Math.max(x.total ? 8 : 2, ((x.total ?? 0) / peak) * 100)}%`,
            // A day outside the range is a hairline, a day inside it with
            // nothing on it is a visible floor, and the two are different
            // heights as well as different colours.
            background: x.total === null ? 'var(--vp-surface-2)' : 'var(--vp-accent)',
            opacity: x.total ? 1 : 0.45,
          }}
        />
      ))}
    </div>
  )
}

function Warning({ text }: { text: string }) {
  return (
    <p
      className="mt-1.5 flex items-start gap-1 text-vp-xs leading-relaxed"
      style={{ color: 'var(--vp-state-waiting)' }}
    >
      <AlertTriangle size={11} className="mt-0.5 shrink-0" />
      <span>{safeText(text)}</span>
    </p>
  )
}
