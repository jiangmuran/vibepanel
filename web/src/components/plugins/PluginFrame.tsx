import { useCallback, useEffect, useRef, useState } from 'react'

import { t, useLang } from '../../i18n'
import { api } from '../../protocol/api'
import type { PluginRow } from '../../protocol/wire'
import { askConfirm } from '../ask'
import { safeText } from '../text'
import { showToast } from '../toasts'
import { allowed, capFor, parseFrameMessage, settled } from './host'
import type { FrameMessage } from './host'
import { requestOpen } from './open'
import { textIn } from './screen'

/**
 * A plugin's frame: docs/plugins.md §5, the host's half.
 *
 * The frame is `<iframe sandbox="allow-scripts allow-forms">` on
 * `/plugin/<grant>/<entry>`, and the grant is minted here for the owner's
 * session. The document inside has an opaque origin and no cookie; every
 * request it makes carries the grant in its path and reaches
 * `/api/plugin/<grant>/v1/` and nothing else. This component is the only
 * thing on the panel's side that talks to it, and the rules are the Preview
 * pane's: a message is accepted only when `event.source` is this frame's
 * window (every sandboxed frame's origin is "null", so the origin proves
 * nothing), the set is fixed, payloads are rebuilt from checked fields, and
 * `open` and `notify` are refused for a plugin whose grants lack the
 * capability. Downward it posts `context`: the handles of what the slot is
 * showing, the theme, the language.
 *
 * Grants are cached per plugin for the page's lifetime and renewed before
 * they expire; a frame whose grant stopped resolving (the plugin disabled,
 * the session signed out) shows the panel's own line rather than the
 * browser's error page.
 */

/** How often a draft's fingerprint is asked for while a frame is in dev mode. */
const FINGERPRINT_MS = 500

/** Renew a grant this long before it expires. */
const RENEW_MARGIN_S = 15 * 60

interface Grant {
  grant: string
  base: string
  expiresAt: number
}

const grants = new Map<string, Promise<Grant>>()

function grantFor(plugin: string): Promise<Grant> {
  const cached = grants.get(plugin)
  if (cached) {
    return cached.then((g) => {
      if (g.expiresAt - Date.now() / 1000 > RENEW_MARGIN_S) return g
      grants.delete(plugin)
      return grantFor(plugin)
    })
  }
  const next = api.mintPluginGrant(plugin).then(
    (g) => ({ grant: g.grant, base: g.base, expiresAt: g.expiresAt }),
    (e: unknown) => {
      grants.delete(plugin)
      throw e
    },
  )
  grants.set(plugin, next)
  return next
}

