import { decodeData, encodeData } from './wire'
import type { ClientMessage, PanelState, ServerMessage, StateMessage } from './wire'

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
}

/** Connection state, for the UI to show honestly rather than pretending. */
export type SocketStatus = 'connecting' | 'open' | 'closed'

const MAX_BACKOFF_MS = 10_000
const PING_INTERVAL_MS = 25_000

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

const DEAD_AFTER_MS = 60_000

/**
 * One WebSocket for the whole page, shared by every terminal on it.
 *
 * Per-terminal sockets would be simpler, but a user with a main session and
 * four bottom terminals would hold five of them, and mobile browsers throttle
 * background sockets hard enough that some would stall without ever erroring.
 */
export class PanelSocket {
  private ws: WebSocket | null = null
  private streams = new Map<string, Stream>()
  private byRef = new Map<number, Stream>()
  private backoff = 500
  private pingTimer: number | null = null
  private closed = false
  /** When the last frame of any kind arrived. See DEAD_AFTER_MS. */
  private lastSeenAt = 0
  private statusListeners = new Set<(s: SocketStatus) => void>()

  status: SocketStatus = 'closed'

  private stateListeners = new Set<(s: PanelState) => void>()
  private panelListeners = new Set<(projectId: string, kind: string) => void>()

  constructor() {
    // The browser knows before we can: a phone leaving coverage, wifi
    // dropping, the laptop being closed. Without this the status dot stays
    // green — the socket is not closed, it is simply never going to hear
    // anything again — and the panel goes on claiming to be live while the
    // list of who needs you quietly stops updating. That is the worst way for
    // this particular application to fail, because nothing looks wrong.
    window.addEventListener('offline', this.onOffline)
    window.addEventListener('online', this.onOnline)
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

  /** Close the socket and let onclose start the reconnect. */
  private dropConnection() {
    const ws = this.ws
    if (!ws) {
      // Nothing open; make sure the status is not left claiming otherwise.
      if (this.status !== 'closed') this.setStatus('closed')
      return
    }
    // close() fires onclose asynchronously, and while the network is down a
    // socket can sit in CLOSING for a long time. Report the truth now.
    this.setStatus('closed')
    ws.close()
  }

  connect() {
    if (this.ws || this.closed) return
    this.setStatus('connecting')

    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
    const ws = new WebSocket(`${proto}//${location.host}/ws?client=${encodeURIComponent(clientID())}`)
    ws.binaryType = 'arraybuffer'
    this.ws = ws

    ws.onopen = () => {
      this.backoff = 500
      this.lastSeenAt = Date.now()
      this.setStatus('open')
      // Re-subscribe everything. After a reconnect the server has no memory of
      // this client, and each subscription replays from the ring buffer, so the
      // terminals repaint themselves rather than sitting frozen.
      for (const stream of this.streams.values()) {
        stream.ref = null
        this.send({
          t: 'subscribe',
          sessionId: stream.sessionId,
          cols: stream.cols,
          rows: stream.rows,
        })
      }
      this.startPing()
    }

    ws.onmessage = (ev) => {
      // Any frame, of any kind, is proof the connection is alive.
      this.lastSeenAt = Date.now()
      if (ev.data instanceof ArrayBuffer) {
        const frame = decodeData(ev.data)
        if (!frame) return
        this.byRef.get(frame.ref)?.handlers.onData(frame.payload, frame.replay)
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
      this.ws = null
      this.stopPing()
      for (const stream of this.streams.values()) stream.ref = null
      this.byRef.clear()
      this.setStatus('closed')
      if (!this.closed) {
        setTimeout(() => this.connect(), this.backoff)
        this.backoff = Math.min(this.backoff * 2, MAX_BACKOFF_MS)
      }
    }

    ws.onerror = () => {
      // onclose always follows, and that is where reconnection is handled.
      // Closing here as well would double the backoff on every failure.
    }
  }

  private handleControl(msg: ServerMessage) {
    switch (msg.t) {
      case 'subscribed': {
        const stream = msg.sessionId ? this.streams.get(msg.sessionId) : undefined
        if (!stream || msg.ref === undefined) return
        // A second confirmation for the same session means the subscription
        // restarted, so the replay that follows is the whole buffer again.
        // Clearing here rather than on the replay frame keeps this correct
        // whether the snapshot arrives in one frame or several.
        if (stream.confirmed) stream.handlers.onReset?.()
        stream.confirmed = true
        stream.ref = msg.ref
        this.byRef.set(msg.ref, stream)
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
        if (!stream) return
        stream.ref = null
        this.send({
          t: 'subscribe',
          sessionId: stream.sessionId,
          cols: stream.cols,
          rows: stream.rows,
        })
        break
      }
      case 'exit': {
        const stream = msg.sessionId ? this.streams.get(msg.sessionId) : undefined
        stream?.handlers.onExit?.()
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
      case 'state': {
        const st = msg as unknown as StateMessage
        for (const fn of this.stateListeners) {
          fn({
            projects: st.projects,
            sessions: st.sessions,
            live: st.live,
            projectOrder: st.projectOrder,
            stateGuessed: st.stateGuessed,
          })
        }
        break
      }
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

  subscribe(sessionId: string, cols: number, rows: number, handlers: StreamHandlers) {
    const stream: Stream = { sessionId, handlers, ref: null, cols, rows, confirmed: false }
    this.streams.set(sessionId, stream)
    this.send({ t: 'subscribe', sessionId, cols, rows })
  }

  unsubscribe(sessionId: string) {
    const stream = this.streams.get(sessionId)
    if (!stream) return
    this.streams.delete(sessionId)
    if (stream.ref !== null) this.byRef.delete(stream.ref)
    this.send({ t: 'unsubscribe', sessionId })
  }

  /** Sends text, encoded as UTF-8. */
  writeText(sessionId: string, text: string) {
    this.write(sessionId, new TextEncoder().encode(text))
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
  pasteText(sessionId: string, text: string, submit = false) {
    const stream = this.streams.get(sessionId)
    if (!stream || stream.ref === null || this.ws?.readyState !== WebSocket.OPEN) return
    // `submit` is the server's job: the paste goes by the tmux command socket
    // and a return would go by the PTY, so sending them from here would be
    // racing two roads to the same pane.
    this.send({ t: 'paste', sessionId, text, submit })
  }

  /** Sends raw bytes to a session. */
  write(sessionId: string, data: Uint8Array) {
    const stream = this.streams.get(sessionId)
    if (!stream || stream.ref === null || this.ws?.readyState !== WebSocket.OPEN) return
    this.ws.send(encodeData(stream.ref, data))
  }

  resize(sessionId: string, cols: number, rows: number, takeControl = false) {
    const stream = this.streams.get(sessionId)
    if (!stream) return
    stream.cols = cols
    stream.rows = rows
    this.send({ t: takeControl ? 'takeControl' : 'resize', sessionId, cols, rows })
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
    window.removeEventListener('offline', this.onOffline)
    window.removeEventListener('online', this.onOnline)
    this.stopPing()
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

  private startPing() {
    this.stopPing()
    this.pingTimer = window.setInterval(() => {
      // Check before pinging: send() on a socket whose network has gone is not
      // an error, it just buffers, so pinging alone proves nothing and the
      // status stayed green forever. What the pings are for is giving the
      // server something to answer, so that silence means something.
      if (this.lastSeenAt > 0 && Date.now() - this.lastSeenAt > DEAD_AFTER_MS) {
        this.dropConnection()
        return
      }
      this.send({ t: 'ping' })
    }, PING_INTERVAL_MS)
  }

  private stopPing() {
    if (this.pingTimer !== null) {
      clearInterval(this.pingTimer)
      this.pingTimer = null
    }
  }
}
