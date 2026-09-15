import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { Crosshair, Maximize2, RotateCw, Send, TriangleAlert, Upload, Tv, X } from 'lucide-react'

import { api } from '../../protocol/api'
import type {
  Session,
  ShareDetail,
  SharePageCatalogue,
  SharePageDetail,
  SharePageDraft,
  SharePageRow,
  SharePageViewport,
} from '../../protocol/wire'
import { t, useLang } from '../../i18n'
import { safeText } from '../text'
import {
  mergeErrors,
  pickLine,
  readFrameMessage,
  shouldReload,
  type FrameError,
} from './pick'
import { useNow } from './usePages'

/**
 * The Preview pane: a page's draft, drawn next to the agent writing it.
 *
 * The frame is on a *preview link* -- a real share link the server mints for
 * fifteen minutes -- so it goes through exactly the route a wall does. What it
 * shows is what a wall would show, redaction and sandbox included; the pane
 * adds instruments around it and nothing inside it.
 *
 * Everything the frame says arrives by postMessage and goes through
 * readFrameMessage, which believes a message only from this pane's own frames.
 * Nothing the frame says decides anything but what is drawn here, what is
 * pasted at a prompt the person then has to send, and the errors file the
 * agent reads.
 */

/** How often the draft's fingerprint is asked for while the pane is open. */
const FINGERPRINT_MS = 500

/** How often the preview link is renewed. It lives fifteen minutes. */
const RENEW_MS = 5 * 60_000

/** How often the page's links and versions are re-read, for trials and viewers. */
const DETAIL_MS = 5000

/** How long frame errors sit before they are written for the agent. */
const REPORT_AFTER_MS = 1000

const TRIAL_MINUTES = [5, 10, 30]

/** The tallest a frame is drawn in the side panel, and in the window. A phone
 *  scaled to the full width of a wide window is a column taller than the screen. */
const PANEL_FRAME_HEIGHT = 640
const FULL_FRAME_HEIGHT = 720

const INPUT =
  'min-w-0 rounded-vp border border-hairline bg-surface-2 px-2 py-1 text-vp-sm text-ink outline-none focus:border-accent'

interface PreviewLink {
  id: string
  token: string
}

/** The biggest screen already showing this page, as a viewport of its own, or null. */
function liveViewport(detail: SharePageDetail | null): SharePageViewport | null {
  const live = (detail?.links ?? [])
    .filter((l) => l.viewportWidth > 0 && l.viewportHeight > 0)
    .sort((a, b) => b.viewportWidth * b.viewportHeight - a.viewportWidth * a.viewportHeight)[0]
  if (!live) return null
  return { name: `${live.viewportWidth}×${live.viewportHeight}`, width: live.viewportWidth, height: live.viewportHeight }
}

/**
 * The screen a page is most likely composed for: the biggest one showing it,
 * then the first its manifest names, then a laptop.
 */
function defaultViewport(
  all: SharePageViewport[],
  live: SharePageViewport | null,
  manifestViewports: string[] | undefined,
): SharePageViewport {
  return (
    live ??
    all.find((v) => v.name === manifestViewports?.[0]) ??
    all.find((v) => v.name === 'laptop') ?? { name: 'laptop', width: 1440, height: 900 }
  )
}

