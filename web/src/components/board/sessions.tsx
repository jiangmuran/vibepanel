import { useEffect, useState } from 'react'

import type { ShareDashboard, ShareSession, ShareWidget } from '../../protocol/wire'
import { EXIT_VANISHED } from '../../protocol/wire'
import { t } from '../../i18n'
import { rows as rowsAt, useDensity } from './density'
import { StateDot } from '../StateDot'
import { formatBytes } from '../panels/meter'
import { safeText } from '../text'
import { Bar, Empty, Tile } from './Tile'
import { filterRows, orderRows } from './rows'
import { duration } from './format'

/**
 * The widgets that draw sessions.
 *
 * Two of them draw the same rows in two shapes, and the difference is what the
 * screen is for: a grid answers "how many of these are there and what colour
 * are they" from across a room, and a list answers "which one, and for how
 * long" from a desk. Both take the same filter and order, because "show me only
 * the ones that need an answer" is the same request either way.
 */

/** A row's name, or its position when the link carries no names. */
function rowName(row: ShareSession, index: number): string {
  return row.name ? safeText(row.name) : t('dash.row', { n: index + 1 })
}

function kindLabel(kind: string): string {
  if (kind === 'agent') return t('dash.kindAgent')
  if (kind === 'shell') return t('dash.kindShell')
  return t('dash.kindOther')
}

/**
 * Pages through a list that does not fit, or shows all of it.
 *
 * `size` is how many fit; `seconds` is how long each page stays. A grid of
 * forty sessions on a screen that fits twelve is a grid showing twelve
 * sessions, and the twelve it shows are always the same twelve.
 */
function usePage(count: number, size: number, seconds: number): number {
  const [page, setPage] = useState(0)
  const pages = size > 0 ? Math.ceil(count / size) : 1
  useEffect(() => {
    if (seconds <= 0 || pages <= 1) return
    const timer = window.setInterval(() => setPage((p) => (p + 1) % pages), seconds * 1000)
    return () => clearInterval(timer)
  }, [seconds, pages])
  // Derived rather than reset in the effect, and clamped rather than trusted:
  // the row count shrinks between polls, and an index left pointing past the
  // end renders an empty grid on a wall with nobody there to scroll it.
  if (seconds <= 0 || pages <= 1) return 0
  return Math.min(page, pages - 1)
}

/**
 * Every session as a tile, sized to the screen it is on.
 *
 * One widget rather than a "summary" board and a "wall" board, because the
 * difference between them is the viewport and the viewport is something CSS
 * already knows. `auto-fill` with a viewport-relative minimum means a laptop
 * gets four across and a 4K television gets sixteen, from the same stored
 * board — which is what "if the screen is big enough, lay them all out" asks
 * for without anybody choosing a board per screen.
 */
export function SessionGrid({ w, data, now }: { w: ShareWidget; data: ShareDashboard; now: number }) {
  const rows = orderRows(filterRows(data.sessions, w.filter ?? 'all'), w.order ?? 'state')
  // A page holds what a tile of this width plausibly fits. Deliberately
  // generous: the grid reflows, so overshooting costs a scroll and
  // undershooting hides sessions on a screen bought to show them.
  const size = w.rotate && w.rotate > 0 ? 24 : rows.length
  const page = usePage(rows.length, size, w.rotate ?? 0)
  const shown = size > 0 ? rows.slice(page * size, page * size + size) : rows

  return (
    <Tile
      kind={w.kind}
      span={w.span} height={w.height}
      testid="widget-sessiongrid"
      label={t('board.kind.sessiongrid')}
    >
      {rows.length === 0 ? (
        <Empty text={t('dash.nothing')} />
      ) : (
        <div
          className="grid gap-3"
          style={{
            // A card is a number of *characters* wide, not a number of pixels.
            //
            // It used to be `clamp(150px, 11vw, 260px)` while the type inside
            // came from `--vp-wall`, which the tile computes from its own box.
            // Two unrelated bases: on a 1080p wall the name rendered at around
            // 48px inside a 260px card, so every session read "cla…", "co…",
            // "—…" — nine cards that cannot be told apart, on a screen whose
            // entire job is telling them apart.
            //
            // Tying the track to the same unit keeps the ratio fixed: however
            // large the type gets, the card grows with it. `auto-fill` still
            // decides how many fit.
            gridTemplateColumns: CARD_TRACKS,
          }}
        >
          {shown.map((row) => {
            const since = row.stateChangedAt > 0 ? now - row.stateChangedAt : 0
            return (
              <div
                key={row.id}
                data-testid="dash-tile"
                data-state={row.exited ? 'exited' : row.state}
                className="min-w-0 rounded-vp border border-hairline p-3"
                style={{ background: 'var(--vp-surface-2)' }}
              >
                <div className="mb-2 flex items-center gap-2">
                  <StateDot
                    state={row.state}
                    size={20}
                    exited={row.exited}
                    exitStatus={row.exitStatus}
                  />
                  <span className="min-w-0 flex-1 truncate text-vp-xl text-ink">
                    {rowName(row, data.sessions.indexOf(row))}
                  </span>
                </div>
                <div className="tabular flex items-baseline justify-between gap-2 text-vp-xl text-ink-2">
                  <span>{since > 0 ? duration(since) : '—'}</span>
                  <span>{row.measured ? `${Math.round(row.cpuPercent)}%` : '—'}</span>
                </div>
              </div>
            )
          })}
        </div>
      )}
    </Tile>
  )
}

