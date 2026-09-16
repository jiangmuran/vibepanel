import { useEffect, useMemo, useState } from 'react'
import { AlertTriangle, RefreshCw, X } from 'lucide-react'

import { api } from '../protocol/api'
import type { Project, TokenUsage, UsageTotals } from '../protocol/wire'
import { t, useLang, type Key } from '../i18n'
import { safeText } from './text'
import { compact, exact, monthLabels, totalOf, weeks } from './panels/tokens'
import {
  axisTicks,
  dayPoints,
  pace,
  rank,
  shortDay,
  sinceFirst,
  windowValue,
  type DayPoint,
  type Metric,
  type Ranked,
} from './panels/spend'

/** The range control's positions, in days. */
const RANGES = [7, 30, 90, 365]

/** How far back the year grid reaches. Mirrors heatmapDays in internal/httpapi. */
const HEATMAP_DAYS = 371

/** How often the view refreshes while it is open. */
const POLL_MS = 20000

/** Rows a ranking shows before it is asked for the rest. The project list
 *  spans the height of two cards and gets the room to match. */
const RANK_ROWS = 6
const RANK_ROWS_TALL = 11

/** Rows the session table shows before it is asked for the rest. */
const SESSION_ROWS = 12

/**
 * The full picture of what the agents spent.
 *
 * Laid out as a dashboard rather than a report: two figures that answer "how
 * much today", a chart that answers "is that unusual", and then where it went.
 * It was one long column of equally weighted sections, which is the complaint
 * this layout answers -- 「乱、抓不住重点」.
 *
 * Total and output sit side by side because they tell different stories. The
 * total is nearly all cache reads, so it tracks how much context was re-read;
 * output tracks how much was produced. One chart toggle switches every
 * ranking on the page between the two, so a project's share is always a share
 * of the same thing the chart is drawing.
 *
 * The body is a size container and every responsive rule inside it is a
 * container query (AGENTS.md): this is a dialog, and a viewport breakpoint
 * fires at the window's width rather than the dialog's.
 */
export function TokenUsageView({
  projects,
  projectId,
  onClose,
}: {
  projects: Project[]
  /** The project selected in the sidebar, used as the initial filter. */
  projectId: string | null
  onClose: () => void
}) {
  useLang()
  const [days, setDays] = useState(30)
  const [project, setProject] = useState(projectId ?? '')
  const [tool, setTool] = useState('')
  const [metric, setMetric] = useState<Metric>('total')
  const [data, setData] = useState<TokenUsage | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    let cancelled = false
    let timer = 0
    const tick = async () => {
      try {
        const next = await api.tokenUsage({
          days,
          project: project || undefined,
          tool: tool || undefined,
        })
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
  }, [days, project, tool])

  // Escape closes, because every other full-screen surface here does.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  const refresh = async () => {
    setBusy(true)
    try {
      await api.refreshTokenUsage()
      setData(
        await api.tokenUsage({ days, project: project || undefined, tool: tool || undefined }),
      )
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="vp-backdrop absolute inset-0 z-30 flex items-start justify-center overflow-y-auto bg-black/40 px-3 py-6">
      <div
        data-testid="token-view"
        data-vp-modal="tokens"
        className="vp-panel-in @container w-full max-w-6xl overflow-hidden rounded-vp-lg border border-hairline bg-surface shadow-xl"
      >
        <header className="flex items-center gap-2 border-b border-hairline px-5 py-3">
          <h2 className="flex-1 truncate text-vp-lg font-semibold tracking-tight text-ink">
            {t('spend.title')}
          </h2>
          <button
            type="button"
            data-testid="token-view-refresh"
            onClick={() => void refresh()}
            disabled={busy}
            title={busy ? t('spend.refreshing') : t('spend.refresh')}
            aria-label={busy ? t('spend.refreshing') : t('spend.refresh')}
            className="vp-control disabled:opacity-50"
          >
            <RefreshCw size={14} className={busy || data?.scanning ? 'animate-spin' : ''} />
          </button>
          <button
            type="button"
            onClick={onClose}
            title={t('spend.close')}
            aria-label={t('spend.close')}
            data-testid="token-view-close"
            className="vp-control"
          >
            <X size={15} />
          </button>
        </header>

        <Filters
          projects={projects}
          project={project}
          onProject={setProject}
          tool={tool}
          onTool={setTool}
          days={days}
          onDays={setDays}
        />

        <div className="flex flex-col gap-3 bg-surface-2 px-3 py-4 @xl:px-5">
          {error && <Warning text={error} />}
          {!data ? (
            <p className="py-16 text-center text-vp-base text-ink-2">{t('spend.scanning')}</p>
          ) : (
            <Body data={data} days={days} metric={metric} onMetric={setMetric} />
          )}
        </div>
      </div>
    </div>
  )
}