export function PagePreview({
  page,
  session,
  full,
  onPaste,
}: {
  page: SharePageRow
  /** The session to paste into: the selected one, when it is in this page's project. */
  session: Session | null
  full: boolean
  onPaste: (sessionId: string, text: string, submit: boolean) => void
}) {
  useLang()
  const [catalogue, setCatalogue] = useState<SharePageCatalogue | null>(null)
  const [detail, setDetail] = useState<SharePageDetail | null>(null)
  const [draft, setDraft] = useState<SharePageDraft | null>(null)
  const [mode, setMode] = useState<ShareDetail>('counts')
  // '' is live data; anything else is a fixture's name.
  const [fixture, setFixture] = useState('')
  const [chosen, setChosen] = useState<string[]>([])
  const [link, setLink] = useState<PreviewLink | null>(null)
  const [reloads, setReloads] = useState(0)
  const [errors, setErrors] = useState<FrameError[]>([])
  const [picking, setPicking] = useState(false)
  // The screen drawn large over the whole window, or null.
  const [zoomed, setZoomed] = useState<SharePageViewport | null>(null)
  const [panel, setPanel] = useState<'' | 'publish' | 'trial'>('')
  const [note, setNote] = useState('')
  const [trialLink, setTrialLink] = useState('')
  const [trialMinutes, setTrialMinutes] = useState(10)
  const [say, setSay] = useState('')
  const [busy, setBusy] = useState(false)
  const [failure, setFailure] = useState('')
  const frames = useRef<(HTMLIFrameElement | null)[]>([])
  const now = useNow()

  const fail = (e: unknown) => setFailure(e instanceof Error ? e.message : String(e))

  // ── the preview link ──
  useEffect(() => {
    let cancelled = false
    api.previewPage(page.id, mode).then(
      (made) => {
        if (!cancelled) setLink({ id: made.id, token: made.token })
      },
      (e: unknown) => {
        if (!cancelled) setFailure(e instanceof Error ? e.message : String(e))
      },
    )
    return () => {
      cancelled = true
    }
  }, [page.id, mode])

  useEffect(() => {
    if (!link) return
    const timer = window.setInterval(() => {
      api.renewPreview(page.id, link.id).catch(() => {
        // Expired while the laptop slept. A fresh one is the same click.
        api.previewPage(page.id, mode).then((made) => setLink({ id: made.id, token: made.token }), fail)
      })
    }, RENEW_MS)
    return () => clearInterval(timer)
  }, [link, page.id, mode])

  // ── what the pane knows about the page ──
  const refreshDraft = useCallback(() => {
    api.pageDraft(page.id).then(setDraft, fail)
  }, [page.id])

  useEffect(() => {
    let cancelled = false
    api.pageCatalogue().then((c) => {
      if (!cancelled) setCatalogue(c)
    }, fail)
    api.pageDraft(page.id).then((d) => {
      if (!cancelled) setDraft(d)
    }, fail)
    const readDetail = () => {
      if (document.hidden) return
      api.page(page.id).then((d) => {
        if (!cancelled) setDetail(d)
      }, () => {})
    }
    readDetail()
    const timer = window.setInterval(readDetail, DETAIL_MS)
    return () => {
      cancelled = true
      clearInterval(timer)
    }
  }, [page.id])

  // ── reload when the directory settles ──
  const fingerprints = useRef({ loaded: '', previous: '' })
  useEffect(() => {
    let cancelled = false
    let inFlight = false
    const poll = () => {
      if (inFlight || document.hidden) return
      inFlight = true
      api.pageFingerprint(page.id).then(
        ({ fingerprint }) => {
          inFlight = false
          if (cancelled) return
          const f = fingerprints.current
          if (f.loaded === '') f.loaded = fingerprint
          if (shouldReload(f.loaded, f.previous, fingerprint)) {
            f.loaded = fingerprint
            setErrors([])
            setReloads((n) => n + 1)
            refreshDraft()
          }
          f.previous = fingerprint
        },
        () => {
          inFlight = false
        },
      )
    }
    const timer = window.setInterval(poll, FINGERPRINT_MS)
    return () => {
      cancelled = true
      clearInterval(timer)
    }
  }, [page.id, refreshDraft])

  // ── what the frames say ──
  useEffect(() => {
    const onMessage = (e: MessageEvent) => {
      const msg = readFrameMessage(
        e.source,
        e.data,
        frames.current.map((f) => f?.contentWindow),
      )
      if (!msg) return
      if (msg.type === 'vp.errors') setErrors((was) => mergeErrors(was, msg.errors))
      if (msg.type === 'vp.pick') setPicking(msg.on)
      if (msg.type === 'vp.picked') {
        setPicking(false)
        if (session) onPaste(session.id, pickLine(msg), false)
      }
    }
    window.addEventListener('message', onMessage)
    return () => window.removeEventListener('message', onMessage)
  }, [session, onPaste])

  // Written for the agent, a moment after they stop arriving. An empty list is
  // written too, once something had been reported: a stale errors.json is an
  // agent fixing a bug that is already gone.
  const reported = useRef(false)
  useEffect(() => {
    if (errors.length === 0 && !reported.current) return
    const timer = window.setTimeout(() => {
      reported.current = errors.length > 0
      api.reportPageErrors(page.id, errors).catch(() => {})
    }, REPORT_AFTER_MS)
    return () => clearTimeout(timer)
  }, [errors, page.id])

  const togglePick = () => {
    const next = !picking
    setPicking(next)
    // '*' because the frame's origin is opaque and cannot be named. What is
    // sent is an instruction to draw an outline; see readFrameMessage for the
    // direction that matters.
    for (const f of frames.current) f?.contentWindow?.postMessage({ type: 'vp.pick', on: next }, '*')
  }

  // The enlarged frame is one more frame: its errors and picks count, and a
  // pick started from the pane reaches it. Its slot is after the pane's.
  const zoomSlot = 8

  // ── the screens ──
  // The live screen is a choice like the named ones, first in the list: it is
  // the one the owner is composing for and cannot see.
  const live = liveViewport(detail)
  const liveName = live?.name ?? ''
  const allViewports = useMemo(() => {
    const named = catalogue?.viewports ?? []
    return live ? [live, ...named.filter((v) => v.name !== live.name)] : named
    // eslint-disable-next-line react-hooks/exhaustive-deps -- `live` is rebuilt every render; its name is what changes
  }, [catalogue, liveName])
  const viewports = useMemo(() => {
    const picked = chosen.map((name) => allViewports.find((v) => v.name === name)).filter((v) => v !== undefined)
    if (picked.length > 0) return picked.slice(0, full ? 3 : 1)
    return [defaultViewport(allViewports, allViewports.find((v) => v.name === liveName) ?? null, draft?.manifest?.viewports)]
  }, [allViewports, chosen, draft, full, liveName])

  const src = link
    ? `/share/${link.token}/${fixture ? `?fixture=${encodeURIComponent(fixture)}` : ''}`
    : ''

  // ── publish and trial ──
  const publish = async () => {
    setBusy(true)
    try {
      await api.publishPage(page.id, note.trim())
      setNote('')
      setPanel('')
      setFailure('')
      refreshDraft()
      setDetail(await api.page(page.id))
    } catch (e) {
      fail(e)
    } finally {
      setBusy(false)
    }
  }

  const links = detail?.links ?? []
  const trial = links.find((l) => l.pinUntil > now && l.pinVersion > 0)

  const startTrial = async () => {
    const target = trialLink || links[0]?.id
    if (!target) return
    setBusy(true)
    try {
      await api.startTrial(page.id, target, trialMinutes)
      setPanel('')
      setFailure('')
      setDetail(await api.page(page.id))
    } catch (e) {
      fail(e)
    } finally {
      setBusy(false)
    }
  }

  const settleTrial = async (keep: boolean) => {
    if (!trial) return
    setBusy(true)
    try {
      if (keep) await api.keepTrial(page.id, trial.id)
      else await api.endTrial(page.id, trial.id)
      setFailure('')
      refreshDraft()
      setDetail(await api.page(page.id))
    } catch (e) {
      fail(e)
    } finally {
      setBusy(false)
    }
  }

  const problems = draft?.problems ?? []
  const errorCount = problems.filter((p) => p.severity === 'error').length
  const changes = draft?.changes
  const changed = changes
    ? changes.added.length + changes.removed.length + changes.modified.length + (changes.manifest ? 1 : 0)
    : 0
  const published = detail?.page.publishedVersion ?? page.publishedVersion

  return (
    <div data-testid="page-preview" className="@container flex min-h-full flex-col gap-2 p-2">
      {/* What this draft is, against what is published. */}
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-vp-sm text-ink-2">
        <span className="min-w-0 truncate text-ink">{safeText(page.name)}</span>
        <span data-testid="page-published">
          {published > 0 ? t('page.published', { v: published }) : t('page.unpublished')}
        </span>
        {draft?.ok && published > 0 && (
          <span data-testid="page-changed">
            {changed > 0 ? t('page.changed', { n: changed }) : t('page.same')}
          </span>
        )}
        {problems.length > 0 && (
          <span data-testid="page-problems" className="flex items-center gap-1">
            <TriangleAlert size={11} />
            {t('page.problems', { e: errorCount, w: problems.length - errorCount })}
          </span>
        )}
        {draft && !draft.sdkCurrent && draft.ok && <span>{t('page.sdkStale')}</span>}
      </div>

      {draft && !draft.ok && (
        <p data-testid="page-draft-error" className="text-vp-sm" style={{ color: 'var(--vp-state-crashed)' }}>
          {safeText(draft.error)}
        </p>
      )}
      {failure && (
        <p className="text-vp-sm" style={{ color: 'var(--vp-state-crashed)' }}>
          {safeText(failure)}
        </p>
      )}

      {/* The controls: what to look at on one row, what to do on the next. Two
          rows at every width rather than one row that wraps wherever the
          panel's width happens to put the break. */}
      <div className="grid grid-cols-2 gap-1.5">
        <select
          data-testid="page-viewport"
          aria-label={t('page.screen')}
          value={viewports[0]?.name ?? ''}
          onChange={(e) => setChosen([e.target.value, ...chosen.filter((c) => c !== e.target.value)])}
          className={`${INPUT} w-full`}
        >
          {allViewports.map((v) => (
            <option key={v.name} value={v.name}>
              {v.name === liveName
                ? t('page.liveScreen', { size: v.name })
                : t('page.viewportOption', { name: v.name, w: v.width, h: v.height })}
            </option>
          ))}
        </select>
        <select
          data-testid="page-data"
          aria-label={t('page.data')}
          value={fixture === '' ? `live:${mode}` : `fixture:${fixture}`}
          onChange={(e) => {
            const [kind, value] = e.target.value.split(':', 2)
            if (kind === 'live') {
              setFixture('')
              setMode(value === 'names' ? 'names' : 'counts')
            } else {
              setFixture(value)
            }
          }}
          className={`${INPUT} w-full`}
        >
          <option value="live:counts">{t('page.liveCounts')}</option>
          <option value="live:names">{t('page.liveNames')}</option>
          {catalogue?.fixtures.map((f) => (
            <option key={f} value={`fixture:${f}`}>
              {t('page.fixture', { name: f })}
            </option>
          ))}
        </select>
      </div>
      <div className="flex items-center gap-1.5">
        <button
          type="button"
          data-testid="page-pick"
          aria-pressed={picking}
          disabled={!session}
          onClick={togglePick}
          title={session ? t('page.pick') : t('page.pickNoSession')}
          aria-label={t('page.pick')}
          className="vp-control vp-press disabled:opacity-40"
        >
          <Crosshair size={13} />
        </button>
        <button
          type="button"
          data-testid="page-reload"
          onClick={() => {
            setErrors([])
            setReloads((n) => n + 1)
            refreshDraft()
          }}
          title={t('page.reload')}
          aria-label={t('page.reload')}
          className="vp-control vp-press"
        >
          <RotateCw size={13} />
        </button>
        <span className="flex-1" />
        {links.length > 0 && published > 0 && (
          <button
            type="button"
            data-testid="page-trial-open"
            onClick={() => setPanel(panel === 'trial' ? '' : 'trial')}
            title={t('page.trial')}
            aria-label={t('page.trial')}
            className="vp-control vp-press"
          >
            <Tv size={13} />
          </button>
        )}
        <button
          type="button"
          data-testid="page-publish-open"
          disabled={!draft?.ok}
          onClick={() => setPanel(panel === 'publish' ? '' : 'publish')}
          className="vp-press flex items-center gap-1 rounded-vp px-2 py-1 text-vp-sm disabled:opacity-40"
          style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
        >
          <Upload size={12} />
          {t('page.publish')}
        </button>
      </div>

      {full && catalogue && (
        <div className="flex flex-wrap gap-1" data-testid="page-screens">
          {allViewports.map((v) => {
            const on = viewports.some((x) => x.name === v.name)
            return (
              <button
                key={v.name}
                type="button"
                aria-pressed={on}
                onClick={() =>
                  setChosen(
                    on
                      ? viewports.filter((x) => x.name !== v.name).map((x) => x.name)
                      : [...viewports.map((x) => x.name), v.name].slice(-3),
                  )
                }
                className="vp-press rounded-vp border border-hairline px-2 py-0.5 text-vp-xs text-ink-2 aria-pressed:border-accent aria-pressed:text-ink"
              >
                {v.name}
              </button>
            )
          })}
        </div>
      )}

      {trial && (
        <div
          data-testid="page-trial-running"
          className="flex flex-wrap items-center gap-2 rounded-vp border border-hairline bg-surface-2 px-2 py-1.5 text-vp-sm"
        >
          <Tv size={12} className="shrink-0" />
          <span className="min-w-0 flex-1">
            {t('page.trialRunning', {
              v: trial.pinVersion,
              name: safeText(trial.name),
              time: new Date(trial.pinUntil * 1000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }),
            })}
          </span>
          <button
            type="button"
            data-testid="page-trial-keep"
            disabled={busy}
            onClick={() => void settleTrial(true)}
            className="vp-press rounded-vp px-2 py-0.5"
            style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
          >
            {t('page.trialKeep')}
          </button>
          <button
            type="button"
            data-testid="page-trial-end"
            disabled={busy}
            onClick={() => void settleTrial(false)}
            className="vp-press rounded-vp px-2 py-0.5 text-ink-2"
          >
            {t('page.trialEnd')}
          </button>
        </div>
      )}

      {panel === 'publish' && draft?.ok && (
        <div data-testid="page-publish-panel" className="rounded-vp border border-hairline bg-surface-2 p-2 text-vp-sm">
          <ChangeList draft={draft} />
          <div className="mt-2 flex gap-2">
            <input
              data-testid="page-publish-note"
              value={note}
              maxLength={200}
              onChange={(e) => setNote(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') void publish()
              }}
              placeholder={t('page.note')}
              className={`${INPUT} flex-1`}
            />
            <button
              type="button"
              data-testid="page-publish-confirm"
              disabled={busy}
              onClick={() => void publish()}
              className="vp-press rounded-vp px-2 py-1"
              style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
            >
              {t('page.publishNow', { v: published + 1 })}
            </button>
          </div>
        </div>
      )}

      {panel === 'trial' && (
        <div data-testid="page-trial-panel" className="flex flex-wrap items-center gap-2 rounded-vp border border-hairline bg-surface-2 p-2 text-vp-sm">
          <select
            aria-label={t('page.trialScreen')}
            value={trialLink || links[0]?.id || ''}
            onChange={(e) => setTrialLink(e.target.value)}
            className={`${INPUT} flex-1`}
          >
            {links.map((l) => (
              <option key={l.id} value={l.id}>
                {safeText(l.name)}
                {l.viewers > 0 ? ` · ${t('share.viewers', { n: l.viewers })}` : ''}
              </option>
            ))}
          </select>
          <select
            aria-label={t('page.trialFor')}
            value={trialMinutes}
            onChange={(e) => setTrialMinutes(Number(e.target.value))}
            className={INPUT}
          >
            {TRIAL_MINUTES.map((m) => (
              <option key={m} value={m}>
                {t('page.minutes', { n: m })}
              </option>
            ))}
          </select>
          <button
            type="button"
            data-testid="page-trial-start"
            disabled={busy || !draft?.ok}
            onClick={() => void startTrial()}
            className="vp-press rounded-vp px-2 py-1"
            style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
          >
            {t('page.trialStart')}
          </button>
        </div>
      )}

      {/* The frames. Each one is drawn at the screen's real size and scaled to
          fit, so a television's layout is a television's layout and not a
          laptop's squeezed. */}
      <div className={full ? 'flex flex-wrap items-start gap-3' : 'flex flex-col gap-2'}>
        {src &&
          viewports.map((v, i) => (
            <ScaledFrame
              key={`${v.name}|${src}|${reloads}`}
              viewport={v}
              src={src}
              // Side by side in the window, each given an equal share of the
              // width and no more height than the window has; in the panel,
              // the panel's width.
              share={full ? viewports.length : 1}
              maxHeight={full ? FULL_FRAME_HEIGHT : PANEL_FRAME_HEIGHT}
              frameRef={(el) => {
                frames.current[i] = el
              }}
              // Not while picking: the click is the pick's, inside the frame.
              onZoom={picking ? undefined : () => setZoomed(v)}
            />
          ))}
      </div>

      {zoomed && src && (
        <ZoomedFrame
          viewport={zoomed}
          viewports={allViewports}
          src={src}
          reloads={reloads}
          onViewport={setZoomed}
          onClose={() => setZoomed(null)}
          frameRef={(el) => {
            frames.current[zoomSlot] = el
            if (el && picking) {
              el.addEventListener('load', () => el.contentWindow?.postMessage({ type: 'vp.pick', on: true }, '*'), {
                once: true,
              })
            }
          }}
        />
      )}

      {errors.length > 0 && (
        <ul data-testid="page-errors" className="space-y-0.5 text-vp-xs">
          {errors.map((e, i) => (
            <li key={i} className="flex gap-2">
              <span className="shrink-0 text-ink-3">{e.kind}</span>
              <span className="min-w-0 break-words text-ink">{safeText(e.message)}</span>
              {e.source && (
                <span className="shrink-0 font-mono text-ink-3">
                  {safeText(e.source)}
                  {e.line > 0 ? `:${e.line}` : ''}
                </span>
              )}
            </li>
          ))}
        </ul>
      )}

      {problems.length > 0 && (
        <ul data-testid="page-lint" className="space-y-0.5 text-vp-xs text-ink-2">
          {problems.slice(0, 20).map((p, i) => (
            <li key={i}>
              <span className="font-mono text-ink-3">
                {safeText(p.file)}
                {p.line > 0 ? `:${p.line}` : ''}
              </span>{' '}
              {safeText(p.message)}
            </li>
          ))}
        </ul>
      )}

      {/* On a phone the terminal is not beside this, so the pane carries a
          line to it. Here, Enter sends: the person typing is the one sending. */}
      {session && (
        <form
          className="mt-auto flex gap-1.5 @3xl:hidden"
          onSubmit={(e) => {
            e.preventDefault()
            const text = say.trim()
            if (!text) return
            onPaste(session.id, text, true)
            setSay('')
          }}
        >
          <input
            data-testid="page-say"
            data-vp-paste-own
            value={say}
            onChange={(e) => setSay(e.target.value)}
            placeholder={t('page.say')}
            className={`${INPUT} flex-1`}
          />
          <button type="submit" aria-label={t('page.send')} className="vp-control vp-press">
            <Send size={13} />
          </button>
        </form>
      )}
    </div>
  )
}