/** The grid track for a session card.
 *
 *  A `const` rather than an inline style value because the untranslated-string
 *  scanner reads a long object-literal value as English prose -- which is the
 *  right instinct, and this is CSS. */
const CARD_TRACKS = 'repeat(auto-fill, minmax(min(100%, calc(9 * var(--vp-wall, 1rem))), 1fr))'

/** One session as a row: the dense view, for somebody at a desk. */
function Row({ row, index, now }: { row: ShareSession; index: number; now: number }) {
  const since = row.stateChangedAt > 0 ? now - row.stateChangedAt : 0
  const pct = Math.max(0, Math.min(100, row.cpuPercent))
  return (
    <div
      // @container, and the row's own width is what decides. A board tile can
      // be twelve columns on a television or the whole of a 320px phone, and
      // the viewport says nothing about which.
      className="@container flex items-center gap-2 border-t border-hairline py-3 first:border-t-0 @md:gap-4"
      data-testid="dash-session"
      data-state={row.exited ? 'exited' : row.state}
    >
      <StateDot state={row.state} size={22} exited={row.exited} exitStatus={row.exitStatus} />
      <span className="min-w-0 flex-1 truncate text-vp-xl text-ink">{rowName(row, index)}</span>
      {/* The two that go first when there is no room.
        *
        * Every column here but the name is shrink-0, so they add up to a fixed
        * width and the name -- the only flexible one -- absorbs the whole
        * deficit. On a 320px phone that summed to about 360px of columns in a
        * 318px box: the name collapsed to three characters and the memory
        * figure still hung off the right edge. That was the reported
        * "4.6 MiB" overflow.
        *
        * These two are the ones to lose. The kind is already carried by the
        * name for every agent session, and how long it has been in this state
        * is the least urgent of the four numbers -- the state dot beside the
        * name already says *what* it is doing. */}
      <span className="hidden shrink-0 text-vp-xl text-ink-3 @md:inline">{kindLabel(row.kind)}</span>
      {since > 0 && (
        <span className="tabular hidden shrink-0 text-vp-xl text-ink-2 @sm:inline">
          {t('dash.forTime', { d: duration(since) })}
        </span>
      )}
      {/* Widths in `ch` rather than on the spacing scale, because the type on
          this page is viewport-relative: a column fixed at 6rem holds "24%" at
          1080p and clips "1024.0 MiB" on a 4K panel where the same text is half
          again as wide. With tabular figures, `ch` is the column. */}
      <span className="tabular shrink-0 text-right text-vp-xl text-ink" style={{ minWidth: '5ch' }}>
        {row.measured ? `${pct.toFixed(pct < 10 ? 1 : 0)}%` : '—'}
      </span>
      <span
        className="tabular shrink-0 text-right text-vp-xl text-ink-2"
        style={{ minWidth: '10ch' }}
      >
        {row.measured ? formatBytes(row.rss) : '—'}
      </span>
    </div>
  )
}

