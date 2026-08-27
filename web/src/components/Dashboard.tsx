import { useCallback, useEffect, useMemo, useRef, useState } from 'react'

import { api, UnauthorizedError } from '../protocol/api'
import type { ShareDashboard, ShareSession } from '../protocol/wire'
import { t, useLang } from '../i18n'
import { StateDot } from './StateDot'
import { formatBytes, meterText, meterWidth } from './panels/meter'
import { safeText } from './text'

/**
 * The read-only dashboard behind a share link.
 *
 * A separate page rather than the panel with pieces hidden, and that is a
 * security decision before it is a design one: the panel's shell fetches state,
 * opens the socket and offers to start a session, so "the panel with the
 * dangerous parts turned off" is a list of things somebody has to keep turning
 * off. This component knows one endpoint and has no way to reach another.
 *
 * It is also a different product. This is read from across a room — three
 * metres, at an angle, by somebody who is doing something else — so the type
 * is four times the panel's, there is nothing to click, and the only thing
 * that moves is a number changing.
 */

/** How often the dashboard asks. The same cadence the monitor panel uses. */
const POLL_MS = 2000

/**
 * How long a failing poll stays "reconnecting" before it becomes
 * "disconnected".
 *
 * Ten seconds is five missed polls. Shorter and a wifi hiccup puts a red band
 * across a wall; longer and a display that has genuinely lost the panel goes on
 * looking merely slow. Neither reading is silent either way — the "as of"
 * clock counts up from the first failure, which is the honest signal.
 */
const RECONNECTING_MS = 10_000

/**
 * Connection state, and it is the first-class element on this page.
 *
 * The failure this exists for: a dashboard that has silently frozen looks
 * exactly like a quiet system. Six sessions all "done" and a flat CPU line is
 * either a calm afternoon or a page that stopped talking to the panel forty
 * minutes ago, and nothing about the numbers themselves tells you which.
 *
 * 'gone' is terminal and separate from 'disconnected' on purpose. A revoked or
 * expired link is not going to start working, so saying "reconnecting" about it
 * forever is a lie that somebody eventually acts on.
 */
type Connection = 'connecting' | 'live' | 'reconnecting' | 'disconnected' | 'gone'