function ChangeList({ draft }: { draft: SharePageDraft }) {
  const c = draft.changes
  const rows = [
    ...c.added.map((p) => ({ p, k: t('page.added') })),
    ...c.modified.map((p) => ({ p, k: t('page.modified') })),
    ...c.removed.map((p) => ({ p, k: t('page.removed') })),
    ...(c.manifest ? [{ p: 'vibepanel.json', k: t('page.modified') }] : []),
  ]
  if (rows.length === 0) return <p className="text-ink-3">{t('page.same')}</p>
  return (
    <ul data-testid="page-changes" className="max-h-32 space-y-0.5 overflow-y-auto">
      {rows.map((r) => (
        <li key={`${r.k}${r.p}`} className="flex gap-2">
          <span className="w-10 shrink-0 text-ink-3">{r.k}</span>
          <span className="min-w-0 truncate font-mono text-ink">{safeText(r.p)}</span>
        </li>
      ))}
    </ul>
  )
}

/**
 * One screen: the iframe at the viewport's real size, scaled to the width it
 * has.
 *
 * `sandbox="allow-scripts"` on the element as well as in the response's policy.
 * The effective sandbox is the intersection of the two, so this cannot widen
 * anything; it is here so the frame is sandboxed even if the response were
 * ever served without its header.
 */
