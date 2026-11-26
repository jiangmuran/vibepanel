import { decodeData, encodeData } from './wire'
import type { ClientMessage, LoadTiming, PanelState, ResourceAction, ResourceAlert, ServerMessage, StateMessage, SessionNotice } from './wire'

/** What a subscriber to one session receives. */
export interface StreamHandlers {
  /** `replay` marks buffered scrollback rather than live output. */
  onData: (bytes: Uint8Array, replay: boolean) => void
  onSize: (cols: number, rows: number, controlling: boolean) => void
  onTitle?: (title: string) => void
  onClipboard?: (text: string) => void
  onExit?: () => void
  /**
   * A subscription is starting again, and the scrollback that follows is a
   * complete snapshot rather than a continuation.
   *
   * Called before the replay arrives, on a resubscribe only — after a
   * reconnect, or after the server cut this viewer off for falling behind.
   * Without it the snapshot is appended to what is already on screen and the
   * terminal shows its whole history twice.
   */
  onReset?: () => void
  /**
   * Called before replay frames arrive, with the exact snapshot size.
   *
   * `resumed` means the replay carries on from what the terminal already
   * shows, and onReset was not called: it is the output missed while the
   * connection was down, not the whole buffer again.
   */
  onSubscribed?: (info: { replayBytes: number; replayChunks: number; resumed: boolean }) => void
  /** Called when the server rejects a request for this stream (e.g. subscribe failed). */
  onError?: (message: string) => void
}

interface Stream {
  sessionId: string
  handlers: StreamHandlers
  ref: number | null
  /** Viewport last reported, replayed after a reconnect. */
  cols: number
  rows: number
  /**
   * Whether the server has confirmed this subscription before. The next
   * confirmation is then a restart, and the replay behind it replaces the
   * terminal's contents instead of extending them.
   */
  confirmed: boolean
  /**
   * Mounted off-screen. Carried on every subscribe, the ones a reconnect sends
   * included, so the server never takes a hidden terminal for a viewer
   * arriving. See setHidden.
   */
  hidden: boolean
  /**
   * Where this terminal is in the session's output: the stream the server
   * named, and the offset of the next byte not yet received. Every frame for
   * this stream advances it, replay and live alike, because the server's ring
   * holds exactly the bytes it broadcasts. Null until the first confirmation.
   */
  position: { stream: string; next: number } | null
  /**
   * The server said this stream's attachment ended, and nothing has replaced
   * it: the tmux session was killed, or went with the machine. The terminal
   * is still mounted and still wants the session, so when a state push shows
   * the session running again -- restored, under the same id -- the
   * subscription is sent again. Without this a restored session showed the
   * old stream's last frame, "[exited]", until the page was reloaded, and
   * typing into it went nowhere.
   */
  ended: boolean
  /** Timestamp when an unconfirmed `subscribe` was sent on an open socket, or 0. */
  awaitingSubSince: number
  /** Triggers a liveness probe if `subscribed` does not arrive promptly. */
  ackTimer: ReturnType<typeof setTimeout> | null
}

/** Connection state, for the UI to show honestly rather than pretending. */
export type SocketStatus = 'connecting' | 'open' | 'closed'

const MAX_BACKOFF_MS = 10_000
const PING_INTERVAL_MS = 25_000
/**
 * How long to wait for any frame after actually sending a ping before treating
 * the socket as half-open and replacing it.
 *
 * Checking `Date.now() - lastSeenAt > 60_000` *before* pinging broke on desktop
 * Chrome: background-tab timer throttling delays `setInterval` to 60 s, so a
 * healthy idle socket woke up at 60 s and killed itself before sending a single
 * ping, then sat in WebSocket.CLOSING for another 60 s. Waiting for an answer
 * to a ping that was actually sent detects dead sockets in seconds without
 * false-killing throttled tabs.
 */
const PING_TIMEOUT_MS = 10_000
const WAKE_PING_TIMEOUT_MS = 5_000
const SUBSCRIBE_ACK_TIMEOUT_MS = 5_000
const CONNECT_STALL_MS = 15_000

