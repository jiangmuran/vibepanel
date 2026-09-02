import { AlertTriangle, RefreshCw } from 'lucide-react'

import type { TokenUsage as Usage, UsageDay } from '../../protocol/wire'
import { t, useLang } from '../../i18n'
import type { PanelDensity } from '../chrome'
import { safeText } from '../text'
import { formatAgo } from './ago'
import { compact, exact, sum, totalOf } from './tokens'
import { dayTotal, projectTotal, toolShares, windowTotal } from './spend'
import { SPEND_SPAN, type Spend } from './useSpend'

/**
 * The token ledger with the side panel to itself.
 *
 * The dock's block is the glance — six figures and a bar. This is the same
 * subject opened out: where the six came from, what the range is made of, and
 * every reason a figure might be a lower bound. It is not the analysis; the
 * analysis is the full-width view behind the header's expand control, because
 * a 53-week grid needs about 580 pixels before a day-of-week gutter and the
 * per-session table has six numeric columns.
 *
 * Three sizes, three jobs, and the middle one earns its place by answering the
 * question the glance provokes — "why is that number that" — without leaving
 * the column you were reading.
 *
 * Money and lines of code are still absent and still settled: pricing is per
 * model, per tier and changes, and a reader cannot tell a price the panel
 * guessed from one it was told.
 */
export function TokenUsage({
  spend,
  projectId,
  projectName,
  density,
}: {
  /** The one reading, shared with the dock's compact block. See useSpend. */
  spend: Spend
  projectId: string | null
  projectName: string | null
  density: PanelDensity
}) {
  useLang()
  const { data, error, now, busy, refresh } = spend

  if (error !== null) {
    return (
      <p className="px-3 py-4 text-vp-base" style={{ color: 'var(--vp-state-waiting)' }}>
        {safeText(error)}
      </p>
    )
  }
  if (data === null) {
    return <p className="px-3 py-4 text-vp-base text-ink-2">{t('spend.scanning')}</p>
  }

  const known = data.scannedAt > 0
  const range = sum(data.byDay)
  const rangeTotal = totalOf(range)
  const missing = data.sources.filter((s) => !s.found)
  const skipped = data.sources.reduce((n, s) => n + s.skipped, 0)
  const tools = toolShares(data)

  // Requests are how the four token classes become a rate rather than a pile.
  // 400k tokens is meaningless; 400k over 60 requests is a session with a large
  // context being re-sent, which is a thing somebody can act on.
  const perRequest = range.requests > 0 ? Math.round(rangeTotal / range.requests) : null

  // `byMonth` is every month there has been, and the panel had never opened it.
  // The month you are in and the one before it is the comparison people make; a
  // thirty-day range straddles two of them and answers neither.
  const thisMonth = monthAt(data.byMonth, data.today.slice(0, 7))
  const lastMonth = monthAt(data.byMonth, previousMonth(data.today.slice(0, 7)))

  const cols = density === 'wide' ? 'grid-cols-2' : 'grid-cols-1'

  return (
    <div className="px-3 py-2" data-testid="token-panel">
      <Section label={t('spend.headline')}>
        <div className={`grid gap-x-4 ${cols}`}>
          <Row label={t('spend.today')} value={known ? dayTotal(data.byDay, data.today) : null} />
          <Row
            label={t('spend.week')}
            value={known ? windowTotal(data.byDay, data.today, 7) : null}
          />
          <Row
            label={projectName ?? t('spend.thisProject')}
            value={known ? projectTotal(data, projectId) : null}
          />
          <Row label={t('spend.rangeDays', { n: SPEND_SPAN })} value={known ? rangeTotal : null} />
        </div>
      </Section>

      {known && <Spark data={data} />}

      {known && (
        <>
          {/* What the range is made of. Cache read is routinely the largest of
              the four by an order of magnitude, so a single total hides the one
              number that explains the rest. */}
          <Section label={t('spend.breakdown')}>
            <div className={`grid gap-x-4 ${cols}`}>
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
              <div className={`grid gap-x-4 ${cols}`}>
                <Row label={t('spend.thisMonth')} value={thisMonth} />
                <Row label={t('spend.lastMonth')} value={lastMonth} />
              </div>
            </Section>
          )}

          {tools.length > 0 && (
            <Section label={t('spend.tools')}>
              <div className="vp-rows">
                {data.byTool.map((tool) => (
                  <div
                    key={tool.tool}
                    data-testid="token-tool"
                    className="flex items-baseline justify-between gap-2 py-[1px] text-vp-xs"
                    // The same composition the bar's segments carry. `requests
                    // · total` beside it are two of the three readings that
                    // disagree; this is where the third one is.
                    title={t('spend.toolTitle', {
                      tool: tool.tool,
                      total: exact(totalOf(tool)),
                      output: exact(tool.output),
                      cache: exact(tool.cacheRead),
                    })}
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

      {!known && (
        <p className="mt-2 text-vp-sm leading-relaxed text-ink-2">
          {data.scanning ? t('spend.scanning') : t('spend.neverScanned')}
        </p>
      )}

      {/* Said here, not only behind the expand control. Somebody who never
          opens the full view still has to know these are the agents' figures
          and not the panel's, or every number above is read as a claim the
          panel cannot make. */}
      <p className="mt-2 text-vp-xs leading-relaxed text-ink-2">{t('spend.whose')}</p>

      {missing.map((s) => (
        <Warning
          key={s.tool}
          text={t('spend.sourceMissing', { tool: safeText(s.tool), why: s.problem || '?' })}
        />
      ))}
      {skipped > 0 && <Warning text={t('spend.lowerBound', { n: exact(skipped) })} />}
      {data.passError !== '' && <Warning text={t('spend.passError', { why: data.passError })} />}

      <button
        type="button"
        data-testid="token-refresh"
        onClick={refresh}
        disabled={busy}
        title={busy ? t('spend.refreshing') : t('spend.refresh')}
        className="vp-press mt-2 flex w-full items-center justify-center gap-1 rounded-vp border border-hairline bg-surface-2 px-2 py-1.5 text-vp-sm text-ink transition-colors duration-200 ease-vp hover:bg-surface disabled:opacity-50"
      >
        <RefreshCw size={12} className={busy || data.scanning ? 'animate-spin' : ''} />
        {t('spend.refresh')}
      </button>
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
    <div className="mt-2 border-t border-hairline pt-1.5 first:mt-0 first:border-t-0 first:pt-0">
      <p className="mb-0.5 text-vp-xs uppercase tracking-wide text-ink-2">{label}</p>
      {children}
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
      <span className="truncate text-ink-2" title={label}>
        {safeText(label)}
      </span>
      <span
        className="tabular shrink-0 text-ink"
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
  for (let i = SPEND_SPAN - 1; i >= 0; i--) {
    const at = new Date(end)
    at.setDate(at.getDate() - i)
    const key = `${at.getFullYear()}-${String(at.getMonth() + 1).padStart(2, '0')}-${String(
      at.getDate(),
    ).padStart(2, '0')}`
    days.push({ day: key, total: byDay.has(key) ? (byDay.get(key) as number) : null })
  }
  const peak = Math.max(1, ...days.map((x) => x.total ?? 0))

  return (
    <div className="mt-2 flex h-10 items-end gap-px" data-testid="token-spark">
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