/**
 * One screen, as large as the window allows, over everything.
 *
 * The pane's frames are thumbnails: a television at a third of a sidebar's width
 * is a composition, not something you can read. This is the same preview link
 * at the size of the window, with the screens to switch between, and gone on
 * Escape or a click outside it.
 *
 * A portal, because the side panel is a scroller with its own stacking, and a
 * `fixed` box inside one is only as fixed as its ancestors let it be.
 */
function ZoomedFrame({
  viewport,
  viewports,
  src,
  reloads,
  onViewport,
  onClose,
  frameRef,
}: {
  viewport: SharePageViewport
  viewports: SharePageViewport[]
  src: string
  reloads: number
  onViewport: (v: SharePageViewport) => void
  onClose: () => void
  frameRef: (el: HTMLIFrameElement | null) => void
}) {
  const [height, setHeight] = useState(() => window.innerHeight)
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      // Before the settings dialog's or the panel's own Escape handling.
      e.stopPropagation()
      onClose()
    }
    const onResize = () => setHeight(window.innerHeight)
    window.addEventListener('keydown', onKey, true)
    window.addEventListener('resize', onResize)
    return () => {
      window.removeEventListener('keydown', onKey, true)
      window.removeEventListener('resize', onResize)
    }
  }, [onClose])

  return createPortal(
    <div
      data-testid="page-zoom"
      role="dialog"
      aria-modal="true"
      aria-label={t('page.zoomTitle', { name: viewport.name })}
      className="vp-backdrop fixed inset-0 z-50 flex flex-col bg-black/80 px-4 py-3"
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose()
      }}
    >
      <div className="mx-auto mb-3 flex w-full max-w-[96vw] flex-wrap items-center gap-2 rounded-vp bg-surface px-2 py-1.5">
        <div className="flex flex-wrap gap-1" data-testid="page-zoom-screens">
          {viewports.map((v) => (
            <button
              key={v.name}
              type="button"
              aria-pressed={v.name === viewport.name}
              onClick={() => onViewport(v)}
              className={`vp-press rounded-vp border px-2 py-0.5 text-vp-sm ${
                v.name === viewport.name ? 'border-accent text-ink' : 'border-hairline text-ink-2 hover:text-ink'
              }`}
            >
              {v.name === `${v.width}×${v.height}`
                ? v.name
                : t('page.viewportOption', { name: v.name, w: v.width, h: v.height })}
            </button>
          ))}
        </div>
        <span className="flex-1" />
        <button
          type="button"
          data-testid="page-zoom-close"
          onClick={onClose}
          title={t('page.zoomClose')}
          aria-label={t('page.zoomClose')}
          className="vp-control"
        >
          <X size={14} />
        </button>
      </div>
      <div
        className="flex min-h-0 flex-1 items-start justify-center"
        onClick={(e) => {
          if (e.target === e.currentTarget) onClose()
        }}
      >
        <ScaledFrame
          key={`${viewport.name}|${src}|${reloads}`}
          viewport={viewport}
          src={src}
          share={1}
          // The window less the bar above and the caption below.
          maxHeight={Math.max(200, height - 110)}
          frameRef={frameRef}
        />
      </div>
    </div>,
    document.body,
  )
}