/**
 * How long without a single byte from the server before the connection is
 * treated as dead.
 *
 * The client pings and the server answers, so silence this long means two
 * answers went missing. It has to be an application-level exchange: the
 * protocol-level ping the server sends every 30s is answered by the browser
 * itself and is invisible to script, so a socket can be perfectly idle at this
 * layer while the network underneath it has been gone for minutes.
 *
 * Not shorter, because the cost of noticing quickly is paid by a phone's radio
 * — this panel is meant to be left open on one. The fast path for the case
 * that actually happens to a phone is the `offline` event below, which is
 * immediate; this is the backstop for a connection that dies without the
 * operating system noticing, which is what a NAT timeout or a wedged proxy
 * looks like.
 */
/**
 * This viewer's identity, stable across reconnects and reloads.
 *
 * The server used to mint one per connection, which made a returning viewer a
 * stranger. That matters because grid ownership is held by a client id: when
 * the owner's connection ends the grid is frozen and released, and only the
 * owner is meant to pick it up again without asking. With a fresh id every
 * time, "the owner is back" was indistinguishable from "somebody else turned
 * up", and a phone doing nothing but reloading took a desktop's grid and
 * reflowed the agent from 112 columns to 46.
 *
 * sessionStorage rather than localStorage, deliberately: it survives a reload
 * and a dropped socket, and it does *not* leak across tabs. Two tabs of the
 * same browser are two viewers, possibly two different sizes, and must not
 * claim each other's grid. A closed tab is a viewer that is not coming back,
 * and its id goes with it.
 *
 * Falls back to a per-load id where storage is unavailable — a private window
 * with site data blocked still has to work, it just loses the reclaim.
 */
const CLIENT_KEY = 'vibepanel.clientId'
let cachedClientID = ''
function clientID(): string {
  if (cachedClientID) return cachedClientID
  const fresh = () =>
    `c${Math.random().toString(36).slice(2, 10)}${Date.now().toString(36)}`
  try {
    const stored = sessionStorage.getItem(CLIENT_KEY)
    if (stored) {
      cachedClientID = stored
      return stored
    }
    cachedClientID = fresh()
    sessionStorage.setItem(CLIENT_KEY, cachedClientID)
  } catch {
    cachedClientID = fresh()
  }
  return cachedClientID
}

export class PanelSocket {
  private ws: WebSocket | null = null
  private streams = new Map<string, Stream>()
  private byRef = new Map<number, Stream>()
  private backoff = 500
  private pingTimer: number | null = null
  private pingDeadline: ReturnType<typeof setTimeout> | null = null
  private closed = false
  // The pending reconnect, so close() can cancel it. Without that a socket
  // closed while a retry was scheduled reopened itself a moment later.
  private retry: ReturnType<typeof setTimeout> | undefined
  /** When the last frame of any kind arrived. */
  private lastSeenAt = 0
  /** When the current WebSocket handshake started, to recover from stalled CONNECTING across sleep. */
  private connectStartedAt = 0
  private statusListeners = new Set<(s: SocketStatus) => void>()

  status: SocketStatus = 'closed'

  private stateListeners = new Set<(s: PanelState) => void>()
  private panelListeners = new Set<(projectId: string, kind: string) => void>()
  private resourceListeners = new Set<(alert: ResourceAlert | null, acted: ResourceAction | null) => void>()
  private noticeListeners = new Set<(n: SessionNotice) => void>()
  private errorListeners = new Set<(sessionId: string, message: string) => void>()

  constructor() {
    this.attachLifecycle()
  }

  private attachLifecycle() {
    // The browser knows before we can: a phone leaving coverage, wifi
    // dropping, the laptop being closed. Without this the status dot stays
    // green — the socket is not closed, it is simply never going to hear
    // anything again — and the panel goes on claiming to be live while the
    // list of who needs you quietly stops updating. That is the worst way for
    // this particular application to fail, because nothing looks wrong.
    window.addEventListener('offline', this.onOffline)
    window.addEventListener('online', this.onOnline)
    window.addEventListener('pageshow', this.onPageShow)
    if (typeof document !== 'undefined' && typeof document.addEventListener === 'function') {
      document.addEventListener('visibilitychange', this.onVisibilityChange)
    }
  }

