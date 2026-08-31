import { useEffect, useMemo, useState } from 'react'
import { AlertTriangle, RefreshCw, X } from 'lucide-react'

import { api } from '../protocol/api'
import type { Project, TokenUsage } from '../protocol/wire'
import { t, useLang } from '../i18n'
import { safeText } from './text'
import { compact, exact, monthLabels, totalOf, weeks } from './panels/tokens'

/** The range control's positions, in days. */
const RANGES = [7, 30, 90, 365]

/** How far back the year grid reaches. Mirrors heatmapDays in internal/httpapi. */
const HEATMAP_DAYS = 371

/** How often the view refreshes while it is open. */
const POLL_MS = 20000

/**
 * The whole picture: a year grid, the filters, and four tables.
 *
 * A full-width overlay rather than a fifth thing crammed into the side panel.
 * The panel is 280 pixels by default; a 53-week heatmap needs about 580 before
 * a day-of-week gutter, and the per-session table has six numeric columns. The
 * side panel keeps the glance — today, the range, and why a figure might be
 * missing — and this holds the analysis.
 *
 * Modelled on the Settings overlay so there is one way to open a full-screen
 * surface in this product rather than two.
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

  // Escape closes, because every other full-screen surface here does and a
  // reader who has just pressed it twice on the settings dialog expects it.
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
    <div className="vp-backdrop absolute inset-0 z-30 flex items-start justify-center overflow-y-auto bg-black/40 px-4 py-8">
      <div
        data-testid="token-view"
        data-vp-modal="tokens"
        className="vp-panel-in w-full max-w-5xl rounded-vp-lg border border-hairline bg-surface p-6 shadow-xl"
      >
        <div className="mb-4 flex items-center gap-2">
          <h2 className="flex-1 text-vp-lg font-semibold tracking-tight text-ink">
            {t('spend.title')}
          </h2>
          <button
            type="button"
            data-testid="token-view-refresh"
            onClick={() => void refresh()}
            disabled={busy}
            title={busy ? t('spend.refreshing') : t('spend.refresh')}
            className="vp-control disabled:opacity-50"
          >
            <RefreshCw size={14} className={busy || data?.scanning ? 'animate-spin' : ''} />
          </button>
          <button
            type="button"
            onClick={onClose}
            title={t('spend.close')}
            data-testid="token-view-close"
            className="vp-control"
          >
            <X size={15} />
          </button>
        </div>

        {/* Before any number, not after it. What these figures are is not a
            footnote: a reader who takes them for the panel's own accounting
            has misread every row below. */}
        <p className="mb-4 text-vp-base leading-relaxed text-ink-2">{t('spend.whose')}</p>

        {error && (
          <p className="mb-4 text-vp-base" style={{ color: 'var(--vp-state-waiting)' }}>
            {safeText(error)}
          </p>
        )}

        <Filters
          projects={projects}
          project={project}
          onProject={setProject}
          tool={tool}
          onTool={setTool}
          days={days}
          onDays={setDays}
        />

        {!data ? (
          <p className="py-8 text-center text-vp-base text-ink-2">{t('spend.scanning')}</p>
        ) : (
          <Body data={data} days={days} />
        )}
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
    'rounded-vp border border-hairline bg-surface-2 px-2 py-1 text-vp-base text-ink'
  return (
    <div className="mb-5 flex flex-wrap items-center gap-3" data-testid="token-filters">
      <label className="flex items-center gap-1.5 text-vp-sm text-ink-2">
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

      <label className="flex items-center gap-1.5 text-vp-sm text-ink-2">
        {t('spend.filterTool')}
        {/* The two agent names are product names, not prose, so they are not
            in the dictionary. "All" is. */}
        <select
          data-testid="token-filter-tool"
          className={select}
          value={props.tool}
          onChange={(e) => props.onTool(e.target.value)}
        >
          <option value="">{t('spend.all')}</option>
          <option value="claude">Claude Code</option>
          <option value="codex">Codex</option>
        </select>
      </label>

      <div className="flex items-center gap-1.5 text-vp-sm text-ink-2">
        {t('spend.filterRange')}
        <div className="vp-segmented w-fit">
          {RANGES.map((n) => (
            <button
              key={n}
              type="button"
              data-testid={`token-range-${n}`}
              // See the language picker in Settings: aria-pressed for the
              // reader, data-active for the sheet.
              aria-pressed={props.days === n}
              data-active={props.days === n}
              onClick={() => props.onDays(n)}
              className="vp-tab text-vp-sm"
            >
              {t('spend.rangeShort', { n })}
            </button>
          ))}
        </div>
      </div>
    </div>
  )
}

