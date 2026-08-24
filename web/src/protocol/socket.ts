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
}

interface Stream {
  sessionId: string
  handlers: StreamHandlers
  ref: number | null
  /** Viewport last reported, replayed after a reconnect. */
  cols: number
  rows: number
}

/** Connection state, for the UI to show honestly rather than pretending. */
export type SocketStatus = 'connecting' | 'open' | 'closed'

const MAX_BACKOFF_MS = 10_000
const PING_INTERVAL_MS = 25_000

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
  private statusListeners = new Set<(s: SocketStatus) => void>()

  status: SocketStatus = 'closed'

  private stateListeners = new Set<(s: PanelState) => void>()

  connect() {
    if (this.ws || this.closed) return
    this.setStatus('connecting')

    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
    const ws = new WebSocket(`${proto}//${location.host}/ws`)
    ws.binaryType = 'arraybuffer'
    this.ws = ws

    ws.onopen = () => {
      this.backoff = 500
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
      case 'state': {
        const st = msg as unknown as StateMessage
        for (const fn of this.stateListeners) {
          fn({
            projects: st.projects,
            sessions: st.sessions,
            live: st.live,
            projectOrder: st.projectOrder,
          })
        }
        break
      }
    }
  }

  subscribe(sessionId: string, cols: number, rows: number, handlers: StreamHandlers) {
    const stream: Stream = { sessionId, handlers, ref: null, cols, rows }
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

  /** Sends keystrokes. Doing so also claims control of the grid, server-side. */
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
    this.pingTimer = window.setInterval(() => this.send({ t: 'ping' }), PING_INTERVAL_MS)
  }

  private stopPing() {
    if (this.pingTimer !== null) {
      clearInterval(this.pingTimer)
      this.pingTimer = null
    }
  }
}
