import { useEffect, useState } from 'react'

import type { ShareDashboard, ShareSession, ShareWidget } from '../../protocol/wire'
import { t } from '../../i18n'
import { rows as rowsAt, useDensity } from './density'
import { StateDot } from '../StateDot'
import { safeText } from '../text'
import { Bar, Empty, Tile } from './Tile'
import { duration } from './format'
import { filterRows, orderRows } from './rows'

/**
 * The widgets a wall is composed *with*, as opposed to the ones it is made of.
 *
 * Two kinds of thing live here. The furniture — a spacer, a rule, a heading,
 * the screen's own name — which exists because a screen that is a solid brick
 * of tiles is crowded rather than filled, and because grouping is the half of
 * composition that gets forgotten. And the strips and tallies that carry the
 * whole panel in one row, which is what lets a hero have the rest of the space.
 *
 * Nothing here reads anything the payload does not already carry. A board can
 * only ever subtract; see internal/store/board.go.
 */

/** The three states, in the order a strip reads them. */
const STATES = ['waiting', 'working', 'done'] as const

/**
 * The state tallies as one proportional strip.
 *
 * Red line 4 twice over. The proportions are the shape, but every segment
 * carries its own count and its own glyph, so the strip survives a photograph
 * of a screen taken at an angle and a reader who cannot separate the hues. A
 * segment too narrow for its number keeps the number outside it rather than
 * dropping it.
 */
export function StateBar({ w, data }: { w: ShareWidget; data: ShareDashboard }) {
  const c = data.counts
  const counts = { waiting: c.waiting, working: c.working, done: c.done }
  const total = counts.waiting + counts.working + counts.done
  return (
    <Tile kind={w.kind} span={w.span} height={w.height} testid="widget-statebar" plain>
      <div className="flex h-4 w-full overflow-hidden rounded-full" data-testid="statebar-track">
        {total === 0 ? (
          <div className="h-full w-full" style={{ background: 'var(--vp-surface-2)' }} />
        ) : (
          STATES.map((s) => (
            <div
              key={s}
              data-testid="statebar-segment"
              data-state={s}
              style={{
                width: `${(counts[s] / total) * 100}%`,
                background: `var(--vp-state-${s})`,
              }}
            />
          ))
        )}
      </div>
      <div className="mt-3 flex flex-wrap gap-x-8 gap-y-1">
        {STATES.map((s) => (
          <span key={s} className="flex items-center gap-2 text-vp-xl text-ink-2">
            <StateDot state={s} size={20} />
            <span className="tabular font-semibold text-ink">{counts[s]}</span>
            {t(`dash.${s}` as 'dash.waiting')}
          </span>
        ))}
      </div>
    </Tile>
  )
}

/**
 * One row that says where the panel is, right now.
 *
 * The cheapest thing that makes a wall read as composed rather than assembled:
 * a hero fills the top, and this closes the bottom with everything the hero
 * left out. Deliberately flat — no surface, no borders — because a strip with a
 * box round it is another card.
 */
export function NowStrip({ w, data }: { w: ShareWidget; data: ShareDashboard }) {
  const c = data.counts
  const m = data.machine
  const items: { label: string; value: string; tone?: string }[] = [
    { label: t('dash.working'), value: String(c.working), tone: 'var(--vp-state-working)' },
    { label: t('dash.waiting'), value: String(c.waiting), tone: 'var(--vp-state-waiting)' },
    { label: t('dash.done'), value: String(c.done), tone: 'var(--vp-state-done)' },
    { label: t('dash.projects'), value: String(c.projects) },
    { label: t('dash.load'), value: m.load1.toFixed(2) },
    { label: t('board.metric.uptime'), value: duration(m.uptime) },
  ]
  return (
    <Tile kind={w.kind} span={w.span} height={w.height} testid="widget-nowstrip" plain>
      <div className="flex flex-wrap items-baseline gap-x-10 gap-y-2" data-testid="nowstrip">
        {items.map((i) => (
          <span key={i.label} className="flex items-baseline gap-2">
            <span
              className="tabular text-vp-2xl font-semibold"
              style={{ color: i.tone ?? 'var(--vp-ink)' }}
            >
              {i.value}
            </span>
            <span className="text-vp-xl text-ink-3">{i.label}</span>
          </span>
        ))}
      </div>
    </Tile>
  )
}

/**
 * How many agents, how many shells, how much else.
 *
 * "Six sessions" and "four agents and two shells" are different sentences, and
 * only the second says how much of the machine's load is work. `kind` is the
 * three-valued summary the server sends; the command line is never here.
 */