function Body({ data, days }: { data: TokenUsage; days: number }) {
  const known = data.scannedAt > 0
  const missing = data.sources.filter((s) => !s.found)
  const skipped = data.sources.reduce((n, s) => n + s.skipped, 0)

  if (!known) {
    return (
      <p className="py-8 text-center text-vp-base text-ink-2">
        {data.scanning ? t('spend.scanning') : t('spend.neverScanned')}
      </p>
    )
  }

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

      <Headline data={data} days={days} />

      <div className="mt-6 grid gap-6 @3xl:grid-cols-2">
        <Ranking
          title={t('spend.whereItWent')}
          rows={data.projects.map((p) => ({
            key: p.id || '\u0000none',
            label: p.name || t('spend.notInAProject'),
            hint: p.id ? p.path : '',
            value: totalOf(p),
          }))}
        />
        <Ranking
          title={t('spend.whatSpentIt')}
          rows={data.byModel.map((m) => ({
            key: m.model,
            label: m.model || t('spend.unknownModel'),
            hint: '',
            value: totalOf(m),
          }))}
        />
      </div>

      {/* The year grid, only when a year is being asked about.
        *
        * It is the largest thing on the page and it was always on. Five weeks
        * of history in a 53-week frame is eleven twelfths blank dots, above
        * the numbers somebody actually came for. */}
      {days >= 365 && <Heatmap data={data} />}

      <Section title={t('spend.sessions')}>
        <p className="mb-2 text-vp-sm leading-relaxed text-ink-3">{t('spend.agentSessionNote')}</p>
        {data.sessions.length === 0 ? (
          <Empty />
        ) : (
          <>
            {/* The table is wider than a phone and scrolls inside its own box;
                the page never scrolls sideways. */}
            <div className="overflow-x-auto">
              <table className="w-full min-w-[40rem] text-vp-base">
                <thead>
                  <tr className="text-left text-vp-sm text-ink-3">
                    <th className="py-1 pr-2 font-normal">{t('spend.directory')}</th>
                    <th className="py-1 pr-2 font-normal">{t('spend.model')}</th>
                    <th className="py-1 pr-2 font-normal">{t('spend.lastSeen')}</th>
                    <th className="py-1 pr-2 text-right font-normal">{t('spend.requests')}</th>
                    <th className="py-1 text-right font-normal">{t('spend.total')}</th>
                  </tr>
                </thead>
                <tbody>
                  {data.sessions.map((s) => (
                    <tr
                      key={`${s.tool}:${s.session}`}
                      className="border-b border-hairline last:border-0"
                      data-testid="token-session-row"
                    >
                      <td
                        className="max-w-0 truncate py-1 pr-2 text-ink"
                        title={`${safeText(s.cwd)} · ${safeText(s.session)}`}
                      >
                        {safeText(s.projectName || s.cwd)}
                      </td>
                      <td className="max-w-0 truncate py-1 pr-2 text-vp-sm text-ink-2">
                        {s.models ? safeText(s.models) : t('spend.unknownModel')}
                      </td>
                      <td className="tabular py-1 pr-2 text-vp-sm text-ink-2">{s.lastDay}</td>
                      <td className="tabular py-1 pr-2 text-right text-vp-sm text-ink-2">
                        {exact(s.requests)}
                      </td>
                      <Cell value={totalOf(s)} />
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            {data.sessionCount > data.sessions.length && (
              <p className="mt-2 text-vp-sm text-ink-3">
                {t('spend.capped', { n: data.sessions.length, total: data.sessionCount })}
              </p>
            )}
          </>
        )}
      </Section>
    </>
  )
}

/**
 * What the board is for, in one line: is today unusual?
 *
 * The old top of this page was a single 32.7B, which answers nothing. A number
 * that size is only readable against something, and the only baseline that
 * needs no price table is the period's own daily average -- so "today 237M,
 * daily average 1.09B, 0.22x" says in three numbers what a thirty-row table
 * said in thirty.
 */
function Headline({ data, days }: { data: TokenUsage; days: number }) {
  const total = totalOf(data.total)
  const today = data.byDay.find((d) => d.day === data.today)
  const todayTotal = today ? totalOf(today) : 0
  // Divided by the days that have numbers, not by the width of the window: a
  // panel installed a week ago has 30 days of window and 7 of history, and
  // dividing by 30 would report it as spending a quarter of what it spends.
  const observed = data.byDay.length || 1
  const average = total / observed
  const ratio = average > 0 ? todayTotal / average : 0

  return (
    <div className="rounded-vp border border-hairline bg-surface-2 px-4 py-3">
      <div className="flex flex-wrap items-baseline gap-x-8 gap-y-2">
        <Figure label={t('spend.today')} value={todayTotal} big />
        <Figure label={t('spend.perDay')} value={Math.round(average)} />
        {average > 0 && (
          <span className="text-vp-base text-ink-2" data-testid="spend-ratio">
            {t('spend.timesAverage', { x: ratio >= 10 ? ratio.toFixed(0) : ratio.toFixed(2) })}
          </span>
        )}
      </div>
      <Trend data={data} />
      <p className="mt-2 text-vp-sm text-ink-3" data-testid="spend-summary">
        {t('spend.summaryLine', {
          n: String(days),
          total: compact(total),
          requests: exact(data.total.requests),
          each: compact(Math.round(data.total.requests ? total / data.total.requests : 0)),
        })}
      </p>
    </div>
  )
}

function Figure({ label, value, big }: { label: string; value: number; big?: boolean }) {
  return (
    <span className="flex items-baseline gap-2">
      <span className="text-vp-sm text-ink-2">{label}</span>
      <span
        className={`tabular text-ink ${big ? 'text-vp-xl font-semibold' : 'text-vp-lg'}`}
        title={exact(value)}
      >
        {compact(value)}
      </span>
    </span>
  )
}

/** The range, as one row of bars. Replaces a thirty-row table of the same
 *  numbers, which is the same data read one line at a time. */
function Trend({ data }: { data: TokenUsage }) {
  const rows = data.byDay
  const peak = rows.reduce((m, d) => Math.max(m, totalOf(d)), 0)
  if (rows.length === 0 || peak === 0) return null
  return (
    <div className="mt-3 flex h-12 items-end gap-px" data-testid="spend-trend">
      {rows.map((d) => {
        const v = totalOf(d)
        return (
          <span
            key={d.day}
            title={`${d.day} · ${compact(v)}`}
            className="min-w-px flex-1"
            style={{
              height: `${Math.max(2, (v / peak) * 100)}%`,
              background: v === 0 ? 'var(--vp-hairline)' : 'var(--vp-accent)',
              opacity: v === 0 ? 1 : 0.55 + 0.45 * (v / peak),
            }}
          />
        )
      })}
    </div>
  )
}

/**
 * A ranked list with a bar per row.
 *
 * Two of these answer the two questions a table of five sections could not:
 * where it went, and what spent it. A share is the point, so the bar is the
 * share and the number is beside it -- reading 26.3B against 6.0B in a column
 * is arithmetic somebody has to do.
 */
function Ranking({
  title,
  rows,
}: {
  title: string
  rows: { key: string; label: string; hint: string; value: number }[]
}) {
  const shown = rows.filter((r) => r.value > 0).sort((a, b) => b.value - a.value)
  const total = shown.reduce((n, r) => n + r.value, 0)
  return (
    <Section title={title}>
      {shown.length === 0 ? (
        <Empty />
      ) : (
        <div className="flex flex-col gap-2" data-testid="spend-ranking">
          {shown.slice(0, 8).map((r) => {
            const share = total > 0 ? r.value / total : 0
            return (
              <div key={r.key} data-testid="spend-rank-row">
                <div className="flex items-baseline gap-2">
                  {/* The name takes the slack and the numbers do not: a value
                      column that floats with the length of the label is a
                      column of digits nobody can compare down. */}
                  <span
                    className="min-w-0 flex-1 truncate text-vp-base text-ink"
                    title={safeText(r.hint || r.label)}
                  >
                    {safeText(r.label)}
                  </span>
                  <span
                    className="tabular w-16 shrink-0 text-right text-vp-base text-ink"
                    title={exact(r.value)}
                  >
                    {compact(r.value)}
                  </span>
                  <span className="tabular w-10 shrink-0 text-right text-vp-sm text-ink-3">
                    {Math.round(share * 100)}%
                  </span>
                </div>
                <div className="mt-1 h-1 rounded-full bg-surface-2">
                  <div
                    className="h-1 rounded-full"
                    style={{ width: `${Math.max(1, share * 100)}%`, background: 'var(--vp-accent)' }}
                  />
                </div>
              </div>
            )
          })}
        </div>
      )}
    </Section>
  )
}


/**
 * The year grid.
 *
 * Colour is not the only carrier: every square has its exact figure on hover
 * and on focus, every square is reachable by keyboard, and the legend says in
 * words what the shades mean. A grid that can only be read by comparing hues
 * is unreadable to a good number of people and useless in a screenshot.
 */
function Heatmap({ data }: { data: TokenUsage }) {
  const grid = useMemo(
    () => weeks(data.heatmap, data.today, HEATMAP_DAYS),
    [data.heatmap, data.today],
  )
  const labels = useMemo(() => monthLabels(grid), [grid])

  return (
    <Section title={t('spend.heatmap')}>
      <div className="overflow-x-auto pb-1">
        <div className="inline-block min-w-0">
          <div className="flex gap-[3px]">
            {grid.map((_, i) => {
              const label = labels.find((l) => l.index === i)
              return (
                <div key={i} className="w-[11px] text-vp-xs text-ink-3">
                  {label ? String(label.month) : ' '}
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
      <div className="mt-2 flex items-center gap-1.5 text-vp-sm text-ink-3">
        <span>{t('spend.less')}</span>
        {[0, 1, 2, 3, 4].map((level) => (
          <span
            key={level}
            className="h-[11px] w-[11px] rounded-md"
            style={shade(level === 0 ? 0 : 1, level)}
          />
        ))}
        <span>{t('spend.more')}</span>
        <span className="ml-2">{t('spend.legend')}</span>
      </div>
    </Section>
  )
}

function cellLabel(day: string, total: number | null): string {
  if (total === null) return t('spend.cellOutside', { day })
  if (total === 0) return t('spend.cellNone', { day })
  return t('spend.cellSpent', { day, n: exact(total) })
}

/**
 * A square's fill.
 *
 * Tokens, not a hard-coded palette: the accent and the surface both move with
 * the theme, and a fixed green here would be the white-on-white failure with
 * an extra step. Opacity carries the five steps so there is one hue and one
 * scale rather than five colours somebody has to keep in agreement.
 *
 * A day the range never covered is drawn as the bare surface with a hairline,
 * which is a different *shape* from a day that was covered and empty — the
 * legend's leftmost square. Colour is not doing that work alone.
 */
function shade(total: number | null, level: number): React.CSSProperties {
  if (total === null) {
    return { background: 'transparent', border: '1px dashed var(--vp-hairline)' }
  }
  if (level === 0) return { background: 'var(--vp-surface-2)' }
  return { background: 'var(--vp-accent)', opacity: 0.25 + level * 0.1875 }
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="mt-6">
      <h3 className="mb-2 text-vp-md font-medium text-ink">{title}</h3>
      {children}
    </section>
  )
}

function Cell({ value }: { value: number | null }) {
  return (
    <td
      className="tabular w-24 py-1 text-right text-ink"
      title={value === null ? undefined : `${exact(value)} ${t('spend.tokens')}`}
    >
      {value === null ? '—' : compact(value)}
    </td>
  )
}

function Empty() {
  return <p className="text-vp-base text-ink-3">{t('spend.noData')}</p>
}

function Warning({ text }: { text: string }) {
  return (
    <p
      className="mb-3 flex items-start gap-1.5 text-vp-base leading-relaxed"
      style={{ color: 'var(--vp-state-waiting)' }}
    >
      <AlertTriangle size={13} className="mt-0.5 shrink-0" />
      <span>{safeText(text)}</span>
    </p>
  )
}