function Filters(props: {
  projects: Project[]
  project: string
  onProject: (v: string) => void
  tool: string
  onTool: (v: string) => void
  days: number
  onDays: (v: number) => void
}) {
  const select =
    'h-7 min-w-0 max-w-[14rem] truncate rounded-md border border-hairline bg-surface px-2 text-vp-sm text-ink'
  return (
    <div
      className="flex flex-wrap items-center gap-x-3 gap-y-2 border-b border-hairline px-5 py-2.5"
      data-testid="token-filters"
    >
      <div className="vp-segmented w-fit" role="group" aria-label={t('spend.filterRange')}>
        {RANGES.map((n) => (
          <button
            key={n}
            type="button"
            data-testid={`token-range-${n}`}
            aria-pressed={props.days === n}
            data-active={props.days === n}
            onClick={() => props.onDays(n)}
            className="vp-tab whitespace-nowrap px-3 text-vp-sm"
          >
            {n === 365 ? t('spend.rangeYear') : t('spend.rangeShort', { n })}
          </button>
        ))}
      </div>

      <label className="flex min-w-0 items-center gap-1.5 text-vp-sm text-ink-2">
        {t('spend.filterProject')}
        <select
          data-testid="token-filter-project"
          className={select}
          value={props.project}
          onChange={(e) => props.onProject(e.target.value)}
        >
          <option value="">{t('spend.all')}</option>
          {props.projects.map((p) => (
            <option key={p.id} value={p.id}>
              {safeText(p.name)}
            </option>
          ))}
        </select>
      </label>

      <label className="flex min-w-0 items-center gap-1.5 text-vp-sm text-ink-2">
        {t('spend.filterTool')}
        <select
          data-testid="token-filter-tool"
          className={select}
          value={props.tool}
          onChange={(e) => props.onTool(e.target.value)}
        >
          <option value="">{t('spend.all')}</option>
          {/* Product names, not prose, so not in the dictionary. */}
          <option value="claude">Claude Code</option>
          <option value="codex">Codex</option>
          <option value="opencode">opencode</option>
        </select>
      </label>
    </div>
  )
}