export function PluginFrame({
  plugin,
  entry,
  slot,
  session,
  project,
  fill,
  className,
  testid,
}: {
  plugin: PluginRow
  entry: string
  slot: string
  /** The panel's own ids of what the slot is showing; the frame gets handles. */
  session?: string | null
  project?: string | null
  /** Fill the box (a pane, a page) rather than size to the frame's content. */
  fill?: boolean
  className?: string
  testid?: string
}) {
  const lang = useLang()
  const frame = useRef<HTMLIFrameElement | null>(null)
  const [grant, setGrant] = useState<Grant | null>(null)
  const [failed, setFailed] = useState('')
  const [height, setHeight] = useState(160)
  const [reloads, setReloads] = useState(0)
  const [fetched, setFetched] = useState<{ for: string; session: string | null; project: string | null } | null>(null)
  const wantKey = session || project ? `${session ?? ''}|${project ?? ''}` : ''
  // What the slot is showing, as the plugin's handles: the last answer for
  // this exact session and project, and nothing while there is none.
  const handles = fetched && fetched.for === wantKey ? fetched : { session: null, project: null }
  const name = textIn(plugin.name, lang)

  useEffect(() => {
    let cancelled = false
    grantFor(plugin.id).then(
      (g) => {
        if (!cancelled) setGrant(g)
      },
      (e: unknown) => {
        if (!cancelled) setFailed(e instanceof Error ? e.message : String(e))
      },
    )
    return () => {
      cancelled = true
    }
  }, [plugin.id, reloads])

  // The handles for what the slot shows, asked for rather than computed: a
  // handle is an HMAC under a salt the browser does not have.
  useEffect(() => {
    if (wantKey === '') return
    let cancelled = false
    api.pluginHandles(plugin.id, session ?? undefined, project ?? undefined).then(
      (h) => {
        if (!cancelled) setFetched({ for: wantKey, session: h.session ?? null, project: h.project ?? null })
      },
      () => {},
    )
    return () => {
      cancelled = true
    }
  }, [plugin.id, session, project, wantKey])

  const postContext = useCallback(() => {
    const win = frame.current?.contentWindow
    if (!win) return
    const theme = document.documentElement.dataset.theme ?? ''
    const narrow = window.innerWidth < 640
    win.postMessage({ type: 'context', context: { session: handles.session, project: handles.project, theme, lang, slot, narrow } }, '*')
  }, [handles, lang, slot])

  useEffect(() => {
    postContext()
  }, [postContext])

  // The theme attribute is written by the toggle, outside React's knowledge
  // of this component; watch it so a frame recolours with the page.
  useEffect(() => {
    const mo = new MutationObserver(postContext)
    mo.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
    return () => mo.disconnect()
  }, [postContext])

  useEffect(() => {
    const onMessage = (e: MessageEvent) => {
      const win = frame.current?.contentWindow
      if (!win || e.source !== win) return
      const msg = parseFrameMessage(e.data)
      if (!msg) return
      const answer = (id: number, value: unknown) => win.postMessage({ type: 'answer', id, value }, '*')
      void (async () => {
        // A gated message is checked against the grants as they are now,
        // not as the list last read them: a box unticked on the settings
        // page is withheld from an open frame on its next ask.
        let row = plugin
        if (capFor(msg) !== null) {
          try {
            row = await api.plugin(plugin.id)
          } catch {
            /* the cached row decides */
          }
        }
        if (!allowed(row, msg)) {
          if ('id' in msg) answer(msg.id, null)
          return
        }
        act(msg, answer)
      })()
    }
    const act = (msg: FrameMessage, answer: (id: number, value: unknown) => void) => {
      switch (msg.type) {
        case 'ready':
          postContext()
          return
        case 'height':
          if (!fill) setHeight(Math.max(48, msg.px))
          return
        case 'error':
          if (plugin.dev) console.warn(`plugin ${plugin.id}: ${msg.message}`)
          return
        case 'open':
          requestOpen({ plugin: plugin.id, session: msg.session, project: msg.project, settings: msg.settings })
          answer(msg.id, true)
          return
        case 'notify':
          showToast({ kind: msg.kind === 'error' ? 'error' : msg.kind === 'success' ? 'success' : 'info', key: 'plg.notice',
            params: { name, text: safeText(msg.text) } })
          answer(msg.id, true)
          return
        case 'confirm':
          void askConfirm({
            title: safeText(msg.title),
            body: safeText(msg.body) || undefined,
            confirm: safeText(msg.confirm) || t('plg.ok'),
            cancel: safeText(msg.cancel) || t('ask.cancel'),
            destructive: msg.destructive,
          }).then((yes) => answer(msg.id, yes))
          return
        case 'menu':
          // A menu anchored to a frame is a later rung's work; a frame asked
          // today is answered honestly with nothing chosen.
          answer(msg.id, null)
          return
      }
    }
    window.addEventListener('message', onMessage)
    return () => window.removeEventListener('message', onMessage)
  }, [plugin, fill, name, postContext])

  // Dev mode: the draft's fingerprint twice a second, and a reload when two
  // readings agree on something new.
  const loaded = useRef('')
  const readings = useRef<string[]>([])
  useEffect(() => {
    if (!plugin.dev) return
    let cancelled = false
    const tick = async () => {
      if (cancelled || document.hidden) return
      try {
        const { fingerprint } = await api.pluginFingerprint(plugin.id)
        if (cancelled) return
        readings.current = [...readings.current.slice(-1), fingerprint]
        if (loaded.current === '') {
          loaded.current = fingerprint
          return
        }
        if (settled(readings.current, loaded.current)) {
          loaded.current = fingerprint
          setReloads((n) => n + 1)
        }
      } catch {
        /* a draft that cannot be read right now is a reload that waits */
      }
    }
    // The baseline at once, not at the first tick: a file written in the
    // half second between the frame loading and the first reading would
    // otherwise become the baseline, and the change it was would never be
    // seen. plugins-check writes right after the frame goes live, which is
    // exactly that window.
    void tick()
    const timer = window.setInterval(() => void tick(), FINGERPRINT_MS)
    return () => {
      cancelled = true
      clearInterval(timer)
    }
  }, [plugin.id, plugin.dev])

  if (failed) {
    return (
      <p className={`px-3 py-4 text-vp-base text-ink-2 ${className ?? ''}`} data-testid={testid ? `${testid}-failed` : undefined}>
        {t('plg.frame.noGrant')} {safeText(failed)}
      </p>
    )
  }
  if (!grant) {
    return <p className={`px-3 py-4 text-vp-sm text-ink-3 ${className ?? ''}`}>{t('plg.frame.loading')}</p>
  }
  return (
    <iframe
      key={`${grant.grant}:${reloads}`}
      ref={frame}
      data-testid={testid}
      data-plugin={plugin.id}
      data-slot={slot}
      title={name}
      src={`${grant.base}${entry}`}
      sandbox="allow-scripts allow-forms"
      referrerPolicy="no-referrer"
      onLoad={postContext}
      className={`block w-full border-0 bg-transparent ${fill ? 'h-full' : ''} ${className ?? ''}`}
      style={fill ? undefined : { height }}
    />
  )
}