export function Kinds({ w, data }: { w: ShareWidget; data: ShareDashboard }) {
  const tally = { agent: 0, shell: 0, other: 0 }
  for (const row of data.sessions) {
    if (row.kind === 'agent') tally.agent++
    else if (row.kind === 'shell') tally.shell++
    else tally.other++
  }
  return (
    <Tile kind={w.kind} span={w.span} height={w.height} testid="widget-kinds" label={t('dash.sessions')}>
      <div className="flex flex-col gap-1" data-testid="kinds">
        {(['agent', 'shell', 'other'] as const).map((k) => (
          <div key={k} className="flex items-baseline justify-between gap-3">
            <span className="truncate text-vp-xl text-ink-2">
              {t(`dash.kind${k[0].toUpperCase()}${k.slice(1)}` as 'dash.kindAgent')}
            </span>
            <span className="tabular text-vp-2xl font-semibold text-ink">{tally[k]}</span>
          </div>
        ))}
      </div>
    </Tile>
  )
}

/**
 * Where it is all happening: the projects with the most running, ranked.
 *
 * `projects` lists every group in the order the server sent them; this answers
 * a different question on a tile with room for four rows. Under `counts` the
 * groups have no names, so they are numbered — the same numbering the session
 * groups already use, so the two tiles line up.
 */
export function Busiest({ w, data }: { w: ShareWidget; data: ShareDashboard }) {
  const density = useDensity()
  const rows = [...data.projects].sort((a, b) => b.total - a.total).slice(0, rowsAt(density, 6))
  const most = rows.reduce((n, r) => Math.max(n, r.total), 0)
  return (
    <Tile kind={w.kind} span={w.span} height={w.height} testid="widget-busiest" label={t('dash.projects')}>
      {rows.length === 0 ? (
        <Empty text={t('dash.nothing')} />
      ) : (
        rows.map((p, i) => (
          <Bar
            key={p.id}
            testid="busiest-row"
            label={p.name ? safeText(p.name) : t('dash.group', { n: i + 1 })}
            value={`${p.total}`}
            fraction={most > 0 ? p.total / most : 0}
            tone="var(--vp-accent)"
          />
        ))
      )}
    </Tile>
  )
}

/**
 * How long each session has been where it is, on one shared scale.
 *
 * Deliberately dwell and not history, and the comment is here because the
 * missing feature is the obvious one to add: the panel stores when a session
 * entered its *current* state and nothing about the states before it, so a bar
 * segmented across the last hour would be drawn from data that does not exist.
 * A state-change log would be needed first.
 *
 * What this draws is true and is still the widget that turns "seventeen agents
 * working in parallel" from a number into a picture. Red line 4: the bar
 * carries the state as hue, the glyph beside it carries it as shape, and the
 * length is a duration printed at the end of the row.
 */
export function Timeline({ w, data, now }: { w: ShareWidget; data: ShareDashboard; now: number }) {
  const density = useDensity()
  const rows = orderRows(filterRows(data.sessions, w.filter ?? 'all'), w.order ?? 'waited')
  const longest = rows.reduce((n, r) => Math.max(n, dwell(r, now)), 1)
  return (
    <Tile kind={w.kind} span={w.span} height={w.height} testid="widget-timeline">
      {rows.length === 0 ? (
        <Empty text={t('dash.nothing')} />
      ) : (
        <div className="flex flex-col gap-2" data-testid="timeline">
          {rows.slice(0, rowsAt(density, 12)).map((row, i) => (
            <div key={row.id} className="flex items-center gap-3" data-testid="timeline-row">
              <StateDot state={row.state} size={18} />
              <span className="w-40 min-w-0 shrink-0 truncate text-vp-xl text-ink-2">
                {row.name ? safeText(row.name) : t('dash.row', { n: i + 1 })}
              </span>
              <div
                className="h-3 min-w-0 flex-1 overflow-hidden rounded-full"
                style={{ background: 'var(--vp-surface-2)' }}
              >
                <div
                  className="h-full rounded-full transition-[width] duration-500 ease-vp"
                  style={{
                    width: `${Math.max(2, (dwell(row, now) / longest) * 100)}%`,
                    background: `var(--vp-state-${row.state})`,
                  }}
                />
              </div>
              <span className="tabular w-20 shrink-0 text-right text-vp-xl text-ink-3">
                {duration(dwell(row, now))}
              </span>
            </div>
          ))}
        </div>
      )}
    </Tile>
  )
}

function dwell(row: ShareSession, now: number): number {
  return row.stateChangedAt > 0 ? Math.max(0, now - row.stateChangedAt) : 0
}

/**
 * Whether the panel behind this screen is well.
 *
 * The widget for the failure this whole dashboard keeps circling: a wall that
 * has quietly stopped being true looks exactly like a quiet afternoon. Three
 * facts the numbers themselves cannot carry — is the panel keeping its records
 * up to date, can it read the process tree, and when does this link go dark.
 */
