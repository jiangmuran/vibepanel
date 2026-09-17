import type { TokenUsage as Usage } from '../../protocol/wire'
import { t, useLang } from '../../i18n'
import type { PanelDensity } from '../chrome'
import { safeText } from '../text'
import { formatAgo, spendIsStale } from './ago'
import { Spark } from '../spark'
import { compact, exact, totalOf } from './tokens'
import { dayValue, daySeries, projectFigures, toolShares, windowValue } from './spend'
import type { ToolShare } from './spend'

/**
 * What one segment of the share bar is made of.
 *
 * On hover rather than on the bar, because the bar has room for a proportion
 * and nothing else. What it buys is that the proportion can be checked: the
 * total is 96-99% cache reads on the machine this was written on, so 82/18 by
 * tokens is 73/27 by output, and somebody comparing two agents deserves to
 * find that out without exporting anything.
 */
function toolTitle(x: ToolShare): string {
  return t('spend.toolTitle', {
    tool: x.tool,
    total: exact(x.total),
    output: exact(x.output),
    cache: exact(x.cacheRead),
  })
}

/**
 * What the agents are costing, in the corner of the eye.
 *
 * A small table: today, this week and (when one is selected) this project down
 * the side, total and output across the top. The two columns are the two
 * readings that tell different stories -- the total is nearly all cache reads
 * and moves with how much context was re-read, the output moves with how much
 * was produced -- and a column each keeps them from being read as one number.
 * Today's row is the larger size; everything else is one step down.
 *
 * Under it, a fortnight of the total as a trend and the split by agent.
 * Nothing here is pressable: the header row above opens the full view.
 */
export function TokenBlock({
  data,
  projectId,
  projectName,
  span,
  now,
}: {
  data: Usage
  projectId: string | null
  projectName: string | null
  /** Days the project row and the footer cover. Stated, never guessed. */
  span: number
  density: PanelDensity
  /** One clock for the whole panel; see the monitor for why it is a prop. */
  now: number
}) {
  useLang()

  // Never read is not zero. Until a pass has finished every figure is null.
  const known = data.scannedAt > 0
  const at = (v: number) => (known ? v : null)
  const project = projectFigures(data, projectId)
  const tools = known ? toolShares(data) : []
  // A fortnight: thirty points across a column this narrow read as texture.
  const series = known ? daySeries(data.byDay, data.today, 14) : []
  const seriesMax = Math.max(1, ...series)

  const head = 'truncate text-vp-xs text-ink-2'
  const side = 'truncate text-vp-xs text-ink-2'
  return (
    <div className="px-3 pb-2.5 pt-1" data-testid="token-block">
      <div className="grid grid-cols-[minmax(2.5rem,auto)_1fr_1fr] items-baseline gap-x-3 gap-y-0.5">
        <span />
        <span className={head}>{t('spend.totalLabel')}</span>
        <span className={head}>{t('spend.output')}</span>

        <span className={side}>{t('spend.todayShort')}</span>
        <Figure value={at(dayValue(data.byDay, data.today, 'total'))} rank="hero" />
        <Figure value={at(dayValue(data.byDay, data.today, 'output'))} rank="hero" />

        <span className={side}>{t('spend.week')}</span>
        <Figure value={at(windowValue(data.byDay, data.today, 7, 'total'))} rank="pair" />
        <Figure value={at(windowValue(data.byDay, data.today, 7, 'output'))} rank="pair" />

        {projectId && (
          <>
            <span
              className={`${side} max-w-[6rem]`}
              title={`${projectName ?? ''} · ${t('spend.rangeDays', { n: span })}`}
            >
              {safeText(projectName ?? t('spend.thisProject'))}
            </span>
            <Figure value={project && known ? project.total : null} rank="pair" />
            <Figure value={project && known ? project.output : null} rank="pair" />
          </>
        )}
      </div>

      {series.length > 1 && (
        <div
          className="mt-2 h-7 opacity-80"
          data-testid="token-spark"
          title={t('spend.sparkDays', { n: series.length })}
        >
          <Spark values={series} max={seriesMax} tone="var(--vp-accent)" testid="token-spark-svg" />
        </div>
      )}

      {tools.length > 0 && <ToolBar tools={tools} />}

      <p
        data-testid="token-block-footer"
        className="tabular mt-2 truncate text-vp-xs text-ink-2"
        title={`${exact(totalOf(data.total))} ${t('spend.tokens')}`}
      >
        {t('spend.rangeDays', { n: span })} {compact(totalOf(data.total))}
        {' · '}
        {t('spend.outputShort', { v: compact(data.total.output) })}
        {' · '}
        {t('spend.requestsShort', { n: compact(data.total.requests) })}
        {spendIsStale(data.scannedAt, now) && (
          <> · {t('spend.scannedAgo', { ago: formatAgo(data.scannedAt, now) })}</>
        )}
      </p>

      {!known && (
        <p className="mt-1 text-vp-sm leading-relaxed text-ink-2">
          {data.scanning ? t('spend.scanning') : t('spend.neverScanned')}
        </p>
      )}
    </div>
  )
}

