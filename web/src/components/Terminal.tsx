import { useEffect, useRef, useState } from 'react'
import { attachTouchSelection } from './mobile/touchSelect'
import { rendererPreference } from './renderer'
import { Terminal as Xterm } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebLinksAddon } from '@xterm/addon-web-links'
import { Unicode11Addon } from '@xterm/addon-unicode11'
import { WebglAddon } from '@xterm/addon-webgl'
// xterm.css is imported from styles.css so our overrides come after it.

import type { PanelSocket } from '../protocol/socket'
import { terminalTheme } from './theme'
import { t, useLang } from '../i18n'
import { filesFrom } from './upload'

/**
 * The smallest a passive viewer is allowed to shrink the text to.
 *
 * Nine pixels is not comfortable, but it is still letters. Below that the
 * question stops being "can I read this" and becomes "is there anything
 * there", and a phone looking at a desktop-sized grid was answering the
 * second one.
 */
const MIN_LEGIBLE_FONT_PX = 9

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
  /**
   * Files pasted into the terminal, which for a coding agent means a
   * screenshot.
   *
   * xterm's own paste handling is for text, so an image landed nowhere at all:
   * ctrl-V did nothing and there was no way to tell whether the panel had
   * ignored it or the clipboard was empty. Dropping a file already worked --
   * this is the same journey for people who took a screenshot rather than saved
   * one, which on every desktop is the faster half.
   */
  onPasteFiles?: (files: File[]) => void
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
/**
 * Every live terminal, by session id, so that what is on screen can be read
 * back from outside the component.
 *
 * The WebGL renderer draws to a canvas. `.xterm-rows` is empty under it, and
 * every browser check that read the screen through the DOM went blind the
 * moment the renderer was loaded -- thirteen of them at once, each reporting an
 * empty terminal about a terminal that was full. The screen had not changed;
 * the only readable copy of it had.
 *
 * So the buffer is the source, which is the more truthful one anyway: the DOM
 * spans were a rendering of the buffer, and a check that reads the rendering
 * can be fooled by the renderer. `vibepanelScreen` reads and cannot write, and
 * it is the same text xterm's own accessibility layer would expose without
 * turning screen-reader mode on for everybody.
 */
const liveTerminals = new Map<string, Xterm>()

declare global {
  interface Window {
    vibepanelScreen?: (arg?: string | { id?: string; all?: boolean }) => string[] | null
  }
}

