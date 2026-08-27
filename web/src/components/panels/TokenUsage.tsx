import { useEffect, useState } from 'react'
import { AlertTriangle, Maximize2, RefreshCw } from 'lucide-react'

import { api } from '../../protocol/api'
import type { TokenUsage as Usage } from '../../protocol/wire'
import { t, useLang } from '../../i18n'
import { safeText } from '../text'
import { compact, exact, sum, totalOf } from './tokens'

/** How often the panel refreshes while it is on screen. */
const POLL_MS = 20000

/** How many of the range's days the sparkline shows. */
const SPARK_DAYS = 30

/**
 * A summary of what the agents have spent, in a 280-pixel column.
 *
 * The panel is a glance, not the analysis — everything that needs width lives
 * behind the button at the bottom. The measurement that settled it: a
 * GitHub-style year grid is 53 columns, and 53 columns of anything legible is
 * about 580 pixels before a day-of-week gutter. Squeezing it into the panel
 * means either three months instead of a year or squares too small to aim at,
 * and both are worse than a link.
 *
 * What the panel does hold is the two figures somebody actually glances for —
 * today, and the range — plus the reason any of them might be missing.
 */
export function TokenUsage({
  projectId,
  onOpen,
}: {
  projectId: string | null
  onOpen: () => void
}) {
  useLang()
  const [data, setData] = useState<Usage | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

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
  const rangeTotal = totalOf(sum(data.byDay))
  // Never read is not zero, and the difference is the whole point. Until a
  // pass has finished there is no figure to show at all.
  const known = data.scannedAt > 0
  const missing = data.sources.filter((s) => !s.found)
  const skipped = data.sources.reduce((n, s) => n + s.skipped, 0)

  return (
    <div className="px-3 py-3" data-testid="token-panel">
      <Figure
        label={t('spend.today')}
        value={known ? todayTotal : null}
        emphasis
      />
      <Figure label={t('spend.rangeDays', { n: SPARK_DAYS })} value={known ? rangeTotal : null} />

      {known && <Spark data={data} />}

      {!known && (
        <p className="mt-2 text-vp-sm leading-relaxed text-ink-2">
          {data.scanning ? t('spend.scanning') : t('spend.neverScanned')}
        </p>
      )}

      {/* Said on the panel, not only behind the button. Somebody who never
          opens the full view still has to know these are the agents' figures
          and not the panel's, or every number above is read as a claim the
          panel cannot make. */}
      <p className="mt-3 text-vp-xs leading-relaxed text-ink-3">{t('spend.whose')}</p>

      {missing.map((s) => (
        <Warning
          key={s.tool}
          text={t('spend.sourceMissing', { tool: s.tool, why: s.problem || '?' })}
        />
      ))}
      {skipped > 0 && <Warning text={t('spend.lowerBound', { n: exact(skipped) })} />}
      {data.passError !== '' && <Warning text={t('spend.passError', { why: data.passError })} />}

      <div className="mt-3 flex items-center gap-1">
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

/**
 * One headline number, or an explicit absence.
 *
 * `null` renders an em dash rather than a zero. There is no formatting trick
 * that makes a zero mean "not known", so it does not get to try.
 */
function Figure({
  label,
  value,
  emphasis,
}: {
  label: string
  value: number | null
  emphasis?: boolean
}) {
  return (
    <div className="mb-2 flex items-baseline justify-between gap-2">
      <span className="truncate text-vp-sm text-ink-2">{label}</span>
      <span
        className={`tabular shrink-0 ${emphasis ? 'text-vp-lg' : 'text-vp-md'} text-ink`}
        title={value === null ? undefined : `${exact(value)} ${t('spend.tokens')}`}
      >
        {value === null ? '—' : compact(value)}
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
      className="mt-2 flex items-start gap-1 text-vp-xs leading-relaxed"
      style={{ color: 'var(--vp-state-waiting)' }}
    >
      <AlertTriangle size={11} className="mt-0.5 shrink-0" />
      <span>{safeText(text)}</span>
    </p>
  )
}