  private detachLifecycle() {
    window.removeEventListener('offline', this.onOffline)
    window.removeEventListener('online', this.onOnline)
    window.removeEventListener('pageshow', this.onPageShow)
    if (typeof document !== 'undefined' && typeof document.removeEventListener === 'function') {
      document.removeEventListener('visibilitychange', this.onVisibilityChange)
    }
  }

  private onOffline = () => {
    if (this.closed) return
    this.dropConnection()
  }

  private onOnline = () => {
    if (this.closed || this.ws) return
    // Back on the network: retry now rather than sitting out the remainder of
    // a backoff that was measured against a problem that has gone away.
    this.backoff = 500
    this.connect()
  }

  private onVisibilityChange = () => {
    if (this.closed || typeof document === 'undefined') return
    if (!document.hidden) this.checkAliveOnWake()
  }

  private onPageShow = () => {
    if (this.closed) return
    if (typeof document === 'undefined' || !document.hidden) this.checkAliveOnWake()
  }

  /**
   * Re-verify liveness when a tab returns to the foreground (or wakes from
   * laptop sleep / mobile lock). If the socket already closed, reconnect
   * immediately instead of sitting out backoff; if it looks open after an idle
   * gap, probe immediately so a half-open connection is replaced in seconds
   * rather than waiting for the next 25 s ping interval.
   */
  private checkAliveOnWake() {
    if (this.closed) return
    const ws = this.ws
    if (!ws) {
      if (this.retry !== undefined) {
        this.backoff = 500
        this.connect()
      }
      return
    }
    if (ws.readyState === WebSocket.OPEN) {
      const now = Date.now()
      const idleLong = this.lastSeenAt > 0 && now - this.lastSeenAt > PING_INTERVAL_MS
      const stalledSub = [...this.streams.values()].some(
        (s) => !s.hidden && s.awaitingSubSince > 0 && now - s.awaitingSubSince >= SUBSCRIBE_ACK_TIMEOUT_MS,
      )
      if (idleLong || stalledSub) {
        this.probe(WAKE_PING_TIMEOUT_MS)
      }
      return
    }
    if (ws.readyState === WebSocket.CONNECTING) {
      if (this.connectStartedAt > 0 && Date.now() - this.connectStartedAt > CONNECT_STALL_MS) {
        this.dropConnection(true)
      }
      return
    }
    // CLOSING or CLOSED: detach and open a fresh socket right away.
    this.dropConnection(true)
  }

  private scheduleReconnect() {
    if (this.closed || this.retry !== undefined) return
    this.retry = setTimeout(() => this.connect(), this.backoff)
    this.backoff = Math.min(this.backoff * 2, MAX_BACKOFF_MS)
  }

  /**
   * Detach a dead or offline socket immediately so a half-open CLOSING
   * handshake cannot block reconnection for ~60 s.
   *
   * Leaving `this.ws` set until `ws.onclose` fired kept Chromium's 60 s close
   * timeout blocking `connect()` and `onOnline()`, leaving every newly clicked
   * terminal stuck at "Connecting terminal 0%" for 40-67 s. Detaching here lets
   * the stale socket finish closing in the background (its late `onclose` is
   * ignored by the `this.ws !== ws` guard) while a fresh socket opens at once.
   */
  private dropConnection(reconnectNow = false) {
    const ws = this.ws
    this.ws = null
    this.stopPing()
    for (const stream of this.streams.values()) {
      stream.ref = null
      this.clearAckTimer(stream)
    }
    this.byRef.clear()
    if (ws) {
      try {
        ws.close()
      } catch {
        // Best effort: the socket is already detached.
      }
    }
    this.setStatus('closed')
    if (this.closed) return
    if (reconnectNow) {
      clearTimeout(this.retry)
      this.retry = undefined
      this.backoff = 500
      this.connect()
      return
    }
    this.scheduleReconnect()
  }