function Body({
  data,
  days,
  metric,
  onMetric,
}: {
  data: TokenUsage
  days: number
  metric: Metric
  onMetric: (m: Metric) => void
}) {
  const known = data.scannedAt > 0
  const missing = data.sources.filter((s) => !s.found)
  const skipped = data.sources.reduce((n, s) => n + s.skipped, 0)

  if (!known) {
    return (
      <p className="py-16 text-center text-vp-base text-ink-2">
        {data.scanning ? t('spend.scanning') : t('spend.neverScanned')}
      </p>
    )
  }

  const projects = rank(
    data.projects.map((p) => ({
      key: p.id || ' none',
      label: p.name || t('spend.notInAProject'),
      hint: p.id ? p.path : '',
      t: p,
    })),
    metric,
  )
  const models = rank(
    data.byModel.map((m) => ({
      key: m.model,
      label: m.model || t('spend.unknownModel'),
      hint: '',
      t: m,
    })),
    metric,
  )
  const tools = rank(
    data.byTool.map((x) => ({ key: x.tool, label: x.tool, hint: '', t: x })),
    metric,
  )

  return (
    <>
      {missing.map((s) => (
        <Warning
          key={s.tool}
          text={t('spend.sourceMissing', { tool: s.tool, why: s.problem || '?' })}
        />
      ))}
      {skipped > 0 && <Warning text={t('spend.lowerBound', { n: exact(skipped) })} />}
      {data.passError !== '' && <Warning text={t('spend.passError', { why: data.passError })} />}

      {/* Three across only when each card has room for its legend; between
          that and a phone, the two figures pair up and the mix takes a row. */}
      <div className="grid gap-3 @xl:grid-cols-2 @5xl:grid-cols-3">
        <Stat data={data} days={days} metric="total" label="spend.totalLabel" />
        <Stat data={data} days={days} metric="output" label="spend.output" />
        <Mix totals={data.total} className="@xl:col-span-2 @5xl:col-span-1" />
      </div>

      <Trend data={data} days={days} metric={metric} onMetric={onMetric} />

      {/* Projects down the left, models and tools stacked on the right: the
          tool list is two or three rows, and a card of its own the height of
          the project list was mostly empty. */}
      {/* A project filter leaves one project row, which is not a ranking; the
          card goes and the other two share the row. */}
      <div className="grid gap-3 @3xl:grid-cols-2">
        {projects.length > 1 && (
          <Ranking
            title={t('spend.projects')}
            rows={projects}
            metric={metric}
            testid="projects"
            className="@3xl:row-span-2"
            limit={RANK_ROWS_TALL}
          />
        )}
        <Ranking title={t('spend.models')} rows={models} metric={metric} testid="models" />
        <Ranking title={t('spend.tools')} rows={tools} metric={metric} testid="tools" />
      </div>

      {days >= 365 && <Heatmap data={data} />}

      <Sessions data={data} />
    </>
  )
}

function Card({
  children,
  className,
  testid,
}: {
  children: React.ReactNode
  className?: string
  testid?: string
}) {
  return (
    <section
      data-testid={testid}
      className={`min-w-0 rounded-vp border border-hairline bg-surface px-4 py-3 ${className ?? ''}`}
    >
      {children}
    </section>
  )
}

function CardTitle({ children, aside }: { children: React.ReactNode; aside?: React.ReactNode }) {
  return (
    <div className="mb-2 flex min-h-7 items-center gap-2">
      <h3 className="flex-1 truncate text-vp-sm font-medium text-ink-2">{children}</h3>
      {aside}
    </div>
  )
}

/**
 * Today's figure for one metric, with its baseline.
 *
 * A number this size means nothing on its own, so the line under it is the
 * comparison: the average finished day in the range, and today against it.
 */
function Stat({
  data,
  days,
  metric,
  label,
}: {
  data: TokenUsage
  days: number
  metric: Metric
  label: Key
}) {
  const points = dayPoints(data.byDay, data.today, days, metric)
  const today = points.find((p) => p.day === data.today)?.value ?? 0
  const p = pace(points, data.today)
  const range = windowValue(data.byDay, data.today, days, metric)
  return (
    <Card testid={`spend-stat-${metric}`}>
      <CardTitle
        aside={
          <span className="tabular text-vp-sm text-ink-2" title={exact(range)}>
            {t('spend.rangeValue', { n: days, v: compact(range) })}
          </span>
        }
      >
        {t(label)}
      </CardTitle>
      <div className="flex items-baseline gap-2">
        <span
          data-testid="spend-stat-today"
          className="tabular text-vp-2xl font-semibold tracking-tight text-ink"
          title={exact(today)}
        >
          {compact(today)}
        </span>
        <span className="text-vp-sm text-ink-2">{t('spend.todayShort')}</span>
      </div>
      <p className="tabular mt-1 text-vp-sm text-ink-2" data-testid="spend-ratio">
        {p === null
          ? t('spend.noBaseline')
          : t('spend.pace', {
              avg: compact(Math.round(p.average)),
              x: p.ratio >= 10 ? p.ratio.toFixed(0) : p.ratio.toFixed(1),
            })}
      </p>
    </Card>
  )
}