function ScaledFrame({
  viewport,
  src,
  share,
  maxHeight,
  frameRef,
  onZoom,
}: {
  viewport: SharePageViewport
  src: string
  /** How many frames share the row's width. */
  share: number
  /** The tallest the scaled frame may be, in CSS pixels. */
  maxHeight: number
  frameRef: (el: HTMLIFrameElement | null) => void
  /** Draws a click target over the frame that opens it large. */
  onZoom?: () => void
}) {
  const row = useRef<HTMLElement>(null)
  const [scale, setScale] = useState(0)
  useEffect(() => {
    // The figure's parent is the row; measure that, so a frame's own size
    // never feeds back into the width it is fitted to.
    const el = row.current?.parentElement
    if (!el) return
    const fit = () => {
      const gaps = 12 * (share - 1)
      const width = Math.max(0, (el.clientWidth - gaps) / share)
      setScale(Math.min(width / viewport.width, maxHeight / viewport.height))
    }
    fit()
    const ro = new ResizeObserver(fit)
    ro.observe(el)
    return () => ro.disconnect()
  }, [viewport.width, viewport.height, share, maxHeight])
  return (
    <figure ref={row} className="m-0 min-w-0" style={{ width: scale > 0 ? viewport.width * scale : '100%' }}>
      <div
        data-testid="page-frame-box"
        className="relative w-full overflow-hidden rounded-vp border border-hairline bg-surface-2"
        style={{ height: scale > 0 ? viewport.height * scale : 0 }}
      >
        <iframe
          ref={frameRef}
          data-testid="page-frame"
          title={viewport.name}
          src={src}
          sandbox="allow-scripts"
          referrerPolicy="no-referrer"
          width={viewport.width}
          height={viewport.height}
          className="absolute left-0 top-0 origin-top-left border-0"
          style={{ transform: `scale(${scale})`, width: viewport.width, height: viewport.height }}
        />
        {/* Over the frame rather than inside it: a sandboxed document's clicks
            never reach this one. Transparent, with the magnifier shown on
            hover and focus so a thumbnail says it can be opened. */}
        {onZoom && (
          <button
            type="button"
            data-testid="page-zoom-open"
            onClick={onZoom}
            title={t('page.zoom')}
            aria-label={t('page.zoom')}
            className="group absolute inset-0 cursor-zoom-in"
          >
            <span className="absolute right-1.5 top-1.5 rounded-vp bg-black/60 p-1 text-white opacity-0 transition-opacity duration-150 group-hover:opacity-100 group-focus-visible:opacity-100">
              <Maximize2 size={13} />
            </span>
          </button>
        )}
      </div>
      <figcaption className="mt-0.5 text-vp-xs text-ink-3">
        {/* A live screen is named by its size already; saying it twice is noise. */}
        {viewport.name === `${viewport.width}×${viewport.height}`
          ? viewport.name
          : t('page.viewportOption', { name: viewport.name, w: viewport.width, h: viewport.height })}
      </figcaption>
    </figure>
  )
}