export function Health({ w, data, now }: { w: ShareWidget; data: ShareDashboard; now: number }) {
  const lines: { label: string; ok: boolean; note?: string }[] = [
    { label: t('dash.healthRecords'), ok: !data.stale },
    { label: t('dash.healthUsage'), ok: data.usageReadable },
  ]
  if (data.expiresAt > 0) {
    lines.push({
      label: t('dash.healthExpiry'),
      ok: data.expiresAt - now > 86400,
      note: duration(Math.max(0, data.expiresAt - now)),
    })
  }
  return (
    <Tile kind={w.kind} span={w.span} height={w.height} testid="widget-health" label={t('dash.health')}>
      <div className="flex flex-col gap-1" data-testid="health">
        {lines.map((l) => (
          <div key={l.label} className="flex items-baseline justify-between gap-3">
            <span className="flex min-w-0 items-baseline gap-2">
              {/* The glyph, not the hue, is what says which this is. */}
              <span
                aria-hidden="true"
                className="text-vp-xl"
                style={{ color: l.ok ? 'var(--vp-state-done)' : 'var(--vp-state-waiting)' }}
              >
                {l.ok ? '✓' : '▲'}
              </span>
              <span className="truncate text-vp-xl text-ink-2">{l.label}</span>
            </span>
            <span className="tabular shrink-0 text-vp-xl text-ink-3">
              {l.note ?? (l.ok ? t('dash.healthOk') : t('dash.healthNot'))}
            </span>
          </div>
        ))}
      </div>
    </Tile>
  )
}

/**
 * The clock, the date and the day of the week.
 *
 * A screen in a corridor is a clock most of the time. `clock` is the small one
 * for a corner of a board; this is the one that anchors a wall, so the date is
 * under the time rather than beside it and both are formatted by the browser,
 * which knows the reader's language and the server does not.
 */
export function DateTime({ w }: { w: ShareWidget }) {
  const [at, setAt] = useState(() => new Date())
  useEffect(() => {
    const timer = window.setInterval(() => setAt(new Date()), 1000)
    return () => clearInterval(timer)
  }, [])
  return (
    <Tile kind={w.kind} span={w.span} height={w.height} testid="widget-datetime" plain>
      <div className="flex min-w-0 flex-col justify-center" data-testid="datetime">
        <span className="tabular truncate text-vp-3xl font-semibold text-ink">
          {at.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })}
        </span>
        <span className="truncate text-vp-xl text-ink-2">
          {at.toLocaleDateString(undefined, { month: 'long', day: 'numeric' })}
        </span>
        <span className="truncate text-vp-xl text-ink-3">
          {at.toLocaleDateString(undefined, { weekday: 'long' })}
        </span>
      </div>
    </Tile>
  )
}

/**
 * The link's own remark, as the screen's name.
 *
 * The same string the header already carries, placeable: on a rotating board
 * the header is one line above every page, and the name of the room the screen
 * is in belongs on the page. It is the one widget whose words the owner can
 * change without touching the board at all — which is the point, because the
 * board may be locked and the label may not be.
 */
export function RemarkTile({ w, data }: { w: ShareWidget; data: ShareDashboard }) {
  return (
    <Tile kind={w.kind} span={w.span} height={w.height} testid="widget-remark" plain>
      <span className="truncate text-vp-2xl font-semibold text-ink-2" data-testid="remark-tile">
        {safeText(data.remark)}
      </span>
    </Tile>
  )
}

/**
 * Words the owner typed, as a heading over what follows.
 *
 * A caption is a sentence in a tile; this is a label with a rule under it and
 * no surface of its own, so the tiles below it read as belonging to it.
 */
export function Heading({ w }: { w: ShareWidget }) {
  return (
    <Tile kind={w.kind} span={w.span} height={w.height} testid="widget-heading" plain>
      <div className="border-b border-hairline pb-2" data-testid="board-heading">
        <span className="truncate text-vp-xl font-medium uppercase tracking-wide text-ink-3">
          {safeText(w.text ?? '')}
        </span>
      </div>
    </Tile>
  )
}

/** A hairline across the board. */
export function Rule({ w }: { w: ShareWidget }) {
  return (
    <Tile kind={w.kind} span={w.span} height={w.height} testid="widget-rule" plain>
      <div className="border-t border-hairline" data-testid="board-rule" />
    </Tile>
  )
}

/**
 * Nothing at all, occupying its span.
 *
 * The whole of the explicit-placement vocabulary. A flat auto-flowing list
 * cannot leave a hole, and a hole is how a wall stops being a solid brick of
 * tiles — this buys that without a coordinate per widget per breakpoint.
 */
export function Spacer({ w }: { w: ShareWidget }) {
  return (
    <Tile kind={w.kind} span={w.span} height={w.height} testid="widget-spacer" plain>
      <div aria-hidden="true" />
    </Tile>
  )
}