if (typeof window !== 'undefined') {
  window.vibepanelScreen = (arg) => {
    const opts = typeof arg === 'string' ? { id: arg } : (arg ?? {})
    const all = [...liveTerminals.values()]
    // The focused one when nothing is named. A check that has just typed into a
    // terminal means *that* terminal, and the panel has several: a main one and
    // a row of scratch ones underneath. Picking the first in the map read the
    // wrong screen and reported that typing had produced no output.
    const term = opts.id
      ? liveTerminals.get(opts.id)
      : (all.find((t) => t.textarea === document.activeElement) ?? all[0])
    if (!term) return null
    const buf = term.buffer.active
    // The viewport by default, which is what "what is on screen" means and what
    // the DOM used to return. `all` reaches the scrollback, for the checks that
    // ask whether something from a minute ago survived.
    const from = opts.all ? 0 : buf.viewportY
    const to = opts.all ? buf.length : Math.min(buf.length, buf.viewportY + term.rows)
    const rows: string[] = []
    for (let i = from; i < to; i++) rows.push(buf.getLine(i)?.translateToString(true) ?? '')
    return rows
  }
}

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
  onPasteFiles,
}: Props) {
  useLang()
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
  const onPasteFilesRef = useRef(onPasteFiles)
  useEffect(() => {
    onSelectionRef.current = onSelectionChange
    onClipboardRef.current = onClipboard
    onPasteFilesRef.current = onPasteFiles
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
      // 1.0, not 1.2.
      //
      // xterm paints the background per cell, so any leading shows up as a
      // horizontal gap through coloured output -- and the box-drawing an agent
      // uses for its panels stops joining up, which reads as the terminal being
      // chopped into strips. A terminal is a grid; the leading belongs inside
      // the glyph, not between the rows.
      lineHeight: 1.0,
      // Draw box-drawing and block characters geometrically instead of taking
      // them from the font.
      //
      // This is the other half of the cracks. A font's ─ and │ are glyphs sized
      // for their own metrics, and at a fractional cell width two of them side
      // by side do not meet: the join shows a hairline of background through
      // it, and an agent's panel border reads as dashed. Drawn as geometry they
      // are computed from the cell box and always meet. Only the canvas and
      // WebGL renderers honour this, which is the other reason one has to be
      // loaded.
      customGlyphs: true,
      // A glyph wider than its cell is squeezed to fit rather than allowed to
      // spill over its neighbour. CJK and emoji in a Latin-metric font do this
      // constantly, and the spill is what makes a column of them look ragged.
      rescaleOverlappingGlyphs: true,
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
    liveTerminals.set(sessionId, term)

    // The renderer, which was never loaded.
    //
    // @xterm/addon-webgl has been a dependency since the beginning and nothing
    // called loadAddon, so every session has been drawing on xterm's DOM
    // renderer: one span per cell, each with its own background, positioned in
    // CSS pixels. At any cell size that is not a whole number those spans do
    // not quite touch, and the gaps are visible as fine cracks through
    // anything solid -- block-character art, a filled progress bar, an agent's
    // panel border. It is the defect that reads as "the terminal looks broken"
    // without pointing at anything in particular, and the fix was one line of
    // an addon already in package.json.
    //
    // Loaded after open(), which is required: the addon needs the element.
    //
    // A GPU context can be refused or lost -- a laptop switching graphics, a
    // driver reset, too many contexts across tabs -- and a WebGL renderer that
    // loses its context draws nothing at all. Disposing on loss puts xterm back
    // on the DOM renderer, which is worse-looking and still works, and that is
    // the right trade for a terminal somebody is reading.
    if (rendererPreference() !== 'dom') {
      try {
        const webgl = new WebglAddon()
        webgl.onContextLoss(() => {
          webgl.dispose()
        })
        term.loadAddon(webgl)
      } catch {
        // No WebGL here at all: a headless browser without a GPU, or a policy
        // that blocks it. The DOM renderer is already in place.
      }
    }
    termRef.current = term
    fitRef.current = fit

    const encoder = new TextEncoder()

    // While scrollback is being parsed, anything the terminal wants to send
    // back is an answer to a question that was asked minutes ago and has
    // already been answered. Letting it through types the reply at whatever
    // prompt the session is sitting at.
    let replaying = false
    // Set when the stream restarts, cleared by the snapshot that follows.
    let replaceOnNextReplay = false

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
      // The subscription restarted, so the snapshot that follows is the whole
      // buffer again rather than a continuation. Appending it leaves the
      // session's history in the browser two copies deep — after every
      // reconnect, and after the server cuts a viewer off for falling behind.
      //
      // Armed rather than acted on: if no snapshot follows (an empty ring
      // buffer on a server that has just started), clearing here would blank a
      // terminal that still had something worth reading in it.
      onReset: () => {
        replaceOnNextReplay = true
      },
      onData: (bytes, replay) => {
        if (!replay) {
          term.write(bytes)
          return
        }
        // xterm generates its responses synchronously while parsing, so the
        // flag has to span the whole write — and the reset, which restores
        // modes the session never asked about — and is cleared from the parse
        // callback rather than on the next line.
        replaying = true
        if (replaceOnNextReplay) {
          replaceOnNextReplay = false
          // reset() rather than clear(): the snapshot may enter the alternate
          // screen or set modes of its own, and it has to be parsed against a
          // terminal in a known state rather than whatever the previous stream
          // left behind.
          term.reset()
        }
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
    // Copy on select, the way a terminal does.
    //
    // Selecting text and then having to press something for it to be on the
    // clipboard is the step nobody expects: every terminal emulator worth using
    // copies on selection, and a web one that does not feels broken rather than
    // careful. The browser allows the write because finishing a drag *is* the
    // user gesture it asks for -- which is exactly what the OSC 52 path does not
    // have, and why that one still has to offer a button.
    //
    // On pointerup rather than on xterm's selection event: that fires
    // continuously while the pointer moves, so copying there would write to the
    // clipboard dozens of times per drag and leave whatever the last frame
    // happened to cover.
    const copyOnSelect = () => {
      if (!term.hasSelection()) return
      const text = term.getSelection()
      if (!text) return
      navigator.clipboard?.writeText(text).catch(() => {
        // Refused anyway -- a non-secure context, or a browser that wants more
        // than a gesture. Fall through to the offer that predates this, rather
        // than losing the text silently.
        onClipboardRef.current?.(text, false)
      })
    }
    host.addEventListener('pointerup', copyOnSelect)

    // A screenshot pasted into the terminal.
    //
    // Capture phase, because xterm's own handler is on the hidden textarea and
    // would otherwise get there first. Only files are taken: text paste is
    // xterm's job and it does it correctly, including asking the pane whether
    // it wants bracketing.
    const onPaste = (e: ClipboardEvent) => {
      const files = filesFrom(e.clipboardData)
      if (files.length === 0) return
      // Only now: preventing the default for a text paste would break the
      // ordinary case in order to serve the rare one.
      e.preventDefault()
      onPasteFilesRef.current?.(files)
    }
    host.addEventListener('paste', onPaste, true)

    const detachTouch = touchSelect ? attachTouchSelection(host, term) : undefined

    return () => {
      liveTerminals.delete(sessionId)
      detachTouch?.()
      host.removeEventListener('pointerup', copyOnSelect)
      host.removeEventListener('paste', onPaste, true)
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
        wrap.style.overflow = ''
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
      const fit = Math.min(wrap.clientWidth / natural.width, wrap.clientHeight / natural.height, 1)

      // Scaling to fit is right until the text stops being text.
      //
      // Measured on a phone (390 wide) watching a session a 1920 desktop
      // owned: scale 0.29, a 13px font rendered at under four pixels — a grey
      // smear with no glyphs in it — while more than a thousand vertical
      // pixels underneath sat empty, because width was the only binding
      // constraint. The whole grid was in the top one per cent of the screen
      // and unreadable, which is not "displayed smaller", it is gone.
      //
      // So the floor buys legibility with the space that was being wasted
      // anyway, and the width that no longer fits is panned to.
      const legible = Math.min(1, MIN_LEGIBLE_FONT_PX / (t.options.fontSize ?? 13))
      const scale = Math.max(fit, legible)
      // Only when the floor actually bit. Leaving the box scrollable when
      // everything fits invents a scrollbar and a way to push the terminal off
      // the side of its own container.
      wrap.style.overflow = scale > fit ? 'auto' : ''
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
          className="vp-press absolute right-3 bottom-3 rounded-full border border-hairline bg-elevated px-3 py-1.5 text-vp-xs text-ink-2 backdrop-blur transition-colors duration-200 ease-vp hover:text-ink"
          title={t('term.takeControlWhy', {
            cols: grid.cols,
            rows: grid.rows,
            mine: `${ownFit.cols}×${ownFit.rows}`,
          })}
        >
          <span className="tabular">
            {grid.cols}×{grid.rows}
          </span>{' '}
          · {t('term.takeControl')}
        </button>
      )}
    </div>
  )
}
