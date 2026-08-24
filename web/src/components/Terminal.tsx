import { useEffect, useRef, useState } from 'react'
import { Terminal as Xterm } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebLinksAddon } from '@xterm/addon-web-links'
import { Unicode11Addon } from '@xterm/addon-unicode11'
// xterm.css is imported from styles.css so our overrides come after it.

import type { PanelSocket } from '../protocol/socket'
import { terminalTheme } from './theme'

interface Props {
  socket: PanelSocket
  sessionId: string
  /** Redrawn when the palette changes, so the terminal follows the theme. */
  themeKey: string
  onTitle?: (title: string) => void
  onExit?: () => void
  className?: string
  /**
   * Stops xterm capturing keystrokes.
   *
   * Set on a phone, where tapping the terminal would otherwise raise the
   * software keyboard over the thing you were trying to read — and where
   * typing goes through the compose box instead, because an input method
   * cannot work against a raw terminal.
   */
  readOnly?: boolean
}

/**
 * One xterm instance bound to one session.
 *
 * Two sizing modes, decided by the server:
 *
 *   controlling — this viewer owns the grid. Fit to the container and tell the
 *                 server the new size.
 *   passive     — someone else owns it. Render at *their* grid and scale the
 *                 whole thing with a CSS transform to fit our container.
 *
 * The passive case is why this component does not simply call fit() on every
 * resize. Reflowing a shared session to whatever the smallest viewer happens to
 * be would rewrap the agent's TUI under the person actually using it — a phone
 * glancing at a session must not be able to do that to a desktop mid-edit.
 *
 * Passive viewers can still type. Only the explicit button below takes the
 * grid, because keystrokes are not a reliable signal of intent: xterm answers
 * device-attribute queries and focus reports through the same channel, so
 * "sent bytes" would mean "claimed the grid on page load".
 */