  connect() {
    if (this.ws) return
    if (this.closed) {
      // Opened again after close(). React's StrictMode does exactly that in
      // development -- effect, cleanup, effect -- on the one socket App keeps
      // in a useMemo, and `closed` was never reset: under `npm run dev` the
      // panel never connected at all. Production runs the effect once, which
      // is why nothing else saw it.
      this.closed = false
      this.attachLifecycle()
    }
    clearTimeout(this.retry)
    this.retry = undefined
    this.connectStartedAt = Date.now()
    this.setStatus('connecting')

    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
    const ws = new WebSocket(`${proto}//${location.host}/ws?client=${encodeURIComponent(clientID())}`)
    ws.binaryType = 'arraybuffer'
    this.ws = ws

    ws.onopen = () => {
      if (this.ws !== ws) return
      this.backoff = 500
      this.lastSeenAt = Date.now()
      this.setStatus('open')
      // Re-subscribe everything, visible terminals ahead of off-screen ones so
      // the pane on screen never waits behind multi-megabyte hidden replays.
      for (const hidden of [false, true]) {
        for (const stream of this.streams.values()) {
          if (stream.hidden !== hidden) continue
          stream.ref = null
          this.sendSubscribe(stream)
        }
      }
      this.startPing()
    }

    ws.onmessage = (ev) => {
      if (this.ws !== ws) return
      // Any frame, of any kind, is proof the connection is alive.
      this.lastSeenAt = Date.now()
      this.clearPingDeadline()
      if (ev.data instanceof ArrayBuffer) {
        this.handleFrame(ev.data)
        return
      }
      let msg: ServerMessage
      try {
        msg = JSON.parse(ev.data as string) as ServerMessage
      } catch {
        return
      }
      this.handleControl(msg)
    }

    ws.onclose = () => {
      // A socket close() let go of can still report its close afterwards, and
      // must not take the status or the streams of the one opened since.
      if (this.ws !== ws) return
      this.ws = null
      this.stopPing()
      for (const stream of this.streams.values()) {
        stream.ref = null
        this.clearAckTimer(stream)
      }
      this.byRef.clear()
      this.setStatus('closed')
      this.scheduleReconnect()
    }

    ws.onerror = () => {
      // onclose always follows, and that is where reconnection is handled.
      // Closing here as well would double the backoff on every failure.
    }
  }

  private handleFrame(buf: ArrayBuffer) {
    const frame = decodeData(buf)
    if (!frame) return
    const stream = this.byRef.get(frame.ref)
    if (!stream) return
    if (stream.position) stream.position.next += frame.payload.byteLength
    stream.handlers.onData(frame.payload, frame.replay)
  }

  /**
   * Whether an end-of-stream message is about a stream this one has already
   * replaced. Matched by session id alone, an `exit` the server sent for the
   * subscription a remount had just cancelled ended the new one: a live
   * session drawn as exited. A message with no ref is believed, as before.
   */
  private stale(stream: Stream, ref: number | undefined): boolean {
    return ref !== undefined && stream.ref !== ref
  }