/**
 * What the range's tokens were, as one divided bar.
 *
 * This is the card that explains why the total and the output differ by two
 * orders of magnitude, without a sentence: the cache-read segment is nearly
 * the whole bar. Every segment is named with its figure in the legend, so the
 * colours are not carrying it (red line 4).
 */
const MIX: { key: keyof Omit<UsageTotals, 'requests'>; label: Key; tone: string }[] = [
  { key: 'cacheRead', label: 'spend.cacheRead', tone: 'var(--vp-state-dead)' },
  { key: 'cacheWrite', label: 'spend.cacheWrite', tone: 'var(--vp-hairline-strong)' },
  { key: 'input', label: 'spend.input', tone: 'var(--vp-state-done)' },
  { key: 'output', label: 'spend.output', tone: 'var(--vp-accent)' },
]

function Mix({ totals, className }: { totals: UsageTotals; className?: string }) {
  const sum = totalOf(totals)
  const pct = (v: number) => (sum > 0 ? (v / sum) * 100 : 0)
  return (
    <Card testid="spend-mix" className={className}>
      <CardTitle
        aside={
          <span className="tabular text-vp-sm text-ink-2">
            {t('spend.requestsShort', { n: compact(totals.requests) })}
          </span>
        }
      >
        {t('spend.breakdown')}
      </CardTitle>
      <div className="vp-bar flex h-2.5">
        {MIX.map((m) =>
          totals[m.key] > 0 ? (
            <span
              key={m.key}
              style={{ width: `${Math.max(0.5, pct(totals[m.key]))}%`, background: m.tone }}
              title={`${t(m.label)} ${exact(totals[m.key])}`}
            />
          ) : null,
        )}
      </div>
      <ul className="mt-3 grid grid-cols-2 gap-x-4 gap-y-1.5">
        {MIX.map((m) => (
          <li key={m.key} className="flex min-w-0 items-center gap-1.5 text-vp-sm">
            <span
              aria-hidden="true"
              className="h-2 w-2 shrink-0 rounded-full"
              style={{ background: m.tone }}
            />
            <span className="truncate text-ink-2">{t(m.label)}</span>
            <span className="tabular ml-auto text-ink" title={exact(totals[m.key])}>
              {compact(totals[m.key])}
            </span>
          </li>
        ))}
      </ul>
    </Card>
  )
}

/**
 * The range, a bar a day, with dates under it and the average across it.
 *
 * The readout above the chart names the day under the pointer, or today when
 * nothing is hovered, so a bar can be read without a tooltip's delay.
 */