export function TerminalView({
  socket,
  sessionId,
  themeKey,
  onTitle,
  onExit,
  className,
  readOnly = false,
}: Props) {
  const hostRef = useRef<HTMLDivElement | null>(null)
  const wrapRef = useRef<HTMLDivElement | null>(null)
  const termRef = useRef<Xterm | null>(null)
  const fitRef = useRef<FitAddon | null>(null)
  const controllingRef = useRef(false)
  const [controlling, setControlling] = useState(false)
  const [grid, setGrid] = useState({ cols: 0, rows: 0 })

  // Terminal lifetime is tied to the session, never to the theme or to
  // callbacks: recreating it would clear the screen and lose the scrollback the
  // replay just restored.
  useEffect(() => {
    const host = hostRef.current
    if (!host) return

    const term = new Xterm({
      allowProposedApi: true,
      disableStdin: readOnly,
      convertEol: false,
      cursorBlink: true,
      cursorStyle: 'bar',
      fontFamily: getComputedStyle(document.documentElement).getPropertyValue('--font-mono').trim(),
      fontSize: 13,
      lineHeight: 1.2,
      scrollback: 10_000,
      theme: terminalTheme(),
      // The panel owns the scroll position on mobile, where the browser would
      // otherwise fight the touch-selection layer for the same gesture.
      smoothScrollDuration: 0,
    })

    const fit = new FitAddon()
    term.loadAddon(fit)
    term.loadAddon(new WebLinksAddon())

    const unicode = new Unicode11Addon()
    term.loadAddon(unicode)
    // Agent output is full of box drawing and emoji. Without the v11 provider
    // xterm measures those at the wrong width and every table drifts.
    term.unicode.activeVersion = '11'

    term.open(host)
    termRef.current = term
    fitRef.current = fit

    const encoder = new TextEncoder()

    // While scrollback is being parsed, anything the terminal wants to send
    // back is an answer to a question that was asked minutes ago and has
    // already been answered. Letting it through types the reply at whatever
    // prompt the session is sitting at.
    let replaying = false

    const dataSub = term.onData((data) => {
      if (replaying) return
      socket.write(sessionId, encoder.encode(data))
    })
    // Binary input is what arrives for pasted bytes that are not valid UTF-16
    // text; without this branch those keystrokes vanish.
    const binarySub = term.onBinary((data) => {
      if (replaying) return
      const bytes = new Uint8Array(data.length)
      for (let i = 0; i < data.length; i++) bytes[i] = data.charCodeAt(i) & 0xff
      socket.write(sessionId, bytes)
    })

    socket.subscribe(sessionId, term.cols, term.rows, {
      onData: (bytes, replay) => {
        if (!replay) {
          term.write(bytes)
          return
        }
        // xterm generates its responses synchronously while parsing, so the
        // flag has to span the whole write and is cleared from the parse
        // callback rather than on the next line.
        replaying = true
        term.write(bytes, () => {
          replaying = false
        })
      },
      onSize: (cols, rows, isControlling) => {
        controllingRef.current = isControlling
        setControlling(isControlling)
        setGrid({ cols, rows })
        if (term.cols !== cols || term.rows !== rows) term.resize(cols, rows)
      },
      onTitle: (t) => onTitle?.(t),
      onClipboard: (text) => {
        // OSC 52 arrived: the pane copied something. Push it to the real
        // clipboard so a copy inside tmux lands where the user expects.
        navigator.clipboard?.writeText(text).catch(() => {
          /* denied without a user gesture; nothing useful to do */
        })
      },
      onExit: () => onExit?.(),
    })

    return () => {
      dataSub.dispose()
      binarySub.dispose()
      socket.unsubscribe(sessionId)
      term.dispose()
      termRef.current = null
      fitRef.current = null
    }
    // onTitle/onExit are intentionally excluded: a new callback identity must
    // not tear down and rebuild the terminal.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [socket, sessionId, readOnly])

  // Repaint the palette in place when the theme changes.
  useEffect(() => {
    if (termRef.current) termRef.current.options.theme = terminalTheme()
  }, [themeKey])

  // Resize handling for both modes.
  useEffect(() => {
    const wrap = wrapRef.current
    const host = hostRef.current
    const term = termRef.current
    if (!wrap || !host || !term) return

    const apply = () => {
      const t = termRef.current
      const f = fitRef.current
      if (!t || !f) return

      if (controllingRef.current) {
        // We own the grid: fit to the container and publish the result.
        host.style.transform = ''
        host.style.width = '100%'
        host.style.height = '100%'
        const dims = f.proposeDimensions()
        if (dims && dims.cols > 0 && dims.rows > 0) {
          f.fit()
          socket.resize(sessionId, t.cols, t.rows)
        }
        return
      }

      // Passive: keep the authoritative grid and scale it into our box. Scaling
      // never exceeds 1 — blowing a small grid up to fill a large monitor looks
      // broken, and leaving it small is honest about what is really there.
      host.style.transform = 'none'
      host.style.width = 'max-content'
      host.style.height = 'max-content'
      const natural = host.getBoundingClientRect()
      if (natural.width === 0 || natural.height === 0) return
      const scale = Math.min(wrap.clientWidth / natural.width, wrap.clientHeight / natural.height, 1)
      host.style.transformOrigin = 'top left'
      host.style.transform = `scale(${scale})`
    }

    apply()
    const ro = new ResizeObserver(apply)
    ro.observe(wrap)
    return () => ro.disconnect()
  }, [socket, sessionId, controlling, grid.cols, grid.rows])

  const takeControl = () => {
    const t = termRef.current
    const f = fitRef.current
    const wrap = wrapRef.current
    if (!t || !f || !wrap) return
    const host = hostRef.current
    if (host) {
      host.style.transform = ''
      host.style.width = '100%'
      host.style.height = '100%'
    }
    const dims = f.proposeDimensions()
    if (dims && dims.cols > 0 && dims.rows > 0) {
      socket.resize(sessionId, dims.cols, dims.rows, true)
    }
  }

  return (
    <div ref={wrapRef} className={`relative overflow-hidden ${className ?? ''}`}>
      <div ref={hostRef} className="h-full w-full" />
      {!controlling && grid.cols > 0 && (
        <button
          type="button"
          data-testid="take-control"
          onClick={takeControl}
          className="absolute right-3 bottom-3 rounded-full border border-hairline bg-elevated px-3 py-1.5 text-xs text-ink-2 backdrop-blur transition-colors duration-200 ease-vp hover:text-ink"
          title={`Another viewer owns this grid (${grid.cols}x${grid.rows}). You can still type; click to resize it to your window.`}
        >
          <span className="tabular">
            {grid.cols}x{grid.rows}
          </span>{' '}
          · take control
        </button>
      )}
    </div>
  )
}