  private handleControl(msg: ServerMessage) {
    switch (msg.t) {
      case 'subscribed': {
        const stream = msg.sessionId ? this.streams.get(msg.sessionId) : undefined
        if (!stream || msg.ref === undefined) return
        this.clearAckTimer(stream)
        // A second confirmation for the same session means the subscription
        // restarted, so the replay that follows is the whole buffer again.
        // Clearing here rather than on the replay frame keeps this correct
        // whether the snapshot arrives in one frame or several.
        //
        // Unless the server resumed it, in which case the replay is only what
        // was missed and the terminal keeps what it has. Believed only for the
        // stream this terminal was actually counting: anything else is the
        // whole buffer, whatever the flag says.
        const resumed =
          msg.resumed === true && stream.position !== null && stream.position.stream === msg.replayStream
        if (stream.confirmed && !resumed) stream.handlers.onReset?.()
        stream.confirmed = true
        stream.ended = false
        stream.ref = msg.ref
        stream.position = msg.replayStream ? { stream: msg.replayStream, next: msg.replayOffset ?? 0 } : null
        this.byRef.set(msg.ref, stream)
        stream.handlers.onSubscribed?.({
          replayBytes: msg.replayBytes ?? 0,
          replayChunks: msg.replayChunks ?? 0,
          resumed,
        })
        if (msg.cols && msg.rows) {
          stream.handlers.onSize(msg.cols, msg.rows, msg.controlling ?? false)
        }
        break
      }
      case 'size': {
        const stream = msg.sessionId ? this.streams.get(msg.sessionId) : undefined
        if (stream && msg.cols && msg.rows) {
          stream.handlers.onSize(msg.cols, msg.rows, msg.controlling ?? false)
        }
        break
      }
      case 'title': {
        const stream = msg.sessionId ? this.streams.get(msg.sessionId) : undefined
        if (stream && msg.text) stream.handlers.onTitle?.(msg.text)
        break
      }
      case 'clipboard': {
        const stream = msg.sessionId ? this.streams.get(msg.sessionId) : undefined
        if (stream && msg.text !== undefined) stream.handlers.onClipboard?.(msg.text)
        break
      }
      case 'dropped': {
        // The server cut this viewer off for falling behind. Resubscribing
        // replays from the ring, so the terminal catches up instead of
        // silently going dead.
        const stream = msg.sessionId ? this.streams.get(msg.sessionId) : undefined
        if (!stream || this.stale(stream, msg.ref)) return
        stream.ref = null
        this.sendSubscribe(stream)
        break
      }
      case 'exit': {
        const stream = msg.sessionId ? this.streams.get(msg.sessionId) : undefined
        if (!stream || this.stale(stream, msg.ref)) break
        stream.ended = true
        stream.handlers.onExit?.()
        break
      }
      case 'panel': {
        // Which project's note or list changed, not the content: pushing a
        // document to every viewer on every keystroke would be a waste, and
        // the panel that cares knows how to fetch it.
        const pid = (msg as { projectId?: string }).projectId
        const kind = (msg as { kind?: string }).kind
        if (pid && kind) {
          for (const fn of this.panelListeners) fn(pid, kind)
        }
        break
      }
      case 'resources': {
        const m = msg as { alert?: ResourceAlert | null; acted?: ResourceAction | null }
        for (const fn of this.resourceListeners) fn(m.alert ?? null, m.acted ?? null)
        break
      }
      case 'notice': {
        // Rebuilt from checked fields: the words are a session's, shown
        // through safeText by whoever draws them, and the shape is this.
        const m = msg as Partial<SessionNotice>
        if (typeof m.sessionId !== 'string' || typeof m.text !== 'string') break
        const n: SessionNotice = {
          t: 'notice',
          sessionId: m.sessionId,
          title: typeof m.title === 'string' ? m.title : '',
          project: typeof m.project === 'string' ? m.project : '',
          level: typeof m.level === 'string' ? m.level : 'info',
          text: m.text,
          choices: Array.isArray(m.choices) ? m.choices.filter((c): c is string => typeof c === 'string') : [],
          url: typeof m.url === 'string' ? m.url : '',
        }
        for (const fn of this.noticeListeners) fn(n)
        break
      }
      case 'state': {
        const st = msg as unknown as StateMessage
        // A session this client watched to its end is running again: restored
        // from its row, with the same id, so the mounted terminal is the right
        // one and only its stream is stale. Subscribe again, from the start --
        // the new attachment is a new stream, so there is nothing to resume --
        // and the confirmation that follows resets the terminal, the same path
        // a reconnect takes.
        for (const stream of this.streams.values()) {
          if (!stream.ended) continue
          const row = st.sessions.find((x) => x.id === stream.sessionId)
          if (!row || row.exited) continue
          stream.ended = false
          stream.position = null
          this.sendSubscribe(stream)
        }
        for (const fn of this.stateListeners) {
          fn({
            projects: st.projects,
            sessions: st.sessions,
            live: st.live,
            archived: st.archived ?? [],
            fullscreen: st.fullscreen ?? [],
            frozen: st.frozen ?? [],
            projectOrder: st.projectOrder,
            stale: st.stale ?? '',
            hasProjectOrder: st.hasProjectOrder ?? false,
            stateGuessed: st.stateGuessed,
            hooksInstalled: st.hooksInstalled ?? false,
            pluginsRev: st.pluginsRev ?? 0,
          })
        }
        break
      }
      case 'error': {
        // The server reporting a failed request without closing the
        // connection. There was no case here at all, so every one of them was
        // dropped: you typed into a terminal, the write failed server-side,
        // the server said so, and nothing reached the screen. Three of the six
        // senders are `write failed` twice and `paste failed`, which is the
        // hazard TestMessageTypesMatchTheClient was written about -- fixed for
        // `dropped` and not for this.
        const sessionId = msg.sessionId ?? ''
        const message = msg.message ?? 'the panel could not do that'
        if (sessionId) {
          const stream = this.streams.get(sessionId)
          if (stream) {
            this.clearAckTimer(stream)
            stream.handlers.onError?.(message)
          }
        }
        for (const fn of this.errorListeners) {
          fn(sessionId, message)
        }
        break
      }
      case 'pong':
        // Nothing to do: any frame refreshes lastSeenAt, which is the whole
        // purpose of the ping. A case rather than a silent fall-through to the
        // default below, which is now a compile error.
        break
      default: {
        // The third hop, pinned by the compiler. TestMessageTypesMatchTheClient
        // compares what the server sends against the declared union, and
        // `error` was in the union while having no case here, so it passed
        // while every error frame was dropped. This makes the switch itself the
        // thing that has to be exhaustive: adding a member to ServerMessage['t']
        // without handling it stops the build.
        const unhandled: never = msg.t
        void unhandled
        break
      }
    }
  }