function Trend({
  data,
  days,
  metric,
  onMetric,
}: {
  data: TokenUsage
  days: number
  metric: Metric
  onMetric: (m: Metric) => void
}) {
  // Days before the first reading are cut off the left. A year chart over
  // two months of history was ten months of empty axis and a sliver of bars.
  const points = useMemo(
    () => sinceFirst(dayPoints(data.byDay, data.today, days, metric), 7),
    [data.byDay, data.today, days, metric],
  )
  const [hover, setHover] = useState<number | null>(null)
  const peak = points.reduce((m, p) => Math.max(m, p.value), 0)
  const p = pace(points, data.today)
  const ticks = axisTicks(points.length, days <= 7 ? 7 : 6)
  const shown: DayPoint | undefined = hover !== null ? points[hover] : points[points.length - 1]

  return (
    <Card testid="spend-trend">
      <CardTitle
        aside={
          <div className="vp-segmented w-fit" role="group" aria-label={t('spend.perDayTitle')}>
            {(['total', 'output'] as const).map((m) => (
              <button
                key={m}
                type="button"
                data-testid={`spend-metric-${m}`}
                aria-pressed={metric === m}
                data-active={metric === m}
                onClick={() => onMetric(m)}
                className="vp-tab whitespace-nowrap px-3 text-vp-sm"
              >
                {t(m === 'total' ? 'spend.totalLabel' : 'spend.output')}
              </button>
            ))}
          </div>
        }
      >
        {t('spend.perDayTitle')}
      </CardTitle>

      <p className="tabular mb-2 text-vp-sm text-ink-2" data-testid="spend-trend-readout">
        {shown ? (
          <>
            <span className="text-ink">{shortDay(shown.day)}</span>
            {'  '}
            <span className="text-vp-md font-medium text-ink">{compact(shown.value)}</span>
          </>
        ) : null}
      </p>

      {peak === 0 ? (
        <p className="py-10 text-center text-vp-base text-ink-2">{t('spend.noData')}</p>
      ) : (
        <>
          <div
            className="relative flex h-36 items-end gap-[2px]"
            onMouseLeave={() => setHover(null)}
            data-testid="spend-trend-bars"
          >
            {p !== null && (
              <div
                aria-hidden="true"
                className="pointer-events-none absolute inset-x-0 border-t border-dashed border-hairline-strong"
                style={{ bottom: `${Math.min(100, (p.average / peak) * 100)}%` }}
              />
            )}
            {points.map((pt, i) => {
              const today = pt.day === data.today
              const active = hover === null ? today : hover === i
              return (
                <div
                  key={pt.day}
                  role="img"
                  aria-label={`${pt.day} ${exact(pt.value)}`}
                  title={`${pt.day} · ${exact(pt.value)}`}
                  onMouseEnter={() => setHover(i)}
                  className="relative flex h-full min-w-0 flex-1 items-end"
                >
                  <span
                    className="w-full transition-opacity duration-150"
                    style={{
                      height: pt.value > 0 ? `${Math.max(1.5, (pt.value / peak) * 100)}%` : '1px',
                      background: pt.value > 0 ? 'var(--vp-accent)' : 'var(--vp-hairline)',
                      opacity: pt.value === 0 ? 1 : active ? 1 : 0.45,
                    }}
                  />
                </div>
              )
            })}
          </div>
          <div className="relative mt-1.5 h-4 text-vp-xs text-ink-2" aria-hidden="true">
            {ticks.map((i) => {
              const left = points.length > 1 ? (i / (points.length - 1)) * 100 : 0
              const edge = i === 0 ? 'translate-x-0' : i === points.length - 1 ? '-translate-x-full' : '-translate-x-1/2'
              return (
                <span key={i} className={`tabular absolute top-0 ${edge}`} style={{ left: `${left}%` }}>
                  {shortDay(points[i].day)}
                </span>
              )
            })}
          </div>
        </>
      )}
    </Card>
  )
}

/**
 * A ranked list, a bar per row.
 *
 * The bar is the share and the number is beside it, so reading 9.7B against
 * 6.4B is not arithmetic. Six rows and the rest on request: the tail of a
 * ranking is the part nobody came for.
 */
function Ranking({
  title,
  rows,
  metric,
  testid,
  className,
  limit = RANK_ROWS,
}: {
  title: string
  rows: Ranked[]
  metric: Metric
  testid: string
  className?: string
  limit?: number
}) {
  const [all, setAll] = useState(false)
  const shown = all ? rows : rows.slice(0, limit)
  return (
    <Card testid={`spend-rank-${testid}`} className={className}>
      <CardTitle>{title}</CardTitle>
      {rows.length === 0 ? (
        <Empty />
      ) : (
        <ul className="flex flex-col gap-2.5" data-testid="spend-ranking">
          {shown.map((r) => {
            const v = metric === 'output' ? r.output : r.total
            return (
              <li key={r.key} data-testid="spend-rank-row" className="min-w-0">
                <div className="flex items-baseline gap-2">
                  <span
                    className="min-w-0 flex-1 truncate text-vp-base text-ink"
                    title={safeText(r.hint || r.label)}
                  >
                    {safeText(r.label)}
                  </span>
                  <span className="tabular shrink-0 text-vp-base text-ink" title={exact(v)}>
                    {compact(v)}
                  </span>
                  <span className="tabular w-9 shrink-0 text-right text-vp-sm text-ink-2">
                    {Math.round(r.share * 100)}%
                  </span>
                </div>
                <div className="mt-1 h-1.5 overflow-hidden rounded-full bg-surface-2">
                  <div
                    className="h-full rounded-full"
                    style={{ width: `${Math.max(1, r.share * 100)}%`, background: 'var(--vp-accent)' }}
                  />
                </div>
              </li>
            )
          })}
        </ul>
      )}
      {rows.length > limit && (
        <button
          type="button"
          onClick={() => setAll((v) => !v)}
          className="mt-3 text-vp-sm text-accent hover:underline"
        >
          {all ? t('spend.showLess') : t('spend.showAll', { n: rows.length })}
        </button>
      )}
    </Card>
  )
}

