import { useCallback, useEffect, useRef, useState } from 'react'
import { RotateCw } from 'lucide-react'

import { api } from '../../protocol/api'
import type { VncTarget } from '../../protocol/wire'
import { t, useLang } from '../../i18n'
import {
  PROBE,
  fresh,
  retryDelay,
  sawBytes,
  shouldRetry,
  stateForClose,
  tick,
  type DisplayState,
  type Liveness,
} from './vnc'

/**
 * A VNC display, in the side panel next to the terminal.
 *
 * noVNC rather than a client written here, and the reason is not "it exists".
 * The easy 20% of an RFB client is a canvas and the Raw encoding; the part
 * that decides whether this is usable is Tight, ZRLE and JPEG — a 1920x1080
 * desktop is 8 MB per frame in Raw — and the keyboard, which is keysyms, dead
 * keys, IMEs and modifier tracking, and which fails quietly and per-layout
 * when it is wrong. Those are the two parts a from-scratch client gets wrong,
 * and both are wrong in ways nobody notices on the machine they wrote it on.
 *
 * MPL-2.0, which is file-level copyleft: the library is used unmodified, so
 * the obligation is to keep its notices with it, and it does not reach the
 * MIT code around it.
 *
 * Loaded with a dynamic import so it is a chunk of its own. Everybody pays for
 * the main bundle; only the person who opens this tab should pay for a VNC
 * client.
 */
export function VncView() {
  useLang()
  const [targets, setTargets] = useState<VncTarget[] | null>(null)
  const [selected, setSelected] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    api.vncTargets().then(
      (list) => {
        if (cancelled) return
        setTargets(list)
        setSelected((s) => s ?? list[0]?.id ?? null)
      },
      () => {
        if (!cancelled) setTargets([])
      },
    )
    return () => {
      cancelled = true
    }
  }, [])

  if (targets === null) return null
  if (targets.length === 0) {
    return <p className="px-3 py-4 text-vp-base text-ink-2">{t('vnc.none')}</p>
  }
  const target = targets.find((x) => x.id === selected) ?? targets[0]

  return (
    <div data-testid="vnc-panel" className="flex h-full min-h-0 flex-col">
      {targets.length > 1 && (
        <div className="flex shrink-0 items-center gap-1 overflow-x-auto border-b border-hairline px-2 py-1">
          {targets.map((x) => (
            <button
              key={x.id}
              type="button"
              data-testid="vnc-pick"
              aria-pressed={x.id === target.id}
              onClick={() => setSelected(x.id)}
              className={`shrink-0 rounded-vp px-2 py-1 text-vp-sm transition-colors duration-200 ease-vp ${
                x.id === target.id
                  ? 'bg-surface-2 text-ink'
                  : 'text-ink-2 hover:bg-surface-2 hover:text-ink'
              }`}
            >
              {x.name || `${x.host}:${x.port}`}
            </button>
          ))}
        </div>
      )}
      {/* Keyed by id: switching display tears the old connection down rather
          than reusing a component that still holds the previous socket. */}
      <Display key={target.id} target={target} />
    </div>
  )
}

function Display({ target }: { target: VncTarget }) {
  const holder = useRef<HTMLDivElement | null>(null)
  const [state, setState] = useState<DisplayState>('connecting')
  const [detail, setDetail] = useState('')
  const [attempt, setAttempt] = useState(0)
  const failures = useRef(0)

  const retry = useCallback(() => {
    failures.current = 0
    setAttempt((n) => n + 1)
  }, [])

  useEffect(() => {
    const node = holder.current
    if (!node) return
    let live = true
    let socket: WebSocket | null = null
    let rfb: { disconnect(): void } | null = null
    let retryTimer: ReturnType<typeof setTimeout> | undefined
    let liveness: Liveness = fresh(Date.now())

    setState('connecting')
    setDetail('')

    const scheme = location.protocol === 'https:' ? 'wss' : 'ws'
    socket = new WebSocket(`${scheme}://${location.host}/api/vnc/targets/${target.id}/socket`)
    socket.binaryType = 'arraybuffer'

    // A second listener on the same socket noVNC is given.
    //
    // noVNC assigns `onmessage`, so `addEventListener` sits alongside it
    // rather than replacing it, and both run. That is what makes it possible
    // to timestamp inbound frames without wrapping the socket in a fake — and
    // a fake is what the first design needed, which would have put a
    // duck-typed object in the path of every byte of the terminal's cousin.
    // `stalled` is a local rather than a read of React state, because this runs
    // once per inbound frame — sixty times a second on a desktop that is doing
    // anything — and a setState per frame is a render pass per frame even when
    // React bails out on the value.
    let stalled = false
    const onFrame = () => {
      liveness = sawBytes(liveness, Date.now())
      if (stalled) {
        stalled = false
        setState((s) => (s === 'stalled' ? 'live' : s))
      }
    }
    socket.addEventListener('message', onFrame)

    socket.addEventListener('close', (e) => {
      if (!live) return
      const next = stateForClose(e.code)
      setState(next)
      setDetail(e.reason)
      if (shouldRetry(next)) {
        failures.current += 1
        retryTimer = setTimeout(() => setAttempt((n) => n + 1), retryDelay(failures.current))
      }
    })

    void (async () => {
      const { default: RFB } = await import('@novnc/novnc')
      if (!live || !socket) return
      const client = new RFB(node, socket)
      rfb = client
      client.scaleViewport = true
      // Not resizeSession: the panel is a column somebody drags, and a display
      // that resizes the remote desktop on every drag rearranges the windows
      // of whoever is working on that machine.
      client.resizeSession = false
      client.background = 'var(--vp-surface-2)'
      client.addEventListener('connect', () => {
        if (!live) return
        failures.current = 0
        liveness = fresh(Date.now())
        setState('live')
      })
      client.addEventListener('disconnect', () => {
        // Handled on the socket's own close event, which carries the code and
        // the reason. This one has neither, and acting on both would overwrite
        // "the policy refused this address" with "disconnected".
      })
    })()

    const probeTimer = setInterval(() => {
      if (!live || !socket || socket.readyState !== WebSocket.OPEN) return
      const out = tick(liveness, Date.now())
      liveness = out.next
      if (out.probe) socket.send(PROBE)
      if (out.stalled && !stalled) {
        stalled = true
        setState((s) => (s === 'live' ? 'stalled' : s))
      }
    }, 1_000)

    return () => {
      live = false
      clearInterval(probeTimer)
      clearTimeout(retryTimer)
      rfb?.disconnect()
      socket?.removeEventListener('message', onFrame)
      socket?.close()
    }
  }, [target.id, attempt])

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div
        data-testid="vnc-status"
        data-state={state}
        className="flex shrink-0 items-center gap-1.5 px-2 py-1 text-vp-sm text-ink-2"
      >
        <DisplayGlyph state={state} />
        <span className="text-ink">{stateLabel(state)}</span>
        {target.viewOnly && <span data-testid="vnc-viewonly">· {t('vnc.viewOnly')}</span>}
        <span className="min-w-0 flex-1 truncate text-right" title={detail}>
          {detail}
        </span>
        {state !== 'live' && state !== 'connecting' && (
          <button
            type="button"
            data-testid="vnc-retry"
            onClick={retry}
            title={t('vnc.retry')}
            className="vp-press shrink-0 rounded-md p-1 text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
          >
            <RotateCw size={13} />
          </button>
        )}
      </div>
      {/* The canvas noVNC attaches to. `touch-none` so a drag on a phone moves
          the remote pointer instead of scrolling the panel out from under it. */}
      <div
        ref={holder}
        data-testid="vnc-screen"
        style={{ touchAction: 'none' }}
        className="min-h-0 flex-1 bg-surface-2"
      />
    </div>
  )
}