  /**
   * Called when the server reports a failed request.
   *
   * Returns the unsubscribe function, in the shape useEffect wants.
   */
  onError(fn: (sessionId: string, message: string) => void) {
    this.errorListeners.add(fn)
    return () => {
      this.errorListeners.delete(fn)
    }
  }

  /**
   * Called when another viewer changed a project's note or todo list.
   *
   * Returns the unsubscribe function, in the shape useEffect wants.
   */
  onPanelChange(fn: (projectId: string, kind: string) => void) {
    this.panelListeners.add(fn)
    return () => {
      this.panelListeners.delete(fn)
    }
  }

  /** A session spoke up: `vibepanel me say` or `notify`. */
  onNotice(fn: (n: SessionNotice) => void) {
    this.noticeListeners.add(fn)
    return () => {
      this.noticeListeners.delete(fn)
    }
  }

  /**
   * Called when the memory question changes: a new one, a countdown starting,
   * or null when it has been answered or has gone away on its own.
   */
  onResourceAlert(fn: (alert: ResourceAlert | null, acted: ResourceAction | null) => void) {
    this.resourceListeners.add(fn)
    return () => {
      this.resourceListeners.delete(fn)
    }
  }

  /**
   * Which way round the page's palette is, read at the moment it is asked.
   *
   * From the computed token rather than from the stored choice, because
   * "system" is not a value -- it is a deferral to the device, and the answer
   * is whatever `prefers-color-scheme` resolved to. Reading the colour the page
   * is actually painted in gets all three cases with one expression.
   */
  private dark(): boolean {
    const bg = getComputedStyle(document.documentElement)
      .getPropertyValue('--vp-bg')
      .trim()
    const m = /^#?([0-9a-f]{2})([0-9a-f]{2})([0-9a-f]{2})$/i.exec(bg)
    if (!m) return true
    const [r, g, b] = [m[1], m[2], m[3]].map((h) => parseInt(h, 16))
    // Rec. 601 luma, which is enough to answer "is this a dark background".
    return 0.299 * r + 0.587 * g + 0.114 * b < 128
  }

  /**
   * Tell every watched session which theme the viewer is in.
   *
   * Called when the theme changes. An application that has already asked will
   * not ask again, so this does not repaint anything that is already running --
   * it is for the next one, and for anything watching mode 2031.
   */
  reportScheme() {
    const dark = this.dark()
    for (const sessionId of this.streams.keys()) {
      this.send({ t: 'scheme', sessionId, dark })
    }
  }