export function SessionList({ w, data, now }: { w: ShareWidget; data: ShareDashboard; now: number }) {
  const rows = orderRows(filterRows(data.sessions, w.filter ?? 'all'), w.order ?? 'state')
  const size = w.rotate && w.rotate > 0 ? 12 : rows.length
  const page = usePage(rows.length, size, w.rotate ?? 0)
  const shown = size > 0 ? rows.slice(page * size, page * size + size) : rows
  const group = w.group ?? 'project'

  const groups: { key: string; heading: string; rows: ShareSession[] }[] = []
  if (group === 'project') {
    data.projects.forEach((p, i) => {
      const inThis = shown.filter((r) => r.projectId === p.id)
      if (inThis.length > 0) {
        groups.push({
          key: p.id,
          heading: p.name ? safeText(p.name) : t('dash.group', { n: i + 1 }),
          rows: inThis,
        })
      }
    })
  } else if (group === 'state') {
    // Spelled out rather than `t('dash.' + state)`: the dictionary's keys are a
    // union type, and a computed one is a lookup the compiler cannot check —
    // which is how a renamed key becomes a blank heading on a wall.
    for (const [state, heading] of [
      ['waiting', t('dash.waiting')],
      ['working', t('dash.working')],
      ['done', t('dash.done')],
    ] as const) {
      const inThis = shown.filter((r) => !r.exited && r.state === state)
      if (inThis.length > 0) groups.push({ key: state, heading, rows: inThis })
    }
    const gone = shown.filter((r) => r.exited)
    if (gone.length > 0) groups.push({ key: 'exited', heading: t('dash.exited'), rows: gone })
  } else {
    groups.push({ key: 'all', heading: '', rows: shown })
  }

  return (
    <Tile kind={w.kind} span={w.span} height={w.height} testid="widget-sessionlist">
      {rows.length === 0 ? (
        <Empty text={t('dash.nothing')} />
      ) : (
        // Scrolls itself rather than growing past its tile.
        //
        // The tile clips now, so a list longer than its box would simply lose
        // its tail with nothing to say so. At MaxDensity this list asks for
        // 1.8x the rows, which is exactly when it does not fit.
        <div className="min-h-0 flex-1 overflow-y-auto" data-testid="sessionlist-scroll">
        {groups.map((g) => (
          <section key={g.key} className="mb-5 last:mb-0" data-testid="dash-group">
            {g.heading && (
              <h3 className="mb-1 truncate text-vp-xl font-medium text-ink-3">{g.heading}</h3>
            )}
            {g.rows.map((row) => (
              <Row key={row.id} row={row} index={data.sessions.indexOf(row)} now={now} />
            ))}
          </section>
        ))}
        </div>
      )}
    </Tile>
  )
}

/** Which project is where: a stacked bar per project. */
export function Projects({ w, data }: { w: ShareWidget; data: ShareDashboard }) {
  return (
    <Tile kind={w.kind} span={w.span} height={w.height} testid="widget-projects" label={t('board.kind.projects')}>
      {data.projects.length === 0 ? (
        <Empty text={t('dash.nothing')} />
      ) : (
        data.projects.map((p, i) => (
          <div key={p.id} className="mb-3 last:mb-0" data-testid="dash-project">
            <div className="mb-1 flex items-baseline justify-between gap-3">
              <span className="min-w-0 truncate text-vp-xl text-ink">
                {p.name ? safeText(p.name) : t('dash.group', { n: i + 1 })}
              </span>
              <span className="tabular shrink-0 text-vp-xl text-ink-2">
                {p.waiting} / {p.working} / {p.done}
              </span>
            </div>
            {/* Three segments rather than one bar, so the proportion between
                states is visible at a distance where the numbers are not. */}
            <div
              className="flex h-2 overflow-hidden rounded-full"
              style={{ background: 'var(--vp-surface-2)' }}
            >
              {(
                [
                  ['waiting', p.waiting, 'var(--vp-state-waiting)'],
                  ['working', p.working, 'var(--vp-state-working)'],
                  ['done', p.done, 'var(--vp-state-done)'],
                ] as const
              ).map(([key, n, tone]) => (
                <div
                  key={key}
                  style={{
                    width: `${p.total > 0 ? (n / p.total) * 100 : 0}%`,
                    background: tone,
                  }}
                />
              ))}
            </div>
          </div>
        ))
      )}
    </Tile>
  )
}