function duration(seconds: number): string {
  if (seconds < 60) return `${Math.max(0, Math.floor(seconds))}s`
  const d = Math.floor(seconds / 86400)
  const h = Math.floor((seconds % 86400) / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}m`
  return `${m}m`
}

/** How long ago, in words, for the line that says when the numbers were true. */
function agoText(seconds: number): string {
  if (seconds < 2) return t('dash.agoNow')
  if (seconds < 60) return t('dash.agoSeconds', { n: Math.floor(seconds) })
  if (seconds < 3600) return t('dash.agoMinutes', { n: Math.floor(seconds / 60) })
  return t('dash.agoHours', { n: Math.floor(seconds / 3600) })
}

function clockText(unixSeconds: number): string {
  return new Date(unixSeconds * 1000).toLocaleTimeString()
}

function kindLabel(kind: string): string {
  if (kind === 'agent') return t('dash.kindAgent')
  if (kind === 'shell') return t('dash.kindShell')
  return t('dash.kindOther')
}

function connectionTone(state: Connection): string {
  if (state === 'live') return 'var(--vp-state-done)'
  if (state === 'gone' || state === 'disconnected') return 'var(--vp-state-crashed)'
  return 'var(--vp-state-waiting)'
}

/**
 * The connection glyph.
 *
 * Red line 4 applies here more than anywhere: this is the one indicator whose
 * meaning somebody has to read at a glance from a distance, and hue is the
 * first thing that distance and a colour-blind reader take away. Four shapes,
 * all unmistakable at 40px — a filled dot inside a ring, a ring with a gap, a
 * ring struck through, a broken chain — and the word beside every one of them
 * at the largest size on the page.
 */
function ConnectionGlyph({ state, size }: { state: Connection; size: number }) {
  const colour = connectionTone(state)
  const label = connectionLabel(state)
  const common = { width: size, height: size, viewBox: '0 0 24 24', role: 'img' as const }

  if (state === 'live') {
    return (
      <svg {...common} aria-label={label} className="vp-breathe">
        <title>{label}</title>
        <circle cx="12" cy="12" r="10" fill="none" stroke={colour} strokeWidth="2" />
        <circle cx="12" cy="12" r="4.5" fill={colour} />
      </svg>
    )
  }
  if (state === 'connecting' || state === 'reconnecting') {
    // A ring with a quarter missing: the shape of something not yet closed.
    return (
      <svg {...common} aria-label={label} className="vp-breathe">
        <title>{label}</title>
        <path
          d="M12 2 A10 10 0 1 1 4.9 5.0"
          fill="none"
          stroke={colour}
          strokeWidth="2.4"
          strokeLinecap="round"
        />
      </svg>
    )
  }
  if (state === 'disconnected') {
    return (
      <svg {...common} aria-label={label}>
        <title>{label}</title>
        <circle cx="12" cy="12" r="10" fill="none" stroke={colour} strokeWidth="2.4" />
        <path d="M5 19 L19 5" stroke={colour} strokeWidth="2.4" strokeLinecap="round" />
      </svg>
    )
  }
  // Gone: a broken chain, which is a different idea from a bad connection and
  // has to look like one.
  return (
    <svg {...common} aria-label={label}>
      <title>{label}</title>
      <path
        d="M9.5 14.5 L7 17 A3.5 3.5 0 0 1 2 12 L4.5 9.5"
        fill="none"
        stroke={colour}
        strokeWidth="2.2"
        strokeLinecap="round"
      />
      <path
        d="M14.5 9.5 L17 7 A3.5 3.5 0 0 1 22 12 L19.5 14.5"
        fill="none"
        stroke={colour}
        strokeWidth="2.2"
        strokeLinecap="round"
      />
      <path d="M4 4 L20 20" stroke={colour} strokeWidth="2.2" strokeLinecap="round" />
    </svg>
  )
}

function connectionLabel(state: Connection): string {
  if (state === 'live') return t('dash.live')
  if (state === 'connecting') return t('dash.connecting')
  if (state === 'reconnecting') return t('dash.reconnecting')
  if (state === 'disconnected') return t('dash.disconnected')
  return t('dash.gone')
}

/** One headline number, with the state's own shape beside it. */
function Tally({
  value,
  label,
  glyph,
  tone,
}: {
  value: number
  label: string
  glyph: React.ReactNode
  tone: string
}) {
  return (
    <div className="flex min-w-0 flex-col items-start gap-1" data-testid="dash-tally">
      <div className="flex items-center gap-3">
        {glyph}
        <span className="tabular text-vp-3xl font-semibold" style={{ color: tone }}>
          {value}
        </span>
      </div>
      <span className="truncate text-vp-xl text-ink-2">{label}</span>
    </div>
  )
}

/** A machine meter, at the size a wall needs. */
function BigMeter({
  label,
  value,
  detail,
}: {
  label: string
  value: number | null
  detail: string
}) {
  const pct = meterWidth(value)
  const tone =
    pct >= 90
      ? 'var(--vp-state-crashed)'
      : pct >= 75
        ? 'var(--vp-state-waiting)'
        : 'var(--vp-accent)'
  return (
    <div className="min-w-0" data-testid="dash-meter">
      <div className="mb-2 flex items-baseline justify-between gap-3">
        <span className="truncate text-vp-xl text-ink-2">{label}</span>
        <span className="tabular shrink-0 text-vp-2xl font-semibold text-ink">
          {meterText(value)}
        </span>
      </div>
      <div
        className="h-3 overflow-hidden rounded-full"
        style={{ background: 'var(--vp-surface-2)' }}
      >
        <div
          className="h-full rounded-full transition-[width] duration-500 ease-vp"
          style={{ width: `${pct}%`, background: tone }}
        />
      </div>
      <div className="tabular mt-2 truncate text-vp-xl text-ink-2">{detail}</div>
    </div>
  )
}

/** One session, as a row on a wall. */
function Row({
  row,
  index,
  now,
}: {
  row: ShareSession
  index: number
  now: number
}) {
  // The name is empty under a counts-only link, and an ordinal is what makes
  // the rows tellable apart without naming anything.
  const name = row.name ? safeText(row.name) : t('dash.row', { n: index + 1 })
  const since = row.stateChangedAt > 0 ? now - row.stateChangedAt : 0
  const pct = Math.max(0, Math.min(100, row.cpuPercent))
  return (
    <div
      className="flex items-center gap-4 border-t border-hairline py-3 first:border-t-0"
      data-testid="dash-session"
      data-state={row.exited ? 'exited' : row.state}
    >
      <StateDot state={row.state} size={22} exited={row.exited} exitStatus={row.exitStatus} />
      <span className="min-w-0 flex-1 truncate text-vp-xl text-ink">{name}</span>
      <span className="shrink-0 text-vp-xl text-ink-3">{kindLabel(row.kind)}</span>
      {since > 0 && (
        <span className="tabular shrink-0 text-vp-xl text-ink-2">
          {t('dash.forTime', { d: duration(since) })}
        </span>
      )}
      {/* Widths in `ch` rather than on the spacing scale, because the type on
          this page is viewport-relative: a column fixed at 6rem holds "24%" at
          1080p and clips "1024.0 MiB" on a 4K panel where the same text is half
          again as wide. With tabular figures, `ch` is the column. */}
      <span
        className="tabular shrink-0 text-right text-vp-xl text-ink"
        style={{ minWidth: '5ch' }}
      >
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

export function Dashboard({ token }: { token: string }) {
  useLang()
  const [data, setData] = useState<ShareDashboard | null>(null)
  const [connection, setConnection] = useState<Connection>('connecting')
  // A second clock, ticking whether or not the polls are landing. Without it
  // the "as of" line freezes at the same moment the numbers do, and the page
  // stops being able to say how long it has been wrong.
  const [now, setNow] = useState(() => Math.floor(Date.now() / 1000))
  const lastOkRef = useRef(0)
  const goneRef = useRef(false)

  useEffect(() => {
    const timer = window.setInterval(() => setNow(Math.floor(Date.now() / 1000)), 1000)
    return () => clearInterval(timer)
  }, [])

  const poll = useCallback(async () => {
    try {
      const next = await api.shareDashboard(token)
      lastOkRef.current = Date.now()
      setData(next)
      setConnection('live')
    } catch (e) {
      if (e instanceof UnauthorizedError) {
        // Revoked or expired. Terminal: it is not going to start working, and
        // going on asking would be an unauthenticated request in a loop
        // against an endpoint that records rejections.
        goneRef.current = true
        setConnection('gone')
        return
      }
      setConnection(
        lastOkRef.current > 0 && Date.now() - lastOkRef.current < RECONNECTING_MS
          ? 'reconnecting'
          : 'disconnected',
      )
    }
  }, [token])

  useEffect(() => {
    let cancelled = false
    let timer = 0
    const tick = async () => {
      await poll()
      if (cancelled || goneRef.current) return
      timer = window.setTimeout(() => void tick(), POLL_MS)
    }
    void tick()
    return () => {
      cancelled = true
      clearTimeout(timer)
    }
  }, [poll])

  // The tab's name, so a wall with three of these open can be told apart from
  // the browser's own furniture. Lifted out of the dependency array rather than
  // written as `data?.name` there, which is a shape the hooks lint reads as a
  // complex expression.
  const linkName = data?.name ?? ''
  useEffect(() => {
    if (linkName) document.title = safeText(linkName)
  }, [linkName])

  const groups = useMemo(() => {
    if (!data) return []
    return data.projects.map((project, i) => ({
      project,
      index: i,
      rows: data.sessions.filter((s) => s.projectId === project.id),
    }))
  }, [data])

  if (connection === 'gone') return <LinkGone />
  if (!data) return <FirstLoad state={connection} />

  const age = Math.max(0, now - data.at)
  const frozen = connection !== 'live'
  const machine = data.machine
  const memUsed = machine.memTotal - machine.memAvailable
  const memPct = machine.memTotal > 0 ? (memUsed / machine.memTotal) * 100 : null
  const diskUsed = machine.diskTotal - machine.diskFree
  const diskPct = machine.diskTotal > 0 ? (diskUsed / machine.diskTotal) * 100 : null

  return (
    <div className="flex h-full min-h-0 flex-col bg-bg text-ink" data-testid="dashboard">
      <header className="flex flex-wrap items-center gap-x-6 gap-y-2 border-b border-hairline px-8 py-5">
        <h1 className="min-w-0 flex-1 truncate text-vp-2xl font-semibold text-ink">
          {safeText(data.name)}
        </h1>
        <span className="shrink-0 text-vp-xl text-ink-3">{t('dash.readOnly')}</span>
        <div className="flex shrink-0 items-center gap-3" data-testid="dash-connection" data-connection={connection}>
          <ConnectionGlyph state={connection} size={40} />
          <span className="text-vp-2xl font-semibold" style={{ color: connectionTone(connection) }}>
            {connectionLabel(connection)}
          </span>
        </div>
        <span className="tabular shrink-0 text-vp-xl text-ink-2" data-testid="dash-asof">
          {t('dash.asOf', { time: clockText(data.at) })} · {agoText(age)}
        </span>
        {/* Said before the link goes dark rather than after. A wall that stops
            working overnight with no warning is read as the panel having died. */}
        {data.expiresAt > 0 && (
          <span className="tabular shrink-0 text-vp-xl text-ink-3" data-testid="dash-expiry">
            {t('dash.expiresIn', { when: duration(Math.max(0, data.expiresAt - now)) })}
          </span>
        )}
      </header>

      {/* A band rather than a tinted dot. The whole point is that a frozen
          dashboard must not be able to pass for a quiet one. */}
      {frozen && (
        <div
          className="flex items-center gap-4 border-b border-hairline px-8 py-4"
          style={{ background: 'var(--vp-surface-2)' }}
          data-testid="dash-frozen"
        >
          <ConnectionGlyph state={connection} size={32} />
          <span className="text-vp-xl text-ink">{t('dash.frozen', { ago: agoText(age) })}</span>
        </div>
      )}

      {data.stale && (
        <div
          className="border-b border-hairline px-8 py-4 text-vp-xl"
          style={{ background: 'var(--vp-surface-2)', color: 'var(--vp-state-waiting)' }}
          data-testid="dash-stale"
        >
          {t('dash.stale')}
        </div>
      )}

      {/* Everything below dims while the numbers are not current. Dimmed and
          not hidden: the last true reading is still the most useful thing on
          the screen, it just must not be presented as this moment's. */}
      <div
        className="min-h-0 flex-1 overflow-y-auto px-8 py-6"
        style={{ opacity: frozen ? 0.55 : 1, transition: 'opacity 400ms var(--vp-ease)' }}
      >
        <section className="mb-8 flex flex-wrap items-start gap-x-12 gap-y-6" data-testid="dash-counts">
          <Tally
            value={data.counts.waiting}
            label={t('dash.waiting')}
            tone="var(--vp-state-waiting)"
            glyph={<StateDot state="waiting" size={30} />}
          />
          <Tally
            value={data.counts.working}
            label={t('dash.working')}
            tone="var(--vp-state-working)"
            glyph={<StateDot state="working" size={30} />}
          />
          <Tally
            value={data.counts.done}
            label={t('dash.done')}
            tone="var(--vp-state-done)"
            glyph={<StateDot state="done" size={30} />}
          />
          {data.counts.crashed > 0 && (
            <Tally
              value={data.counts.crashed}
              label={t('dash.crashed')}
              tone="var(--vp-state-crashed)"
              glyph={<StateDot state="done" size={30} exited exitStatus={1} />}
            />
          )}
          <div className="flex min-w-0 flex-col items-start gap-1">
            <span className="tabular text-vp-3xl font-semibold text-ink-2">
              {data.counts.sessions}
            </span>
            <span className="truncate text-vp-xl text-ink-2">
              {t('dash.sessions')} · {data.counts.projects} {t('dash.projects')}
            </span>
          </div>
        </section>

        <section
          className="mb-8 grid gap-x-10 gap-y-6"
          style={{ gridTemplateColumns: 'repeat(auto-fit, minmax(260px, 1fr))' }}
          data-testid="dash-machine"
        >
          <BigMeter
            label={t('monitor.cpu')}
            value={machine.cpuReadable ? machine.cpuPercent : null}
            detail={t('monitor.cores', { n: machine.cores })}
          />
          <BigMeter
            label={t('monitor.memory')}
            value={memPct}
            detail={t('monitor.of', {
              used: formatBytes(memUsed),
              total: formatBytes(machine.memTotal),
            })}
          />
          <BigMeter
            label={t('monitor.disk')}
            value={diskPct}
            detail={t('monitor.free', { size: formatBytes(machine.diskFree) })}
          />
          <div className="min-w-0" data-testid="dash-load">
            <div className="mb-2 text-vp-xl text-ink-2">{t('dash.load')}</div>
            <div className="tabular text-vp-2xl font-semibold text-ink">
              {machine.load1.toFixed(2)} · {machine.load5.toFixed(2)} ·{' '}
              {machine.load15.toFixed(2)}
            </div>
            <div className="tabular mt-2 text-vp-xl text-ink-2">
              {t('monitor.up', { d: duration(machine.uptime) })}
            </div>
          </div>
        </section>

        {groups.length === 0 ? (
          <p className="text-vp-2xl text-ink-3" data-testid="dash-empty">
            {t('dash.nothing')}
          </p>
        ) : (
          groups.map(({ project, index, rows }) => (
            <section key={project.id} className="mb-8" data-testid="dash-group">
              <div className="mb-2 flex items-baseline gap-4">
                <h2 className="min-w-0 flex-1 truncate text-vp-2xl font-semibold text-ink">
                  {project.name ? safeText(project.name) : t('dash.group', { n: index + 1 })}
                </h2>
                <span className="tabular shrink-0 text-vp-xl text-ink-2">
                  {project.waiting} / {project.working} / {project.done}
                </span>
              </div>
              {rows.map((row, i) => (
                <Row key={row.id} row={row} index={i} now={now} />
              ))}
            </section>
          ))
        )}

        {data.detail === 'counts' && (
          <p className="text-vp-xl text-ink-3" data-testid="dash-anonymous">
            {t('dash.anonymous')}
          </p>
        )}
      </div>
    </div>
  )
}

/**
 * Before the first reading has landed.
 *
 * Deliberately not an empty frame: a wall display that shows nothing while it
 * connects is indistinguishable from one that has failed, which is the same
 * mistake the frozen band exists to prevent.
 */
function FirstLoad({ state }: { state: Connection }) {
  return (
    <div
      className="flex h-full flex-col items-center justify-center gap-6 bg-bg px-8 text-center"
      data-testid="dash-firstload"
      data-connection={state}
    >
      <ConnectionGlyph state={state} size={72} />
      <p className="text-vp-2xl text-ink-2">{connectionLabel(state)}</p>
    </div>
  )
}

/** A revoked or expired link, said once and not retried. */
function LinkGone() {
  return (
    <div
      className="flex h-full flex-col items-center justify-center gap-6 bg-bg px-8 text-center"
      data-testid="dash-gone"
    >
      <ConnectionGlyph state="gone" size={72} />
      <p className="text-vp-3xl font-semibold text-ink">{t('dash.gone')}</p>
      <p className="max-w-xl text-vp-xl text-ink-2">{t('dash.goneWhy')}</p>
    </div>
  )
}
