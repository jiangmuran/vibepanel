import { useEffect, useRef, useState } from 'react'
import { attachTouchSelection } from './mobile/touchSelect'
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
  /**
   * The pane copied something over OSC 52. `ok` says whether it reached the
   * system clipboard; when it did not, the text needs offering to the user
   * behind a click.
   */
  onClipboard?: (text: string, ok: boolean) => void
  /**
   * Enables press-and-hold selection with a finger. Off by default: the
   * gesture only makes sense where there is no pointer to drag with, and
   * attaching it on a desktop would fight xterm's own mouse selection.
   */
  touchSelect?: boolean
  /** Fires with the selected text, or '' when the selection is dropped. */
  onSelectionChange?: (text: string) => void
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
  touchSelect = false,
  onSelectionChange,
  onClipboard,
}: Props) {
  const hostRef = useRef<HTMLDivElement | null>(null)
  const wrapRef = useRef<HTMLDivElement | null>(null)
  const termRef = useRef<Xterm | null>(null)
  const fitRef = useRef<FitAddon | null>(null)
  const controllingRef = useRef(false)
  const [controlling, setControlling] = useState(false)
  const [grid, setGrid] = useState({ cols: 0, rows: 0 })
  // What this window would render if it owned the grid. Only meaningful while
  // passive, and only so the offer to take over can be withheld when there is
  // nothing to gain by it.
  const [ownFit, setOwnFit] = useState({ cols: 0, rows: 0 })
  // Held in a ref for the same reason onTitle/onExit are not in the effect's
  // deps: a new callback identity must not rebuild the terminal.
  const onSelectionRef = useRef(onSelectionChange)
  const onClipboardRef = useRef(onClipboard)
  useEffect(() => {
    onSelectionRef.current = onSelectionChange
    onClipboardRef.current = onClipboard
  })

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
        //
        // This write is not inside a user gesture, and browsers say no to
        // that: Chromium rejects it with NotAllowedError unless the page
        // holds clipboard-write, Firefox and Safari require an activation,
        // and over plain http `navigator.clipboard` does not exist at all.
        // Swallowing the failure meant copying inside tmux did nothing and
        // said nothing, so the outcome is reported and the shell offers the
        // click that makes it legal.
        const clip = navigator.clipboard
        if (!clip) {
          onClipboardRef.current?.(text, false)
          return
        }
        clip.writeText(text).then(
          () => onClipboardRef.current?.(text, true),
          () => onClipboardRef.current?.(text, false),
        )
      },
      onExit: () => onExit?.(),
    })

    // Selection is reported from xterm's own event rather than the DOM's,
    // because the gesture drives xterm's selection model and never touches
    // window.getSelection().
    const selSub = term.onSelectionChange(() => {
      onSelectionRef.current?.(term.getSelection())
    })
    const detachTouch = touchSelect ? attachTouchSelection(host, term) : undefined

    return () => {
      detachTouch?.()
      selSub.dispose()
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
  }, [socket, sessionId, readOnly, touchSelect])

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

      // Passive. Before scaling anything, measure what this window would render
      // if it owned the grid — with the host still filling the box, because the
      // fit addon reads the element it is given. Measured after the transform
      // is applied it would only ever report the grid already on screen, which
      // is the question nobody is asking.
      host.style.transform = ''
      host.style.width = '100%'
      host.style.height = '100%'
      const mine = f.proposeDimensions()
      setOwnFit((prev) =>
        mine && mine.cols > 0 && (prev.cols !== mine.cols || prev.rows !== mine.rows)
          ? { cols: mine.cols, rows: mine.rows }
          : prev,
      )

      // Now the display: keep the authoritative grid and scale it into our box.
      // Scaling never exceeds 1 — blowing a small grid up to fill a large
      // monitor looks broken, and leaving it small is honest about what is
      // really there.
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

  // Offer to take the grid only when this window would actually render a
  // different one. Two windows the same size see an identical picture, so a
  // permanent call-to-action over the terminal would be inviting them to
  // trade ownership back and forth for no visible change.
  const offerControl =
    !controlling &&
    grid.cols > 0 &&
    ownFit.cols > 0 &&
    (ownFit.cols !== grid.cols || ownFit.rows !== grid.rows)

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
      {offerControl && (
        <button
          type="button"
          data-testid="take-control"
          onClick={takeControl}
          className="absolute right-3 bottom-3 rounded-full border border-hairline bg-elevated px-3 py-1.5 text-xs text-ink-2 backdrop-blur transition-colors duration-200 ease-vp hover:text-ink"
          title={
            `Another viewer owns this grid (${grid.cols}x${grid.rows}); this window fits ` +
            `${ownFit.cols}x${ownFit.rows}. You can still type. Taking over reflows it for ` +
            `everyone, which a running TUI will notice.`
          }
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