/** What is costing the most right now. */
export function CPUTop({ w, data }: { w: ShareWidget; data: ShareDashboard }) {
  const density = useDensity()
  const rows = data.sessions
    .filter((r) => r.measured && !r.exited)
    .sort((a, b) => b.cpuPercent - a.cpuPercent)
    .slice(0, rowsAt(density, 6))
  const top = rows.length > 0 ? Math.max(rows[0].cpuPercent, 1) : 1
  return (
    <Tile kind={w.kind} span={w.span} height={w.height} testid="widget-cputop" label={t('board.kind.cputop')}>
      {!data.usageReadable ? (
        <Empty text={t('dash.noMeasurements')} />
      ) : rows.length === 0 ? (
        <Empty text={t('dash.nothing')} />
      ) : (
        rows.map((row) => (
          <Bar
            key={row.id}
            testid="dash-cputop-row"
            label={rowName(row, data.sessions.indexOf(row))}
            value={`${Math.round(row.cpuPercent)}% · ${formatBytes(row.rss)}`}
            fraction={row.cpuPercent / top}
            tone="var(--vp-accent)"
          />
        ))
      )}
    </Tile>
  )
}

/** What has stopped, and what stopped badly. */
export function Exits({ w, data }: { w: ShareWidget; data: ShareDashboard }) {
  const density = useDensity()
  const rows = data.sessions.filter((r) => r.exited).slice(0, rowsAt(density, 8))
  return (
    <Tile kind={w.kind} span={w.span} height={w.height} testid="widget-exits" label={t('board.kind.exits')}>
      <div className="mb-3 flex items-baseline gap-6">
        <span className="tabular text-vp-2xl font-semibold text-ink">{data.counts.exited}</span>
        <span className="text-vp-xl text-ink-2">{t('dash.exited')}</span>
        <span
          className="tabular text-vp-2xl font-semibold"
          style={{ color: 'var(--vp-state-crashed)' }}
        >
          {data.counts.crashed}
        </span>
        <span className="text-vp-xl text-ink-2">{t('dash.crashed')}</span>
      </div>
      {rows.length === 0 ? (
        <Empty text={t('dash.noExits')} />
      ) : (
        rows.map((row) => (
          <div
            key={row.id}
            className="flex items-center gap-3 border-t border-hairline py-2 first:border-t-0"
            data-testid="dash-exit-row"
          >
            <StateDot state={row.state} size={18} exited exitStatus={row.exitStatus} />
            <span className="min-w-0 flex-1 truncate text-vp-xl text-ink">
              {rowName(row, data.sessions.indexOf(row))}
            </span>
            <span className="tabular shrink-0 text-vp-xl text-ink-2">
              {row.exitStatus === EXIT_VANISHED
                ? t('dash.vanished')
                : t('dash.exitStatus', { n: row.exitStatus })}
            </span>
          </div>
        ))
      )}
    </Tile>
  )
}

/** How much of each checklist is finished. Counts, never the items. */
export function Todos({ w, data }: { w: ShareWidget; data: ShareDashboard }) {
  const todos = data.todos
  return (
    <Tile kind={w.kind} span={w.span} height={w.height} testid="widget-todos" label={t('board.kind.todos')}>
      {!todos || todos.projects.length === 0 ? (
        <Empty text={t('dash.emptyWidget')} />
      ) : (
        <>
          <div className="mb-3 flex items-baseline gap-4">
            <span className="tabular text-vp-2xl font-semibold text-ink">
              {todos.done} / {todos.done + todos.open}
            </span>
            {todos.closedToday > 0 && (
              <span className="text-vp-xl text-ink-2">
                {t('dash.closedToday', { n: todos.closedToday })}
              </span>
            )}
          </div>
          {todos.projects.map((p, i) => {
            const total = p.open + p.done
            return (
              <Bar
                key={p.id}
                testid="dash-todo-row"
                label={p.name ? safeText(p.name) : t('dash.group', { n: i + 1 })}
                value={`${p.done}/${total}`}
                fraction={total > 0 ? p.done / total : 0}
                tone="var(--vp-state-done)"
              />
            )
          })}
        </>
      )}
    </Tile>
  )
}