function stateLabel(state: DisplayState): string {
  if (state === 'live') return t('vnc.live')
  if (state === 'connecting') return t('vnc.connecting')
  if (state === 'stalled') return t('vnc.stalled')
  if (state === 'refused') return t('vnc.refused')
  return t('vnc.closed')
}

function tone(state: DisplayState): string {
  if (state === 'live') return 'var(--vp-state-done)'
  if (state === 'connecting') return 'var(--vp-state-waiting)'
  if (state === 'stalled') return 'var(--vp-state-waiting)'
  return 'var(--vp-state-crashed)'
}

/**
 * Five states, five shapes.
 *
 * Red line 4, and 'stalled' is the reason this widget needs its own glyph
 * rather than a colour change: "the desktop is not moving" and "the desktop
 * has stopped answering" are the two things a person has to tell apart at a
 * glance, and they differ by nothing else on screen. Pause bars for stalled,
 * against a filled dot for live.
 *
 * The other four deliberately reuse the shapes the share dashboard uses for
 * the same four meanings — an open arc for connecting, a dot in a ring for
 * live, a ring struck through for closed, a broken chain for something that is
 * not coming back. One vocabulary for connection state across the product.
 */
function DisplayGlyph({ state }: { state: DisplayState }) {
  const colour = tone(state)
  const label = stateLabel(state)
  const common = { width: 13, height: 13, viewBox: '0 0 24 24', role: 'img' as const }

  if (state === 'live') {
    return (
      <svg {...common} aria-label={label} className="vp-breathe shrink-0">
        <title>{label}</title>
        <circle cx="12" cy="12" r="9.5" fill="none" stroke={colour} strokeWidth="2" />
        <circle cx="12" cy="12" r="4.5" fill={colour} />
      </svg>
    )
  }
  if (state === 'connecting') {
    return (
      <svg {...common} aria-label={label} className="vp-breathe shrink-0">
        <title>{label}</title>
        <path
          d="M12 2.5 A9.5 9.5 0 1 1 5.3 5.3"
          fill="none"
          stroke={colour}
          strokeWidth="2.6"
          strokeLinecap="round"
        />
      </svg>
    )
  }
  if (state === 'stalled') {
    return (
      <svg {...common} aria-label={label} className="shrink-0">
        <title>{label}</title>
        <circle cx="12" cy="12" r="9.5" fill="none" stroke={colour} strokeWidth="2" />
        <path
          d="M9.5 8 L9.5 16 M14.5 8 L14.5 16"
          stroke={colour}
          strokeWidth="2.4"
          strokeLinecap="round"
        />
      </svg>
    )
  }
  if (state === 'closed') {
    return (
      <svg {...common} aria-label={label} className="shrink-0">
        <title>{label}</title>
        <circle cx="12" cy="12" r="9.5" fill="none" stroke={colour} strokeWidth="2.4" />
        <path d="M5.3 18.7 L18.7 5.3" stroke={colour} strokeWidth="2.4" strokeLinecap="round" />
      </svg>
    )
  }
  return (
    <svg {...common} aria-label={label} className="shrink-0">
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