/** One figure, at one of two ranks. `null` is an em dash and never a zero. */
function Figure({ value, rank }: { value: number | null; rank: 'hero' | 'pair' }) {
  return (
    // `data-rank` is what the browser checks measure font sizes against.
    <span
      data-testid="spend-figure"
      data-rank={rank}
      className={`tabular min-w-0 truncate text-ink ${
        rank === 'hero' ? 'text-vp-xl font-semibold tracking-tight' : 'text-vp-md'
      }`}
      title={value === null ? undefined : `${exact(value)} ${t('spend.tokens')}`}
    >
      {value === null ? '—' : compact(value)}
    </span>
  )
}

/**
 * Who spent it, as one bar rather than three numbers.
 *
 * Colour is not carrying this (red line 4). The order is largest first and the
 * legend lists the same order with a percentage against each, so the bar can be
 * read by length and the legend by words; a segment wide enough to hold its own
 * name carries it inside as well. In a dark room at 2am the words are the part
 * that still works.
 *
 * The hues are the accent and the two state colours that are *not* used for
 * urgency in this panel, so a busy agent never looks like a warning.
 */
const TOOL_TONES = [
  'var(--vp-accent)',
  'var(--vp-state-done)',
  'var(--vp-state-working)',
  'var(--vp-state-dead)',
]

// The bar is a bar. The legend under it names the segments.
//
// The segments used to carry their own labels -- white on the accent inside a
// ten-pixel-tall bar, which render-check measured at 4.02:1 against a required
// 4.5 in light and 3.65:1 in dark. That is the same tool name the legend below
// already prints beside a colour dot, at a legible size, so the failing copy
// was duplicate information nobody could read.
//
// Nothing here depends on colour alone (red line 4): every segment is named in
// the legend, and its width is the figure.
function ToolBar({ tools }: { tools: ToolShare[] }) {
  return (
    <div className="mt-2 flex items-center gap-2" data-testid="token-tools">
      {/* The bar and its legend on one line.

          They were two rows for two agents, and the second row said
          "claude 89% codex 11%" beside a bar already drawn 89/11 -- the same
          fact twice, in a block whose complaint was that it looks empty. The
          bar keeps enough width to be a proportion rather than a decoration;
          the legend takes what is left and wraps under it only when there are
          enough agents that it must. */}
      <div className="vp-bar flex h-2.5 min-w-16 flex-1">
        {tools.map((x, i) => (
          <span
            key={x.tool}
            data-testid="token-tool-seg"
            title={toolTitle(x)}
            className="overflow-hidden"
            style={{
              width: `${x.share * 100}%`,
              background: TOOL_TONES[i % TOOL_TONES.length],
            }}
          />
        ))}
      </div>
      <div className="flex flex-wrap items-center justify-end gap-x-2.5 gap-y-0.5">
        {tools.map((x, i) => (
          <span
            key={x.tool}
            data-testid="token-tool-key"
            className="tabular inline-flex min-w-0 shrink items-center gap-1 text-vp-xs text-ink-2"
          >
            <span
              aria-hidden="true"
              className="h-2 w-2 shrink-0 rounded-full"
              style={{ background: TOOL_TONES[i % TOOL_TONES.length] }}
            />
            <span className="truncate">{safeText(x.tool)}</span>
            <span>{Math.round(x.share * 100)}%</span>
          </span>
        ))}
      </div>
    </div>
  )
}