function Sessions({ data }: { data: TokenUsage }) {
  const [all, setAll] = useState(false)
  const rows = all ? data.sessions : data.sessions.slice(0, SESSION_ROWS)
  const th = 'py-1.5 pr-3 font-normal'
  return (
    <Card testid="spend-sessions">
      <CardTitle
        aside={
          <span className="tabular text-vp-sm text-ink-2">
            {t('spend.sessionCount', { n: exact(data.sessionCount) })}
          </span>
        }
      >
        {t('spend.sessions')}
      </CardTitle>
      {data.sessions.length === 0 ? (
        <Empty />
      ) : (
        <>
          {/* Wider than a phone, so it scrolls in its own box; the page never
              scrolls sideways. */}
          <div className="-mx-4 overflow-x-auto px-4">
            <table className="w-full min-w-[40rem] table-fixed text-vp-base">
              <colgroup>
                <col className="w-[26%]" />
                <col className="w-[24%]" />
                <col className="w-[14%]" />
                <col className="w-[12%]" />
                <col className="w-[12%]" />
                <col className="w-[12%]" />
              </colgroup>
              <thead>
                <tr className="border-b border-hairline text-left text-vp-sm text-ink-2">
                  <th className={th}>{t('spend.directory')}</th>
                  <th className={th}>{t('spend.model')}</th>
                  <th className={th}>{t('spend.lastSeen')}</th>
                  <th className={`${th} text-right`}>{t('spend.requests')}</th>
                  <th className={`${th} text-right`}>{t('spend.output')}</th>
                  <th className="py-1.5 text-right font-normal">{t('spend.totalLabel')}</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((s) => (
                  <tr
                    key={`${s.tool}:${s.session}`}
                    className="border-b border-hairline last:border-0"
                    data-testid="token-session-row"
                  >
                    <td
                      className="max-w-0 truncate py-1.5 pr-3 text-ink"
                      title={`${safeText(s.cwd)} · ${safeText(s.session)}`}
                    >
                      {safeText(s.projectName || lastSegment(s.cwd))}
                    </td>
                    <td
                      className="max-w-0 truncate py-1.5 pr-3 text-vp-sm text-ink-2"
                      title={safeText(s.models)}
                    >
                      {modelsLabel(s.models)}
                    </td>
                    <td className="tabular whitespace-nowrap py-1.5 pr-3 text-vp-sm text-ink-2">
                      {s.lastDay}
                    </td>
                    <td className="tabular py-1.5 pr-3 text-right text-vp-sm text-ink-2">
                      {exact(s.requests)}
                    </td>
                    <td className="tabular py-1.5 pr-3 text-right text-ink-2" title={exact(s.output)}>
                      {compact(s.output)}
                    </td>
                    <td className="tabular py-1.5 text-right text-ink" title={exact(totalOf(s))}>
                      {compact(totalOf(s))}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {data.sessions.length > SESSION_ROWS && (
            <button
              type="button"
              onClick={() => setAll((v) => !v)}
              className="mt-3 text-vp-sm text-accent hover:underline"
            >
              {all ? t('spend.showLess') : t('spend.showAll', { n: data.sessions.length })}
            </button>
          )}
          {data.sessionCount > data.sessions.length && (all || data.sessions.length <= SESSION_ROWS) && (
            <p className="mt-2 text-vp-sm text-ink-2">
              {t('spend.capped', { n: data.sessions.length, total: data.sessionCount })}
            </p>
          )}
        </>
      )}
    </Card>
  )
}

/**
 * The year grid, shown only when a year is being asked about.
 *
 * Colour is not the only carrier: every square has its exact figure on hover
 * and on focus, and a day outside the range read is a dashed outline rather
 * than a lighter fill.
 */
function Heatmap({ data }: { data: TokenUsage }) {
  const grid = useMemo(
    () => weeks(data.heatmap, data.today, HEATMAP_DAYS),
    [data.heatmap, data.today],
  )
  const labels = useMemo(() => monthLabels(grid), [grid])

  return (
    <Card testid="spend-heatmap">
      <CardTitle>{t('spend.heatmap')}</CardTitle>
      <div className="overflow-x-auto pb-1">
        <div className="inline-block min-w-0">
          <div className="flex gap-[3px]">
            {grid.map((_, i) => {
              const label = labels.find((l) => l.index === i)
              return (
                <div key={i} className="w-[11px] text-vp-xs text-ink-2">
                  {label ? String(label.month) : ' '}
                </div>
              )
            })}
          </div>
          <div className="flex gap-[3px]" data-testid="token-heatmap">
            {grid.map((week, i) => (
              <div key={i} className="flex flex-col gap-[3px]">
                {week.cells.map((cell, j) =>
                  cell === null ? (
                    <div key={j} className="h-[11px] w-[11px]" />
                  ) : (
                    <div
                      key={j}
                      tabIndex={0}
                      role="img"
                      data-level={cell.level}
                      aria-label={cellLabel(cell.day, cell.total)}
                      title={cellLabel(cell.day, cell.total)}
                      className="h-[11px] w-[11px] rounded-md outline-offset-1"
                      style={shade(cell.total, cell.level)}
                    />
                  ),
                )}
              </div>
            ))}
          </div>
        </div>
      </div>
      <div className="mt-2 flex items-center gap-1.5 text-vp-sm text-ink-2">
        <span>{t('spend.less')}</span>
        {[0, 1, 2, 3, 4].map((level) => (
          <span
            key={level}
            className="h-[11px] w-[11px] rounded-md"
            style={shade(level === 0 ? 0 : 1, level)}
          />
        ))}
        <span>{t('spend.more')}</span>
      </div>
    </Card>
  )
}

function cellLabel(day: string, total: number | null): string {
  if (total === null) return t('spend.cellOutside', { day })
  if (total === 0) return t('spend.cellNone', { day })
  return t('spend.cellSpent', { day, n: exact(total) })
}

/**
 * A square's fill, from theme tokens so it moves with the theme. Opacity
 * carries the five steps: one hue, one scale.
 */
function shade(total: number | null, level: number): React.CSSProperties {
  if (total === null) {
    return { background: 'transparent', border: '1px dashed var(--vp-hairline)' }
  }
  if (level === 0) return { background: 'var(--vp-surface-2)' }
  return { background: 'var(--vp-accent)', opacity: 0.25 + level * 0.1875 }
}

/** The last directory of a path: the part that tells two rows apart. */
function lastSegment(path: string): string {
  const parts = path.split('/').filter(Boolean)
  return parts.length > 0 ? parts[parts.length - 1] : path
}

/** A session's models, as the first and a count of the rest. */
function modelsLabel(models: string): string {
  if (!models) return t('spend.unknownModel')
  const list = models.split(',').filter(Boolean)
  const first = safeText(list[0] ?? '')
  return list.length > 1 ? `${first} +${list.length - 1}` : first
}

function Empty() {
  return <p className="py-2 text-vp-base text-ink-2">{t('spend.noData')}</p>
}

function Warning({ text }: { text: string }) {
  return (
    <p
      className="flex items-start gap-1.5 rounded-vp border border-hairline bg-surface px-3 py-2 text-vp-base leading-relaxed"
      style={{ color: 'var(--vp-state-waiting)' }}
    >
      <AlertTriangle size={13} className="mt-1 shrink-0" />
      <span>{safeText(text)}</span>
    </p>
  )
}