  subscribe(sessionId: string, cols: number, rows: number, handlers: StreamHandlers, hidden = false) {
    const prev = this.streams.get(sessionId)
    if (prev) this.clearAckTimer(prev)
    const stream: Stream = {
      sessionId,
      handlers,
      ref: null,
      cols,
      rows,
      confirmed: false,
      hidden,
      position: null,
      ended: false,
      awaitingSubSince: 0,
      ackTimer: null,
    }
    this.streams.set(sessionId, stream)
    this.sendSubscribe(stream)
  }

  /** Retry subscribing an already-mounted terminal from scratch. */
  resubscribe(sessionId: string) {
    const stream = this.streams.get(sessionId)
    if (!stream) return
    if (stream.ref !== null) this.byRef.delete(stream.ref)
    stream.ref = null
    stream.ended = false
    stream.position = null
    if (!this.ws && !this.closed) {
      this.backoff = 500
      this.connect()
      return
    }
    this.sendSubscribe(stream)
  }

  private clearAckTimer(stream: Stream) {
    stream.awaitingSubSince = 0
    if (stream.ackTimer !== null) {
      clearTimeout(stream.ackTimer)
      stream.ackTimer = null
    }
  }

  /**
   * One subscribe, first or again. Again, it says where the terminal is, and
   * a reconnect costs the output missed while away rather than the whole two
   * megabytes parsed onto a cleared screen for every mounted terminal.
   */
  private sendSubscribe(stream: Stream) {
    this.clearAckTimer(stream)
    if (this.ws?.readyState !== WebSocket.OPEN) return
    this.send({
      t: 'subscribe',
      sessionId: stream.sessionId,
      cols: stream.cols,
      rows: stream.rows,
      dark: this.dark(),
      hidden: stream.hidden,
      ...(stream.position ? { stream: stream.position.stream, since: stream.position.next } : {}),
    })
    stream.awaitingSubSince = Date.now()
    stream.ackTimer = setTimeout(() => {
      stream.ackTimer = null
      if (stream.awaitingSubSince === 0 || this.ws?.readyState !== WebSocket.OPEN) return
      // No `subscribed` confirmation after 5 s: probe the socket so a half-open
      // connection is detected and replaced instead of leaving the terminal at 0%.
      this.probe(WAKE_PING_TIMEOUT_MS)
    }, SUBSCRIBE_ACK_TIMEOUT_MS)
  }

  unsubscribe(sessionId: string) {
    const stream = this.streams.get(sessionId)
    if (!stream) return
    this.clearAckTimer(stream)
    this.streams.delete(sessionId)
    if (stream.ref !== null) this.byRef.delete(stream.ref)
    this.send({ t: 'unsubscribe', sessionId })
  }

  /** Sends text, encoded as UTF-8. */
  writeText(sessionId: string, text: string): boolean {
    return this.write(sessionId, new TextEncoder().encode(text))
  }

  /**
   * Sends a block of text as a paste rather than as typing.
   *
   * Typing it is what the compose box used to do, and a three-line
   * instruction arrived as three submissions: measured against a reader that
   * echoes one line at a time, "please refactor the auth flow / keep the
   * passkey path working / and do not touch the tmux config" came out as
   * three separate GOT<> lines. An agent acts on the first sentence before it
   * has read the third.
   *
   * The server routes this through tmux, which brackets it only if the pane's
   * application asked for bracketed paste. See MsgPaste.
   */
  pasteText(sessionId: string, text: string, submit = false): boolean {
    const stream = this.streams.get(sessionId)
    if (!stream || stream.ref === null || this.ws?.readyState !== WebSocket.OPEN) return false
    // `submit` is the server's job: the paste goes by the tmux command socket
    // and a return would go by the PTY, so sending them from here would be
    // racing two roads to the same pane.
    this.send({ t: 'paste', sessionId, text, submit })
    return true
  }

