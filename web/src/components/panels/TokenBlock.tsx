import type { TokenUsage as Usage } from '../../protocol/wire'
import { t, useLang } from '../../i18n'
import type { PanelDensity } from '../chrome'
import { safeText } from '../text'
import { formatAgo, spendIsStale } from './ago'
import { Spark } from '../spark'
import { compact, exact } from './tokens'
import {
  dayTotal,
  daySeries,
  outputTotal,
  projectTotal,
  toolShares,
  windowTotal,
} from './spend'
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
 * What the agents are costing, in the height of about six lines.
 *
 * 「几个数字 有布局（本周消耗、本项目消耗、今日消耗、分应用消耗、时间、字数）
 * 好看一点」 — and the layout is the whole of it, because the six are not six
 * equal things and a three-by-two grid of identical cards says they are.
 *
 *   今日消耗    the hero. It is the figure somebody glances at ten times a day
 *              and the only one whose answer changes while you watch. First,
 *              largest, and on its own line at every width.
 *   本周 / 本项目  context for the hero, and a pair rather than two cards: they
 *              are read against it and against each other, so they are the
 *              same size as each other and smaller than it.
 *   分应用消耗  not a number at all. Three totals in a column is arithmetic the
 *              reader has to do; one bar divided three ways is the same fact
 *              already done. It reuses `.vp-bar`, so it is the same object as
 *              every meter in the monitor below it.
 *   时间 / 字数  qualifiers. They say what the five figures above mean rather
 *              than adding a sixth, so they are a quiet footer line and not
 *              cards. See the module comment in spend.ts for what each of them
 *              is a reading of.
 *
 * The hierarchy is ratio, not a new type size. The scale tops out at
 * `text-vp-lg` for the panel on purpose — `text-vp-xl` and up exist for a
 * dashboard read from across a room — so the hero is `lg`, the pair is `md`,
 * and everything else is `xs`. Three steps is enough to rank three ranks.
 */
export function TokenBlock({
  data,
  projectId,
  projectName,
  span,
  density,
  now,
}: {
  data: Usage
  projectId: string | null
  projectName: string | null
  /** Days the figures cover. Stated in the footer, never left to be guessed. */
  span: number
  density: PanelDensity
  /** One clock for the whole panel; see the monitor for why it is a prop. */
  now: number
}) {
  useLang()

  // Never read is not zero, and the difference is the whole feature. Until a
  // pass has finished there is no figure to show at all — so every one of them
  // is null rather than the arithmetic's honest 0.
  const known = data.scannedAt > 0
  const today = known ? dayTotal(data.byDay, data.today) : null
  const week = known ? windowTotal(data.byDay, data.today, 7) : null
  const project = known ? projectTotal(data, projectId) : null
  const output = known ? outputTotal(data.byDay, data.today, span) : null
  const tools = known ? toolShares(data) : []
  // Fourteen days rather than the thirty the footer names. A fortnight is what
  // fits legibly in a strip this wide -- thirty points across 120px is two
  // pixels each and reads as texture -- and it is the window over which "is
  // today unusual" is answerable by looking.
  const series = known ? daySeries(data.byDay, data.today, 14) : []
  const seriesMax = Math.max(1, ...series)

  return (
    <div className="px-3 py-2" data-testid="token-block">
      {/* One row above 380px, two below. The hero keeps its own line either
          way: it is the answer, and an answer that has to share a line with
          its own context is an answer somebody has to look for. */}
      <div
        className={`grid gap-x-4 gap-y-1 ${
          density === 'wide' ? 'grid-cols-[1.4fr_1fr_1fr]' : 'grid-cols-2'
        }`}
      >
        {/* The hero with its trend on the same line.

            The block was four figures and a bar in 275x165 and the complaint
            was that it looks empty -- which it was: every figure was one number
            with no way to tell an ordinary day from a remarkable one. Thirty
            days of the series were already on the payload and nothing drew
            any of it. */}
        <div className={`flex items-end gap-2 ${density === 'wide' ? '' : 'col-span-2'}`}>
          <Figure label={t('spend.today')} value={today} rank="hero" />
          {series.length > 1 && (
            <div
              className="h-7 min-w-0 flex-1 pb-1 opacity-80"
              data-testid="token-spark"
              title={t('spend.sparkDays', { n: series.length })}
            >
              <Spark
                values={series}
                max={seriesMax}
                tone="var(--vp-accent)"
                testid="token-spark-svg"
              />
            </div>
          )}
        </div>
        <Figure label={t('spend.week')} value={week} rank="pair" />
        <Figure
          label={t('spend.thisProject')}
          value={project}
          rank="pair"
          // Which project, so the figure is not a total wearing a scope. With
          // no project selected the label says so rather than the number
          // quietly meaning something else.
          note={projectName ?? t('spend.noProject')}
        />
      </div>

      {tools.length > 0 && <ToolBar tools={tools} />}

      {/* The qualifiers, in the order they qualify: how much was produced, over
          what period, as of when. Every figure above is a lower bound if the
          reader has not been told when it was measured. */}
      <p
        data-testid="token-block-footer"
        className="tabular mt-1.5 truncate text-vp-xs text-ink-2"
        title={output === null ? undefined : `${exact(output)} ${t('spend.tokens')}`}
      >
        {t('spend.output')} {output === null ? '—' : compact(output)}
        {' · '}
        {/* Requests, because it is the other axis of the same story: the same
            spend over ten requests and over ten thousand are different days,
            and the payload has carried the number all along. */}
        {t('spend.requestsShort', { n: compact(data.total.requests) })}
        {' · '}
        {t('spend.rangeDays', { n: span })}
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

/**
 * One figure and its label, at one of two ranks.
 *
 * `null` is an em dash and never a zero. There is no formatting trick that
 * makes a zero mean "not known", so it does not get to try — the same rule the
 * meters follow.
 */
function Figure({
  label,
  value,
  rank,
  note,
  className,
}: {
  label: string
  value: number | null
  rank: 'hero' | 'pair'
  note?: string
  className?: string
}) {
  const hero = rank === 'hero'
  return (
    <div className={`min-w-0 ${className ?? ''}`}>
      <div className="truncate text-vp-xs text-ink-2">{label}</div>
      {/* Named, because the thing being asserted about this block is its
          *hierarchy* and a check that finds the figures by walking the DOM
          finds whatever else happens to be laid out like one. `data-rank` is
          what the browser checks measure font sizes against. */}
      <div
        data-testid="spend-figure"
        data-rank={rank}
        className={`tabular truncate ${hero ? 'text-vp-lg text-ink' : 'text-vp-md text-ink'}`}
        title={value === null ? undefined : `${exact(value)} tokens`}
      >
        {value === null ? '—' : compact(value)}
      </div>
      {note !== undefined && (
        <div className="truncate text-vp-xs text-ink-2" title={note}>
          {safeText(note)}
        </div>
      )}
    </div>
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
