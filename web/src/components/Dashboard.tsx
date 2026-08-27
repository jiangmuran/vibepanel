import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Lock } from 'lucide-react'

import { api, UnauthorizedError } from '../protocol/api'
import type { ShareDashboard } from '../protocol/wire'
import { t, useLang } from '../i18n'
import { Widget } from './board/render'
import { agoText, clockText, duration } from './board/format'
import { forViewport, viewerID } from './board/viewer'
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
 * metres, at an angle, by somebody who is doing something else — so the type is
 * four times the panel's, there is nothing to click, and the only thing that
 * moves is a number changing.
 *
 * What it draws is a *board*: an arrangement stored with the link and sent back
 * with every reading. There is no layout in this file — only the chrome around
 * one, the page rotation, and the grid the widgets are placed into. A board
 * that named a widget this build has never heard of renders an empty tile; see
 * board/render.tsx for why that is the only acceptable answer.
 *
 * There is also no way to change that board from here, and that is the design
 * rather than an omission. The screen this is for is a television with nobody
 * standing at it; the person who wants to rearrange it is somewhere else, on a
 * laptop, signed in. So the board is edited through the settings API and this
 * page picks the change up on its next poll — two seconds — because every poll
 * re-reads the link's row. That is also why the whole share surface is still
 * one GET: nothing had to be added here for the owner to be able to edit a wall
 * they are not standing in front of.
 */

/** How often the dashboard asks. The same cadence the monitor panel uses. */
const POLL_MS = 2000

/**
 * How long a failing poll stays "reconnecting" before it becomes
 * "disconnected".
 *
 * Ten seconds is five missed polls. Shorter and a wifi hiccup puts a red band
 * across a wall; longer and a display that has genuinely lost the panel goes on
 * looking merely slow. Neither reading is silent either way — the "as of" clock
 * counts up from the first failure, which is the honest signal.
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

/**
 * Which page of a rotating board is on screen.
 *
 * A wall that shows one thing forever wastes the wall, and a wall that changes
 * while somebody is reading it wastes their time — so the interval is the
 * board's own, and it restarts whenever the board changes rather than drifting
 * across a redeploy.
 */
function usePages(pages: number, seconds: number): number {
  const [page, setPage] = useState(0)
  useEffect(() => {
    if (pages <= 1 || seconds <= 0) return
    const timer = window.setInterval(() => setPage((p) => (p + 1) % pages), seconds * 1000)
    return () => clearInterval(timer)
  }, [pages, seconds])
  // Derived rather than reset inside the effect: a board that stops rotating,
  // or loses a page, must land on page one on the next render rather than one
  // render later — and a setState in an effect body is a cascading render the
  // lint refuses for exactly that reason.
  if (pages <= 1 || seconds <= 0) return 0
  return Math.min(page, pages - 1)
}

/**
 * The viewport this screen has, banded so it changes when the screen does.
 *
 * Two jobs. It decides how far a stored board collapses (see board/viewer.ts),
 * and it is reported to the panel so the owner composing this wall from a
 * laptop can see what shape of screen they are composing for. Banded to 20px so
 * a window being dragged does not re-render the board on every frame or send a
 * different number on every poll.
 */
function useViewport(): { width: number; height: number } {
  const [size, setSize] = useState(() => band(window.innerWidth, window.innerHeight))
  useEffect(() => {
    const onResize = () => setSize((was) => {
      const next = band(window.innerWidth, window.innerHeight)
      return next.width === was.width && next.height === was.height ? was : next
    })
    window.addEventListener('resize', onResize)
    return () => window.removeEventListener('resize', onResize)
  }, [])
  return size
}

function band(w: number, h: number): { width: number; height: number } {
  const to20 = (v: number) => Math.max(0, Math.round(v / 20) * 20)
  return { width: to20(w), height: to20(h) }
}