  /**
   * Sends raw bytes to a session, and says whether they left.
   *
   * False means nothing was sent: no socket, or no stream yet. The compose box
   * cleared itself without asking and the words were simply gone -- typed on a
   * phone in a lift, sent, the box emptied, and after the reconnect no pane had
   * them. A caller holding something the person wrote keeps it on false.
   */
  write(sessionId: string, data: Uint8Array): boolean {
    const stream = this.streams.get(sessionId)
    if (!stream || stream.ref === null || this.ws?.readyState !== WebSocket.OPEN) return false
    this.ws.send(encodeData(stream.ref, data))
    return true
  }

  resize(sessionId: string, cols: number, rows: number, takeControl = false) {
    const stream = this.streams.get(sessionId)
    if (!stream) return
    stream.cols = cols
    stream.rows = rows
    this.send({ t: takeControl ? 'takeControl' : 'resize', sessionId, cols, rows })
  }

  /**
   * Tells the server a mounted terminal went off-screen or came back.
   *
   * The server decides who drives a session's grid from who is looking at it.
   * Hiding gives the grid up the way closing the terminal used to, and showing
   * claims it back under the same rule as subscribing; without this a desktop
   * held the grid of every session it had merely visited. The answer is a
   * `size` message, which is how the terminal learns whether it still
   * controls. A value that has not changed sends nothing.
   */
  setHidden(sessionId: string, hidden: boolean) {
    const stream = this.streams.get(sessionId)
    if (!stream || stream.hidden === hidden) return
    stream.hidden = hidden
    this.send({ t: 'visibility', sessionId, hidden })
  }

  /** Reports how long a terminal took to load. See LoadTiming. */
  reportLoadTiming(sessionId: string, timing: LoadTiming) {
    if (!this.streams.has(sessionId)) return
    this.send({ t: 'loadTiming', sessionId, timing })
  }

  /**
   * Subscribes to pushed state snapshots.
   *
   * This is what replaced polling. Every viewer sees a change the moment it
   * happens instead of up to two seconds later, and an idle panel with six
   * tabs open sends nothing at all.
   */
  onState(fn: (s: PanelState) => void): () => void {
    this.stateListeners.add(fn)
    // Returns void rather than Set.delete's boolean so it can be handed
    // straight back from a React effect as its cleanup.
    return () => {
      this.stateListeners.delete(fn)
    }
  }

  onStatus(fn: (s: SocketStatus) => void): () => void {
    this.statusListeners.add(fn)
    fn(this.status)
    return () => {
      this.statusListeners.delete(fn)
    }
  }

  close() {
    this.closed = true
    clearTimeout(this.retry)
    this.retry = undefined
    this.detachLifecycle()
    this.stopPing()
    for (const stream of this.streams.values()) {
      this.clearAckTimer(stream)
    }
    this.ws?.close()
    this.ws = null
  }

  private setStatus(s: SocketStatus) {
    this.status = s
    for (const fn of this.statusListeners) fn(s)
  }

  private send(msg: ClientMessage) {
    if (this.ws?.readyState !== WebSocket.OPEN) return
    this.ws.send(JSON.stringify(msg))
  }

  /**
   * Send an application-level ping and arm a bounded response deadline. Any
   * incoming frame clears the deadline; silence past it means the socket is
   * half-open and is replaced immediately.
   */
  private probe(timeoutMs: number) {
    if (this.ws?.readyState !== WebSocket.OPEN) return
    if (this.pingDeadline !== null) return
    this.send({ t: 'ping' })
    this.pingDeadline = setTimeout(() => {
      this.pingDeadline = null
      this.dropConnection(true)
    }, timeoutMs)
  }

  private clearPingDeadline() {
    if (this.pingDeadline !== null) {
      clearTimeout(this.pingDeadline)
      this.pingDeadline = null
    }
  }

  private startPing() {
    this.stopPing()
    this.pingTimer = window.setInterval(() => {
      this.probe(PING_TIMEOUT_MS)
    }, PING_INTERVAL_MS)
  }

  private stopPing() {
    if (this.pingTimer !== null) {
      clearInterval(this.pingTimer)
      this.pingTimer = null
    }
    this.clearPingDeadline()
  }
}