export function Dashboard({ token }: { token: string }) {
  useLang()
  const viewport = useViewport()
  // Made once for the life of the tab. It is not a credential and grants
  // nothing; it is what lets the owner's settings page say "two screens" rather
  // than "one address".
  const [viewer] = useState(viewerID)
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
      const next = await api.shareDashboard(token, viewer, viewport.width, viewport.height)
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
  }, [token, viewer, viewport.width, viewport.height])

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

  // Collapsed for this screen. One stored board opens on a phone and on a
  // television, and the collapsing is here rather than in CSS because the grid
  // is twelve columns wide at every size — a span of 7 in a narrower grid is
  // placed by rules nobody wants to reason about.
  const width = viewport.width
  const widgets = useMemo(
    () => (data?.board.widgets ?? []).map((w) => forViewport(w, width)),
    [data, width],
  )
  const pages = useMemo(
    () => widgets.reduce((most, w) => Math.max(most, (w.page ?? 0) + 1), 1),
    [widgets],
  )
  const page = usePages(pages, data?.board.rotate ?? 0)

  if (connection === 'gone') return <LinkGone />
  if (!data) return <FirstLoad state={connection} />

  const age = Math.max(0, now - data.at)
  const frozen = connection !== 'live'
  const onThisPage = widgets.filter((w) => (w.page ?? 0) === page)

  return (
    <div className="flex h-full min-h-0 flex-col bg-bg text-ink" data-testid="dashboard">
      <header className="flex flex-wrap items-center gap-x-6 gap-y-2 border-b border-hairline px-8 py-5">
        <h1 className="min-w-0 flex-1 truncate text-vp-2xl font-semibold text-ink">
          {safeText(data.name)}
        </h1>
        {/* What this link is about, when it is about one thing. A scoped board
            showing nothing means "nothing in the thing you were sent", which is
            a different sentence from "nothing is running". */}
        {data.scope !== '' && (
          <span className="shrink-0 truncate text-vp-xl text-ink-2" data-testid="dash-scope">
            {data.scopeName
              ? safeText(data.scopeName)
              : data.scope === 'session'
                ? t('dash.oneSession')
                : t('dash.oneProject')}
          </span>
        )}
        {/* The owner's own label for this screen. Under both detail modes:
            `detail` is about whether the panel's words may leave the machine,
            and this is the owner's sentence to the person in front of it. */}
        {data.remark !== '' && (
          <span className="min-w-0 shrink truncate text-vp-xl text-ink-2" data-testid="dash-remark">
            {safeText(data.remark)}
          </span>
        )}
        <span className="shrink-0 text-vp-xl text-ink-3">{t('dash.readOnly')}</span>
        {/* Red line 4: the closed padlock is the carrier, not a colour. */}
        {data.locked && (
          <span
            className="flex shrink-0 items-center gap-2 text-vp-xl text-ink-3"
            data-testid="dash-locked"
          >
            <Lock size={18} aria-hidden="true" />
            {t('dash.locked')}
          </span>
        )}
        <div
          className="flex shrink-0 items-center gap-3"
          data-testid="dash-connection"
          data-connection={connection}
        >
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
        className={`min-h-0 flex-1 px-8 py-6 ${data.board.fill ? 'overflow-hidden' : 'overflow-y-auto'}`}
        style={{ opacity: frozen ? 0.55 : 1, transition: 'opacity 400ms var(--vp-ease)' }}
      >
        {onThisPage.length === 0 ? (
          <p className="text-vp-2xl text-ink-3" data-testid="dash-empty">
            {t('dash.nothing')}
          </p>
        ) : (
          <div
            className="vp-board"
            data-testid="dash-board"
            data-page={page}
            data-fill={data.board.fill ? 'true' : 'false'}
          >
            {onThisPage.map((w, i) => (
              <Widget key={`${w.kind}-${i}`} w={w} data={data} now={now} />
            ))}
          </div>
        )}

        {data.detail === 'counts' && (
          <p className="mt-6 text-vp-xl text-ink-3" data-testid="dash-anonymous">
            {t('dash.anonymous')}
          </p>
        )}
      </div>

      {/* Which page of a rotating board this is. Dots rather than "2 / 3": at
          three metres a row of shapes reads instantly and a fraction does not,
          and the filled one says where in the cycle the wall has got to. */}
      {pages > 1 && (
        <div
          className="flex shrink-0 justify-center gap-2 border-t border-hairline py-3"
          data-testid="dash-pages"
          data-pages={pages}
        >
          {Array.from({ length: pages }, (_, i) => (
            <span
              key={i}
              aria-hidden="true"
              className="inline-block rounded-full"
              style={{
                width: '0.7em',
                height: '0.7em',
                background: i === page ? 'var(--vp-accent)' : 'var(--vp-surface-2)',
              }}
            />
          ))}
        </div>
      )}
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
